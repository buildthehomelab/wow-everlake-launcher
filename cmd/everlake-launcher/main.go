// Command everlake-launcher installs and checks the pinned WoW Forever build from Blizzard's CDN.
//
// Started with no command (a double-click), it opens the launcher's window: Install / Verify / Play buttons and the
// sign-in fields, for the install folder C:\Everlake (Windows) or ~/Everlake (macOS).
//
//	everlake-launcher install --dir <folder> [--tags OSX,arm64,enUS,US] [--no-data] [--max-priority N] [--data-limit-mb N]
//	everlake-launcher verify  --dir <folder> [--tags ...]
//	everlake-launcher show    [--tags ...]          the pinned build: tags and sizes, nothing written
//	everlake-launcher pin     [--product wowf]      prints a pin for the build that is live now (to review, not to follow)
//	everlake-launcher start   --dir <folder> [-- game arguments]   starts the installed game with the arguments it needs
//	everlake-launcher play    --dir <folder> [--login-host <name>] [--email <address>]   signs in and joins a realm (Windows)
//	everlake-launcher ui      [--dir <folder>] [--browser]   the launcher's window (or the same page in the browser)
//	everlake-launcher mirror  --store <folder> [--prefetch]   Everlake's own CDN (runs on the server)
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/appwindow"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/webui"
)

var version = "dev"

// The window has to run on the main thread (Cocoa insists, and WebView2 wants one thread for its window), and only
// the main goroutine can be pinned there: before main starts.
func init() { runtime.LockOSThread() }

func main() {
	cdn.UserAgent = "everlake-launcher/" + version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Finder used to add -psn_<number> when opening an app; it isn't a command.
	if len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "-psn_") {
		os.Args = os.Args[:1]
	}
	if len(os.Args) < 2 {
		// A double-click: no terminal, so errors go in a message box.
		if err := cmdApp(ctx); err != nil {
			appwindow.Alert("Everlake launcher", err.Error())
			os.Exit(1)
		}
		return
	}
	attachConsole()
	stdin = bufio.NewReader(os.Stdin)
	var err error
	switch os.Args[1] {
	case "install":
		err = cmdInstall(ctx, os.Args[2:], false)
	case "verify":
		err = cmdInstall(ctx, os.Args[2:], true)
	case "show":
		err = cmdShow(ctx, os.Args[2:])
	case "pin":
		err = cmdPin(ctx, os.Args[2:])
	case "start":
		err = cmdStart(os.Args[2:])
	case "play":
		err = cmdPlay(ctx, os.Args[2:])
	case "ui":
		err = cmdUI(ctx, os.Args[2:])
	case "mirror":
		err = cmdMirror(ctx, os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "everlake-launcher:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage (with no command it opens the launcher's window):
  everlake-launcher install --dir <folder> [--tags OSX,arm64,enUS,US] [--jobs 8]
                            [--no-data] [--max-priority N] [--data-limit-mb N]
  everlake-launcher verify  --dir <folder> [--quick]   (full hash check, or --quick: size and timestamps only)
  everlake-launcher show    [--tags ...]
  everlake-launcher pin     [--product wowf] [--region us]
  everlake-launcher start   --dir <folder> [-- game arguments]
  everlake-launcher play    --dir <folder> [--login-host <name>] [--email <address>] [--server-key <hex>] [--ca <file>]
  everlake-launcher ui      [--dir <folder>] [--browser | --no-open] [--addr 127.0.0.1:PORT]
  everlake-launcher mirror  --store <folder> [--overlay <folder>] [--addr :8087] [--prefetch | --prefetch-only]
                            [--tags OSX,Windows,x86_64,arm64,enUS,US] [--rate-mb 40] [--quiet]
`)
	os.Exit(2)
}

// defaultTags are the tags for the machine the launcher runs on, in the names WoW's manifests use: platform,
// architecture, locale and region. The region matters: the Mac game binary for CN differs from everyone else's.
func defaultTags() string {
	platform, arch := "Windows", "x86_64"
	if runtime.GOOS == "darwin" {
		platform = "OSX"
	}
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return platform + "," + arch + ",enUS,US"
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func logf(format string, args ...any) {
	fmt.Printf("%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func cmdInstall(ctx context.Context, args []string, verify bool) error {
	name := "install"
	if verify {
		name = "verify"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	dir := fs.String("dir", "", "install folder (required)")
	tags := fs.String("tags", defaultTags(), "manifest tags to install: platform, architecture, locale")
	jobs := fs.Int("jobs", 8, "parallel requests")
	noData := fs.Bool("no-data", false, "only the loose files (the game executable and its friends)")
	maxPrio := fs.Int("max-priority", 1<<30, "only data files at or below this download priority")
	limitMB := fs.Int64("data-limit-mb", 0, "stop the data stage after about this many MB (trial runs)")
	hosts := fs.String("cdn", "", "comma-separated CDN hosts to try before Blizzard's (a mirror)")
	quick := fs.Bool("quick", false, "verify: check the client files by size and timestamp only (fast, no hashing)")
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("%s: --dir is required", name)
	}
	if verify && *quick {
		return quickVerify(*dir)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	p := pin.Default()
	opts := install.Options{
		Dir: *dir, Pin: p, Tags: splitTags(*tags), Jobs: *jobs, Verify: verify, NoData: *noData,
		DataLimit: *limitMB << 20, Log: logf,
	}
	if set["max-priority"] {
		opts.MaxPriority = maxPrio
	}
	if verify {
		// Check what the install fetched, unless told otherwise.
		st, err := install.ReadState(*dir)
		if err != nil {
			return fmt.Errorf("%s: %w", *dir, install.ErrNoInstall)
		}
		if st.Pin.BuildConfig != p.BuildConfig {
			logf("note: %s holds %s, this launcher pins %s; checking against %s", *dir, st.Pin.Version, p.Version,
				p.Version)
		}
		if !set["tags"] {
			opts.Tags = st.Tags
		}
		if !set["no-data"] {
			opts.NoData = st.NoData
		}
		if !set["max-priority"] {
			opts.MaxPriority = st.MaxPriority
		}
		opts.PartialData = st.DataLimit > 0
	}
	// The game streams from Blizzard's hosts; a --cdn mirror is only tried first during the install.
	opts.GameHosts = p.Hosts(ctx, nil)
	opts.CDN = cdn.New(append(splitTags(*hosts), opts.GameHosts...), p.CDNPath)
	logf("%s WoW Forever %s into %s (tags %s) from %s", name, p.Version, *dir, strings.Join(opts.Tags, " "),
		strings.Join(opts.CDN.Hosts, ", "))
	start := time.Now()
	rep, err := install.Run(ctx, opts)
	if err != nil {
		return err
	}
	logf("loose files: %d (%d already there, %d fetched)", rep.LooseFiles, rep.LooseKept, rep.LooseFetched)
	if !opts.NoData {
		logf("data files: %d (%d already stored, %d fetched, %d left out by --data-limit-mb)",
			rep.DataFiles, rep.DataKept, rep.DataFetched, rep.DataSkippedByLimit)
	}
	logf("downloaded %.1f MB in %s", float64(rep.BytesDownloaded)/(1<<20), time.Since(start).Round(time.Second))
	for _, b := range rep.Bad {
		logf("BAD     %s", b)
	}
	for _, m := range rep.Missing {
		logf("MISSING %s", m)
	}
	if rep.Failed() {
		return fmt.Errorf("%d bad, %d missing", len(rep.Bad), len(rep.Missing))
	}
	if verify {
		if opts.PartialData {
			logf("everything checked is intact (a trial install: data files it never fetched aren't counted)")
		} else {
			logf("everything checked is intact")
		}
	}
	return nil
}

// quickVerify checks the client files by size and timestamp only — the fast check for every launch. It falls back
// to a note when the install is too old to have fingerprints.
func quickVerify(dir string) error {
	start := time.Now()
	rep, err := install.QuickVerify(dir)
	if errors.Is(err, install.ErrNoFingerprints) {
		logf("%s was installed before quick checks; run 'verify' (without --quick) for a full hash check", dir)
		return nil
	}
	if err != nil {
		return err
	}
	for _, b := range rep.Bad {
		logf("CHANGED %s", b)
	}
	for _, m := range rep.Missing {
		logf("MISSING %s", m)
	}
	logf("quick check of %s in %s", dir, time.Since(start).Round(time.Millisecond))
	if rep.Failed() {
		return fmt.Errorf("%d changed, %d missing; run 'verify' (without --quick) to repair", len(rep.Bad),
			len(rep.Missing))
	}
	logf("client files intact (size and timestamps; the game data isn't deep-checked — 'verify' does that)")
	return nil
}

// cmdShow reads the pinned build's manifests from the CDN and prints their tags and sizes, writing nothing.
func cmdShow(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	tags := fs.String("tags", defaultTags(), "tags to total up")
	fs.Parse(args)
	p := pin.Default()
	c := cdn.New(p.Hosts(ctx, nil), p.CDNPath)
	buildKey, _ := p.Keys()
	raw, err := c.Get(ctx, "config", buildKey, "")
	if err != nil {
		return err
	}
	if tact.KeyOf(raw) != buildKey {
		return fmt.Errorf("build config doesn't match its name")
	}
	build := tact.ParseConfig(string(raw))
	fmt.Printf("pin: %s %s (%s), build config %s, CDN config %s\n", p.Product, p.Version, build.One("build-name"),
		p.BuildConfig, p.CDNConfig)
	get := func(name string) ([]byte, error) {
		ck, ek, err := build.KeyPair(name)
		if err != nil {
			return nil, err
		}
		if ek.IsZero() {
			return nil, fmt.Errorf("%s has no encoded key in the build config", name)
		}
		data, err := c.Get(ctx, "data", ek, "")
		if err != nil {
			return nil, err
		}
		return tact.Decode(data, ek, ck)
	}
	instData, err := get("install")
	if err != nil {
		return fmt.Errorf("install manifest: %w", err)
	}
	inst, err := tact.ParseInstall(instData)
	if err != nil {
		return err
	}
	dlData, err := get("download")
	if err != nil {
		return fmt.Errorf("download manifest: %w", err)
	}
	dl, err := tact.ParseDownload(dlData)
	if err != nil {
		return err
	}
	fmt.Printf("\ninstall manifest: %d files\n", len(inst.Entries))
	printTags(inst.Tags, len(inst.Entries))
	fmt.Printf("\ndownload manifest (version %d): %d files\n", dl.Version, len(dl.Entries))
	printTags(dl.Tags, len(dl.Entries))

	want := splitTags(*tags)
	files, err := inst.Select(want)
	if err != nil {
		return err
	}
	var isz uint64
	for _, f := range files {
		isz += uint64(f.Size)
	}
	fmt.Printf("\nfor %s:\n  install manifest: %d files, %.1f MB\n", strings.Join(want, " "), len(files),
		float64(isz)/(1<<20))
	for i, f := range files {
		if i == 12 {
			fmt.Printf("    ... %d more\n", len(files)-i)
			break
		}
		fmt.Printf("    %10d  %s\n", f.Size, f.Name)
	}
	data, err := dl.Select(want)
	if err != nil {
		return err
	}
	byPrio := map[int][2]uint64{}
	var dsz uint64
	for _, d := range data {
		v := byPrio[d.Priority]
		byPrio[d.Priority] = [2]uint64{v[0] + 1, v[1] + d.Size}
		dsz += d.Size
	}
	fmt.Printf("  download manifest: %d files, %.1f GB stored\n", len(data), float64(dsz)/(1<<30))
	var prios []int
	for k := range byPrio {
		prios = append(prios, k)
	}
	sort.Ints(prios)
	for _, k := range prios {
		fmt.Printf("    priority %3d: %8d files, %8.1f MB\n", k, byPrio[k][0], float64(byPrio[k][1])/(1<<20))
	}
	return nil
}

func printTags(tags []tact.Tag, entries int) {
	byType := map[uint16][]string{}
	for _, t := range tags {
		byType[t.Type] = append(byType[t.Type], fmt.Sprintf("%s(%d)", t.Name, t.Count(entries)))
	}
	var types []int
	for t := range byType {
		types = append(types, int(t))
	}
	sort.Ints(types)
	for _, t := range types {
		fmt.Printf("  tag type %d: %s\n", t, strings.Join(byType[uint16(t)], " "))
	}
}

func cmdPin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pin", flag.ExitOnError)
	product := fs.String("product", "wowf", "TACT product")
	region := fs.String("region", "us", "version server region")
	folder := fs.String("game-folder", pin.Default().GameFolder, "game folder name for the pin")
	flavor := fs.String("flavor", pin.Default().Flavor, "product flavor for .flavor.info")
	fs.Parse(args)
	p, err := pin.Live(ctx, *product, *region, *folder, *flavor)
	if err != nil {
		return err
	}
	cur := pin.Default()
	if p.BuildConfig == cur.BuildConfig && p.CDNConfig == cur.CDNConfig {
		p.Note = cur.Note
		fmt.Fprintf(os.Stderr, "the live build is the pinned one (%s)\n", cur.Version)
	} else {
		fmt.Fprintf(os.Stderr, "live is %s, this launcher pins %s; review before changing internal/pin/wowf.json:\n",
			p.Version, cur.Version)
	}
	out, _ := json.MarshalIndent(p, "", "  ")
	fmt.Println(string(out))
	return nil
}

// cmdStart starts the installed game with the arguments Battle.net would give it (-uid <product>), plus any after
// "--". It doesn't log in anywhere: the game shows its own login screen.
func cmdStart(args []string) error {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	dir := fs.String("dir", "", "install folder (required)")
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("start: --dir is required")
	}
	pid, err := install.Start(*dir, fs.Args()...)
	if err != nil {
		return err
	}
	logf("started the game (process %d)", pid)
	return nil
}

// installOptions builds the options the ui's Install and Verify buttons use. Install does a full install/repair;
// verify reads what the install recorded.
func installOptions(ctx context.Context, dir string, verify bool) install.Options {
	p := pin.Default()
	opts := install.Options{Dir: dir, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, Verify: verify}
	if verify {
		if st, err := install.ReadState(dir); err == nil {
			opts.Tags, opts.NoData, opts.MaxPriority = st.Tags, st.NoData, st.MaxPriority
			opts.PartialData = st.DataLimit > 0
		}
	}
	opts.GameHosts = p.Hosts(ctx, nil)
	opts.CDN = cdn.New(opts.GameHosts, p.CDNPath)
	return opts
}

// runInstall runs an install or verify and reports the outcome through log.
func runInstall(ctx context.Context, opts install.Options, log func(string)) error {
	opts.Log = func(f string, a ...any) { log(fmt.Sprintf(f, a...)) }
	start := time.Now()
	rep, err := install.Run(ctx, opts)
	if err != nil {
		return err
	}
	log(fmt.Sprintf("loose files: %d (%d already there, %d fetched)", rep.LooseFiles, rep.LooseKept, rep.LooseFetched))
	if !opts.NoData {
		log(fmt.Sprintf("data files: %d (%d already stored, %d fetched, %d left out by a data limit)",
			rep.DataFiles, rep.DataKept, rep.DataFetched, rep.DataSkippedByLimit))
	}
	log(fmt.Sprintf("downloaded %.1f MB in %s", float64(rep.BytesDownloaded)/(1<<20), time.Since(start).Round(time.Second)))
	for _, b := range rep.Bad {
		log("BAD     " + b)
	}
	for _, m := range rep.Missing {
		log("MISSING " + m)
	}
	if rep.Failed() {
		return fmt.Errorf("%d bad, %d missing", len(rep.Bad), len(rep.Missing))
	}
	if opts.Verify {
		log("everything checked is intact")
	}
	return nil
}

// defaultDir is the install folder the window uses when none is given: C:\Everlake on Windows (where the test builds
// have always installed), ~/Everlake elsewhere.
func defaultDir() string {
	if runtime.GOOS == "windows" {
		return `C:\Everlake`
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Everlake")
	}
	return "Everlake"
}

// How the ui shows its page.
type uiShow int

const (
	showWindow  uiShow = iota // the launcher's own window, else the browser
	showBrowser               // the system browser
	showNothing               // just serve (--no-open)
)

// cmdApp is what a double-click runs: the window for the default folder.
func cmdApp(ctx context.Context) error {
	return runUI(ctx, defaultDir(), "127.0.0.1:0", showWindow, true)
}

func cmdUI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "install folder")
	addr := fs.String("addr", "127.0.0.1:0", "address to listen on (loopback only)")
	browser := fs.Bool("browser", false, "show the page in the browser instead of the launcher's window")
	noOpen := fs.Bool("no-open", false, "show nothing: just serve the page")
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("ui: --dir is required")
	}
	if !strings.HasPrefix(*addr, "127.0.0.1:") && !strings.HasPrefix(*addr, "localhost:") && !strings.HasPrefix(*addr, "[::1]:") {
		return fmt.Errorf("ui: --addr must be on the loopback address (127.0.0.1, localhost or [::1])")
	}
	show := showWindow
	if *browser {
		show = showBrowser
	}
	if *noOpen {
		show = showNothing
	}
	return runUI(ctx, *dir, *addr, show, false)
}

// runUI serves the launcher's page on the loopback address and shows it. In the window, closing it ends the
// launcher (after any game it's still patching closes). In the browser, Ctrl-C ends it; or, started with no
// terminal (detached), it ends a while after the last page closes.
func runUI(ctx context.Context, dir, addr string, show uiShow, detached bool) error {
	srv := webui.New(dir, pin.Default().Version, webui.Actions{
		Install: func(ctx context.Context, log func(string)) error {
			return runInstall(ctx, installOptions(ctx, dir, false), log)
		},
		Verify: func(ctx context.Context, log func(string)) error {
			return runInstall(ctx, installOptions(ctx, dir, true), log)
		},
		Play:  uiPlay(dir),
		Realm: uiRealm(dir),
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String() + "/"
	hs := &http.Server{Handler: srv.Handler()}
	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	defer hs.Close()

	if show == showWindow {
		err := appwindow.Run(appwindow.Options{Title: "Everlake launcher", URL: url, Stop: ctx.Done()})
		if err == nil {
			waitForGames(ctx)
			return nil
		}
		logf("no app window (%v); showing the page in the browser instead", err)
		show = showBrowser
	}
	logf("Everlake launcher page at %s  (Ctrl-C to stop)", url)
	if show == showBrowser {
		openBrowser(url)
	}
	var idle <-chan time.Time
	if detached {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		idle = t.C
	}
	for {
		select {
		case err := <-served:
			if err != http.ErrServerClosed {
				return err
			}
			return nil
		case <-ctx.Done():
			return nil
		case <-idle:
			// Nobody can press Ctrl-C on a launcher started without a terminal: it ends once its page has been
			// closed for a while, nothing is running, and no game needs it.
			if srv.Unwatched(2*time.Minute) && gamesRunning() == 0 {
				return nil
			}
		}
	}
}

// openBrowser points the system browser at url, best effort.
func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
		args = []string{url}
	case "windows":
		name = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		name = "xdg-open"
		args = []string{url}
	}
	_ = exec.Command(name, args...).Start()
}

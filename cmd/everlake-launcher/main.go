// Command everlake-launcher installs and checks the pinned WoW Forever build from Blizzard's CDN.
//
//	everlake-launcher install --dir <folder> [--tags OSX,arm64,enUS,US] [--no-data] [--max-priority N] [--data-limit-mb N]
//	everlake-launcher verify  --dir <folder> [--tags ...]
//	everlake-launcher show    [--tags ...]          the pinned build: tags and sizes, nothing written
//	everlake-launcher pin     [--product wowf]      prints a pin for the build that is live now (to review, not to follow)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

var version = "dev"

func main() {
	cdn.UserAgent = "everlake-launcher/" + version
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	fmt.Fprint(os.Stderr, `usage:
  everlake-launcher install --dir <folder> [--tags OSX,arm64,enUS,US] [--jobs 8]
                            [--no-data] [--max-priority N] [--data-limit-mb N]
  everlake-launcher verify  --dir <folder>          (checks what install fetched; --tags etc. override)
  everlake-launcher show    [--tags ...]
  everlake-launcher pin     [--product wowf] [--region us]
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
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("%s: --dir is required", name)
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
	fs.Parse(args)
	p, err := pin.Live(ctx, *product, *region, *folder)
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

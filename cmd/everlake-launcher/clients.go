package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/settings"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/webui"
	"os"
	"path/filepath"
	"runtime"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

func retailPin(path string) (pin.Pin, error) {
	if path == "" {
		return pin.Pin{}, fmt.Errorf("Retail needs a reviewed, server-compatible pin JSON file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return pin.Pin{}, err
	}
	p, err := pin.Parse(data)
	if err == nil && (p.Product != "wow" || p.GameFolder != "_retail_" || p.Flavor != "wow") {
		err = fmt.Errorf("Retail requires product and flavor wow and game folder _retail_")
	}
	return p, err
}

func cmdClients(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("clients expects discover, import, install, verify or start")
	}
	fs := flag.NewFlagSet("clients "+args[0], flag.ContinueOnError)
	dir := fs.String("dir", "", "client root (import destination must be unused)")
	source := fs.String("source", "", "existing client root for import or install reuse")
	retail := fs.Bool("retail", false, "use the embedded TrinityCore-compatible Retail build")
	adopt := fs.Bool("in-place", false, "update a single-product existing install without making another client copy")
	bootstrap := fs.Bool("streaming", false, "install launch files first; game assets download to disk during play")
	pinFile := fs.String("retail-pin", "", "reviewed Retail pin JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "discover" {
		candidates := fs.Args()
		if len(candidates) == 0 {
			candidates = []string{uiDir(), defaultDir()}
		}
		seen := map[string]bool{}
		for _, root := range candidates {
			root = filepath.Clean(root)
			if seen[root] {
				continue
			}
			seen[root] = true
			rows, err := install.ActiveBuilds(root)
			if err != nil {
				logf("%s: %v", root, err)
				continue
			}
			for _, r := range rows {
				logf("%s: %s %s (%s / %s)", root, r.Product, r.Version, r.Build, r.CDN)
			}
		}
		return nil
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}
	p := pin.Default()
	if *retail {
		p = pin.Retail()
	}
	if *pinFile != "" {
		var err error
		p, err = retailPin(*pinFile)
		if err != nil {
			return err
		}
	}
	log := func(s string) { logf("%s", s) }
	switch args[0] {
	case "import":
		if *source == "" {
			return fmt.Errorf("--source is required")
		}
		return install.Import(ctx, *source, *dir, p, log)
	case "install", "verify":
		if args[0] == "install" && *adopt && p.Product == "wowf" {
			if *source != "" {
				return fmt.Errorf("in-place update does not use a separate source")
			}
			return installSharedClientMode(ctx, *dir, false, true, *bootstrap, log)
		}
		if (*source != "" || *adopt) && args[0] == "install" {
			hosts := p.Hosts(ctx, nil)
			opts := install.Options{Adopt: *adopt, Dir: *dir, ReuseFrom: *source, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, GameHosts: hosts, CDN: cdn.New(hosts, p.CDNPath)}
			if *bootstrap {
				priority := 0
				opts.MaxPriority = &priority
			}
			return runInstall(ctx, opts, log)
		}
		if *bootstrap && args[0] == "install" {
			hosts := p.Hosts(ctx, nil)
			priority := 0
			return runInstall(ctx, install.Options{Dir: *dir, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, MaxPriority: &priority, GameHosts: hosts, CDN: cdn.New(hosts, p.CDNPath)}, log)
		}
		if p.Product == "wowf" {
			return installSharedClient(ctx, *dir, args[0] == "verify", log)
		}
		return runRetail(ctx, *dir, p, args[0] == "verify", log)
	case "start":
		return startClient(*dir, p, log)
	default:
		return fmt.Errorf("unknown clients command %q", args[0])
	}
}

func runRetail(ctx context.Context, dir string, p pin.Pin, verify bool, log func(string)) error {
	opts := install.Options{Dir: dir, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, Verify: verify}
	if verify {
		if err := install.CheckBuild(dir, p); err != nil {
			return err
		}
		st, err := install.ReadState(dir)
		if err != nil {
			return err
		}
		if len(st.Tags) > 0 {
			opts.Tags = st.Tags
		}
		opts.NoData, opts.MaxPriority, opts.PartialData = st.NoData, st.MaxPriority, st.DataLimit > 0
	}
	opts.GameHosts = p.Hosts(ctx, nil)
	opts.CDN = cdn.New(opts.GameHosts, p.CDNPath)
	return runInstall(ctx, opts, log)
}

func startClient(dir string, p pin.Pin, log func(string)) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return fmt.Errorf("client launch is available on macOS and Windows")
	}
	if err := install.CheckBuild(dir, p); err != nil {
		return err
	}
	state, err := install.ReadState(dir)
	if err != nil {
		return err
	}
	if state.Pin.GameFolder != p.GameFolder || state.Pin.Product != p.Product || state.Pin.BuildConfig != p.BuildConfig ||
		state.Pin.CDNConfig != p.CDNConfig || state.Pin.Flavor != p.Flavor {
		return fmt.Errorf("installed launcher state differs from the selected client")
	}
	pid, err := install.Start(dir)
	if err == nil {
		log(fmt.Sprintf("Started %s %s (process %d)", p.Product, p.Version, pid))
	}
	return err
}

func retailRoot(forever string) string {
	if st, err := settings.Load(); err == nil && st.RetailDir != "" {
		return st.RetailDir
	}
	return filepath.Clean(forever) + "-Retail"
}

func retailStatus(forever string) (version, note string, ready bool) {
	p, err := configuredRetailPin()
	if err != nil {
		return "", "Retail needs a reviewed compatible build. It uses separate storage for downloaded assets.", false
	}
	dir := retailRoot(forever)
	_, err = install.LaunchState(dir)
	return p.Version, fmt.Sprintf("Retail %s uses separate storage at %s.", p.Version, dir), err == nil && install.CheckBuild(dir, p) == nil
}

func retailAction(ctx context.Context, forever, action string, log func(string)) error {
	p, err := configuredRetailPin()
	if err != nil {
		return err
	}
	dir := retailRoot(forever)
	switch action {
	case "retail-play":
		return startClient(dir, p, log)
	case "retail-stream":
		hosts := p.Hosts(ctx, nil)
		priority := 0
		return runInstall(ctx, install.Options{Dir: dir, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, MaxPriority: &priority, GameHosts: hosts, CDN: cdn.New(hosts, p.CDNPath)}, log)
	case "retail-install":
		if gamesRunning() != 0 {
			return fmt.Errorf("close the running game before installing Retail")
		}
		return runRetail(ctx, dir, p, false, log)
	case "retail-verify":
		return runRetail(ctx, dir, p, true, log)
	default:
		return fmt.Errorf("unknown Retail action")
	}
}

func configuredRetailPin() (pin.Pin, error) {
	if path := os.Getenv("EVERLAKE_RETAIL_PIN"); path != "" {
		return retailPin(path)
	}
	st, err := settings.Load()
	if err != nil {
		return pin.Pin{}, err
	}
	if len(st.RetailPin) == 0 {
		return pin.Retail(), nil
	}
	return parseRetailPin(st.RetailPin)
}

func parseRetailPin(data []byte) (pin.Pin, error) {
	p, err := pin.Parse(data)
	if err != nil {
		return p, err
	}
	if p.Product != "wow" || p.Flavor != "wow" || p.GameFolder != "_retail_" {
		return p, fmt.Errorf("Retail requires product and flavor wow and game folder _retail_")
	}
	return p, nil
}

func clientAction(ctx context.Context, dir, action string, in webui.PlayInput, log func(string)) error {
	if action == "client-configure" {
		p, err := parseRetailPin([]byte(in.PinJSON))
		if err != nil {
			return err
		}
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err := settings.Update(func(s *settings.Settings) { s.RetailPin = data }); err != nil {
			return err
		}
		log("Saved Retail build " + p.Version)
		return nil
	}
	if in.Source == "" {
		return fmt.Errorf("choose the existing client folder")
	}
	rows, err := install.ActiveBuilds(in.Source)
	if err != nil {
		return err
	}
	for _, row := range rows {
		log(fmt.Sprintf("%s %s, build %s, CDN %s", row.Product, row.Version, row.Build, row.CDN))
	}
	if action == "client-inspect" {
		report, err := install.Inventory(ctx, in.Source)
		if err != nil {
			return err
		}
		log(fmt.Sprintf("%d files, %d bytes. A full copy needs this space before any verified reuse.", report.Files, report.Bytes))
		return nil
	}
	if gamesRunning() != 0 {
		return fmt.Errorf("close the game before importing")
	}
	if action == "client-adopt" {
		in.Destination = in.Source
	}
	if in.Destination == "" {
		return fmt.Errorf("choose an unused destination folder")
	}
	p := pin.Default()
	if in.Product == "wow" {
		p, err = configuredRetailPin()
		if err != nil {
			return err
		}
	} else if in.Product != "wowf" {
		return fmt.Errorf("choose Forever or Retail")
	}
	if action == "client-adopt" && p.Product == "wowf" {
		if err := installSharedClientMode(ctx, in.Source, false, true, false, log); err != nil {
			return err
		}
	} else if action == "client-adopt" {
		hosts := p.Hosts(ctx, nil)
		if err := runInstall(ctx, install.Options{Dir: in.Source, Adopt: true, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, GameHosts: hosts, CDN: cdn.New(hosts, p.CDNPath)}, log); err != nil {
			return err
		}
	} else if action == "client-reuse" {
		hosts := p.Hosts(ctx, nil)
		if err := runInstall(ctx, install.Options{Dir: in.Destination, ReuseFrom: in.Source, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, GameHosts: hosts, CDN: cdn.New(hosts, p.CDNPath)}, log); err != nil {
			return err
		}
	} else if err := install.Import(ctx, in.Source, in.Destination, p, log); err != nil {
		return err
	}
	if in.Product == "wow" {
		if err := settings.Update(func(s *settings.Settings) { s.RetailDir = in.Destination }); err != nil {
			return err
		}
	}
	if action == "client-adopt" {
		log("Updated in place at " + in.Destination + "; addons and settings preserved.")
	} else {
		log("Client ready at " + in.Destination + ". The original remains available for recovery.")
	}
	return nil
}

func retailPlay(ctx context.Context, forever string, in webui.PlayInput, log func(string)) error {
	p, err := configuredRetailPin()
	if err != nil {
		return err
	}
	dir := retailRoot(forever)
	if err := install.CheckBuild(dir, p); err != nil {
		return err
	}
	in.Classic = false
	return uiPlay()(ctx, dir, in, log)
}

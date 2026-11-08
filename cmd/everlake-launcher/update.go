package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/selfupdate"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/settings"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/webui"
)

// updateAsset is this build's file in a release, set by scripts/package.sh (everlake-launcher-windows-amd64.exe,
// Everlake-Launcher-macos.zip, ...). Empty, as in go build and CI builds, the launcher never replaces itself.
var updateAsset = ""

// How often an open launcher looks for a new release, and how soon it tries again to put one in that it found
// while a job or a game was running.
const (
	updateEvery = time.Hour
	updateRetry = time.Minute
)

func updateClient() *selfupdate.Client {
	// EVERLAKE_UPDATE_API points the check at another release list (tests).
	return &selfupdate.Client{API: os.Getenv("EVERLAKE_UPDATE_API"), UserAgent: "everlake-launcher/" + version}
}

// errNoUpdate: nothing to do, for one of the reasons in its text.
var errNoUpdate = errors.New("no update")

// findUpdate looks for a release newer than this launcher that has this build's file. The error wraps
// errNoUpdate when there's none, with the reason.
func findUpdate(ctx context.Context, c *selfupdate.Client) (selfupdate.Release, selfupdate.Target, error) {
	var none selfupdate.Release
	if updateAsset == "" {
		return none, selfupdate.Target{}, fmt.Errorf("%w: this build (%s) doesn't update itself; release builds do",
			errNoUpdate, version)
	}
	exe, err := os.Executable()
	if err != nil {
		return none, selfupdate.Target{}, err
	}
	target, err := selfupdate.TargetFor(exe)
	if err != nil {
		return none, target, err
	}
	// A Mac build's file is the zipped app: a copy of its program outside the app can't take it.
	if strings.HasSuffix(updateAsset, ".zip") != target.App {
		return none, target, fmt.Errorf("%w: %s isn't where a release puts it (%s); download the launcher again",
			selfupdate.ErrCantReplace, exe, updateAsset)
	}
	rel, err := c.Latest(ctx)
	if err != nil {
		return none, target, err
	}
	if !selfupdate.Newer(rel.Tag, version) {
		return rel, target, fmt.Errorf("%w: %s is the newest", errNoUpdate, version)
	}
	if _, ok := rel.Assets[updateAsset]; !ok {
		return rel, target, fmt.Errorf("%w: %s has no %s", errNoUpdate, rel.Tag, updateAsset)
	}
	// A copy that put this release in place before and still isn't it after restarting would fetch it forever.
	if s, _ := settings.Load(); s.Updated != nil && s.Updated.Path == target.Path && s.Updated.Tag == rel.Tag {
		return rel, target, fmt.Errorf("%w: %s was put in place before, but this copy still says it's %s; "+
			"download it from %s", errNoUpdate, rel.Tag, version, rel.Page)
	}
	if err := target.Writable(); err != nil {
		return rel, target, fmt.Errorf("%v; download %s from %s", err, rel.Tag, rel.Page)
	}
	return rel, target, nil
}

// applyUpdate downloads this build's file from rel, checks it against the release's SHA256SUMS.txt and puts it in
// place of target.
func applyUpdate(ctx context.Context, c *selfupdate.Client, rel selfupdate.Release, target selfupdate.Target,
	log func(string)) error {
	log(fmt.Sprintf("launcher: %s is out (this is %s); fetching %s", rel.Tag, version, updateAsset))
	sums, err := c.Sums(ctx, rel)
	if err != nil {
		return err
	}
	want, ok := sums[updateAsset]
	if !ok {
		return fmt.Errorf("%s doesn't list %s, so it can't be checked", selfupdate.SumsName, updateAsset)
	}
	staged, err := c.Download(ctx, rel.Assets[updateAsset], want, target.Dir(), func(got, total int64) {
		if total > 0 {
			log(fmt.Sprintf("launcher: %.1f of %.1f MB", float64(got)/(1<<20), float64(total)/(1<<20)))
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%w: %v; download %s from %s", selfupdate.ErrCantReplace, err, updateAsset, rel.Page)
		}
		return err
	}
	log("launcher: the download matches " + selfupdate.SumsName + "; checking it starts")
	// The new copy has to start and say it's the release before it goes in: an antivirus that takes it away, or a
	// release built with the wrong version, stops here with the old copy still in place.
	check := func(program string) error {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		out, err := exec.CommandContext(cctx, program, "version").Output()
		if err != nil {
			return fmt.Errorf("the new launcher didn't start (%v); this one stays", err)
		}
		if got := strings.TrimSpace(string(out)); got != rel.Tag {
			return fmt.Errorf("the new launcher says it's %q, not %s; this one stays", got, rel.Tag)
		}
		return nil
	}
	if err := target.Replace(staged, check); err != nil {
		if errors.Is(err, selfupdate.ErrCantReplace) {
			return fmt.Errorf("%v; download %s from %s", err, updateAsset, rel.Page)
		}
		return err
	}
	if err := settings.Update(func(s *settings.Settings) {
		s.Updated = &settings.UpdateRecord{Path: target.Path, Tag: rel.Tag}
	}); err != nil {
		log("launcher: couldn't note the update in the settings: " + err.Error())
	}
	log(fmt.Sprintf("launcher: %s is in place at %s", rel.Tag, target.Path))
	return nil
}

// cmdUpdate is `everlake-launcher update`: the same update the window does by itself, from a terminal.
func cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only say whether there's a newer launcher")
	fs.Parse(args)
	c := updateClient()
	rel, target, err := findUpdate(ctx, c)
	if errors.Is(err, errNoUpdate) {
		logf("no launcher update: %s", strings.TrimPrefix(err.Error(), errNoUpdate.Error()+": "))
		return nil
	}
	if err != nil {
		return err
	}
	if *check {
		logf("%s is out (this is %s): %s", rel.Tag, version, rel.Page)
		return nil
	}
	target.Cleanup()
	if err := applyUpdate(ctx, c, rel, target, func(l string) { logf("%s", l) }); err != nil {
		return err
	}
	logf("done: the next start runs %s", rel.Tag)
	return nil
}

// autoUpdate keeps an open launcher up to date: it looks for a new release now and every updateEvery, and puts one
// in as a job on the page, but only while nothing else is running and no game it started is open. Once the new copy
// is in place it calls restart. It returns when ctx ends or the update is in. A reason it can't update itself (its
// folder can't be written, macOS runs it from a temporary copy) goes on the page.
func autoUpdate(ctx context.Context, srv *webui.Server, restart func(selfupdate.Target)) {
	if updateAsset == "" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		if t, err := selfupdate.TargetFor(exe); err == nil {
			t.Cleanup()
		}
	}
	if s, _ := settings.Load(); !s.AutoUpdates() {
		logf("launcher updates are off (auto_update in %s)", settingsPath())
		return
	}
	c := updateClient()
	tries := map[string]int{} // failed attempts per release; after maxTries it waits for the next launch
	const maxTries = 3
	var found *selfupdate.Release
	var target selfupdate.Target
	var foundAt time.Time
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = updateEvery
		if found != nil && time.Since(foundAt) >= updateEvery {
			found = nil // still waiting after an hour: look again, a newer release may be out
		}
		if found == nil {
			rel, t, err := findUpdate(ctx, c)
			switch {
			case errors.Is(err, selfupdate.ErrCantReplace):
				srv.SetNotice("This launcher can't update itself: " + err.Error() + ".")
				continue
			case err != nil:
				if !errors.Is(err, errNoUpdate) && ctx.Err() == nil {
					logf("launcher update check: %v", err)
				}
				continue
			}
			if tries[rel.Tag] >= maxTries {
				continue
			}
			found, target, foundAt = &rel, t, time.Now()
		}
		if srv.Busy() != "" || gamesRunning() > 0 {
			wait = updateRetry // after the job, or once the game closes
			continue
		}
		rel, t := *found, target
		done := make(chan error, 1)
		if !srv.Begin("update", func(log func(string)) error {
			err := applyUpdate(ctx, c, rel, t, log)
			if err == nil {
				log("launcher: restarting into " + rel.Tag)
			}
			done <- err
			return err
		}) {
			wait = updateRetry
			continue
		}
		if err := <-done; err != nil {
			tries[rel.Tag]++
			found = nil
			if errors.Is(err, selfupdate.ErrCantReplace) {
				srv.SetNotice("This launcher can't update itself: " + err.Error() + ".")
				tries[rel.Tag] = maxTries
			}
			continue
		}
		restart(t)
		return
	}
}

func settingsPath() string {
	p, err := settings.Path()
	if err != nil {
		return "launcher.json"
	}
	return p
}

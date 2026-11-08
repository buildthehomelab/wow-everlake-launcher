package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/mirror"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

// cmdMirror runs Everlake's own CDN (docs/cdn-and-peers.md): it serves the CDN layout, keeps a checked copy of what
// it fetches from Blizzard in --store, and with --prefetch fetches the pinned build ahead of time.
func cmdMirror(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mirror", flag.ExitOnError)
	store := fs.String("store", "", "folder the mirror keeps its copy of the CDN in (required)")
	overlay := fs.String("overlay", "", "folder of our own patched builds, served first (cdn-build's output)")
	addr := fs.String("addr", ":8087", "address to serve on")
	upstream := fs.String("upstream", "", "comma-separated Blizzard CDN hosts (default: the version server's, then the pin's)")
	paths := fs.String("paths", "", "comma-separated CDN paths to serve (default: the pin's)")
	jobs := fs.Int("jobs", 16, "requests to Blizzard at once")
	memMB := fs.Int("memory-mb", 1024, "archive range bytes held at once, over all requests")
	prefetch := fs.Bool("prefetch", false, "also fetch the pinned build into the store, in the background")
	prefetchOnly := fs.Bool("prefetch-only", false, "fetch the pinned build into the store, then stop (no serving)")
	tags := fs.String("tags", "OSX,Windows,x86_64,arm64,enUS,US", "prefetch: manifest tags to fetch (all are taken)")
	rate := fs.Float64("rate-mb", 40, "prefetch: about this many MB per second at most (0: no cap)")
	planOnly := fs.Bool("plan-only", false, "prefetch: only read the build and say how much would be fetched")
	quiet := fs.Bool("quiet", false, "don't log every request")
	fs.Parse(args)
	if *store == "" {
		return errors.New("mirror: --store is required")
	}
	p := pin.Default()
	hosts := splitTags(*upstream)
	if len(hosts) == 0 {
		hosts = p.Hosts(ctx, nil)
	}
	served := splitTags(*paths)
	if len(served) == 0 {
		served = []string{p.CDNPath}
	}
	st, err := mirror.OpenStore(*store)
	if err != nil {
		return err
	}
	reqLog := logf
	if *quiet || *prefetchOnly || *planOnly {
		reqLog = nil
	}
	m, err := mirror.New(mirror.Options{Store: st, Overlay: *overlay, Upstream: hosts, Paths: served, Jobs: *jobs, MemoryMB: *memMB,
		Log: logf, Requests: reqLog})
	if err != nil {
		return err
	}
	runPrefetch := func(ctx context.Context) error {
		start := time.Now()
		rep, err := m.Prefetch(ctx, mirror.PrefetchOptions{Pin: p, Tags: splitTags(*tags), RateMB: *rate, PlanOnly: *planOnly})
		if rep != nil {
			logf("prefetch: %d files, %d requests (%d already stored), %.1f GB fetched in %s, %d missing on the CDN, "+
				"%d failed", rep.Files, rep.Requests, rep.Already, float64(rep.Bytes)/(1<<30),
				time.Since(start).Round(time.Second), len(rep.Missing), len(rep.Failed))
			for _, x := range rep.Missing {
				logf("MISSING %s", x)
			}
		}
		if err != nil {
			return fmt.Errorf("prefetch: %w", err)
		}
		if *planOnly {
			return nil
		}
		logf("prefetch done: WoW Forever %s is in the store", p.Version)
		return nil
	}
	if *prefetchOnly || *planOnly {
		return runPrefetch(ctx)
	}
	srv := &http.Server{Addr: *addr, Handler: m, ReadHeaderTimeout: 30 * time.Second}
	logf("mirror on %s: store %s, overlay %q, paths %s, upstream %s", *addr, *store, *overlay,
		strings.Join(served, " "), strings.Join(hosts, " "))
	if *prefetch {
		go func() {
			if err := runPrefetch(ctx); err != nil && ctx.Err() == nil {
				logf("%v (the mirror keeps serving; restart it to try again)", err)
			}
		}()
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

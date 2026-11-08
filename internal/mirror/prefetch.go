package mirror

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// PrefetchOptions say which build and which of its files to fetch ahead of time.
type PrefetchOptions struct {
	Pin  pin.Pin
	Tags []string // manifest tags; several of one type are all taken (OSX,Windows,x86_64,arm64,enUS,US)
	Jobs int      // requests at once (default 8)
	// RateMB caps the download (and so the writes to disk) at about this many MB per second; 0 means no cap.
	RateMB float64
	// PlanOnly stops after reading the indexes and manifests and logging what would be fetched.
	PlanOnly bool
}

// PrefetchReport counts what a prefetch did.
type PrefetchReport struct {
	Indexes           int
	Files             int      // stored files the tags want, plus the build's own
	Requests, Already int      // range or file requests planned, and how many were already in the store
	Bytes             int64    // bytes fetched from Blizzard
	Missing           []string // not on the CDN
	Failed            []string // requests that failed for another reason (run the prefetch again)
}

// Prefetch fetches the pinned build's configs, every archive index of its CDN config, the build's own files, and
// the install and download manifests' files for the tags, into the store. It reads archives in the same ranges a
// launcher does (cdn.SpanGap), but merged over all the tags, so every range a launcher with some of those tags asks
// for is already in the store. It can be stopped and run again: what is stored isn't fetched again.
func (m *Mirror) Prefetch(ctx context.Context, o PrefetchOptions) (*PrefetchReport, error) {
	if o.Jobs < 1 {
		o.Jobs = 8
	}
	p, rep := o.Pin.CDNPath, &PrefetchReport{}
	if m.clients[p] == nil {
		return nil, fmt.Errorf("the mirror doesn't serve the pin's CDN path %s", p)
	}
	buildKey, cdnKey := o.Pin.Keys()
	raw, _, err := m.Config(ctx, p, buildKey)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}
	build := tact.ParseConfig(string(raw))
	if raw, _, err = m.Config(ctx, p, cdnKey); err != nil {
		return nil, fmt.Errorf("CDN config: %w", err)
	}
	cdc := tact.ParseConfig(string(raw))
	m.Log("prefetch %s %s (%s), tags %s", o.Pin.Product, o.Pin.Version, build.One("build-name"),
		strings.Join(o.Tags, " "))

	// Every index, as a launcher reads them all.
	archives, err := cdc.Keys("archives")
	if err != nil {
		return nil, err
	}
	keys := append([]tact.Key{}, archives...)
	if fi := cdc.One("file-index"); fi != "" {
		k, err := tact.ParseKey(fi)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	idx := make([]*tact.Index, len(keys))
	if err := parallel(ctx, o.Jobs, len(keys), func(ctx context.Context, i int) (err error) {
		_, idx[i], _, err = m.Index(ctx, p, keys[i])
		return err
	}); err != nil {
		return nil, fmt.Errorf("archive indexes: %w", err)
	}
	rep.Indexes = len(keys)
	where := map[tact.Key]loc{}
	for i, x := range idx {
		for _, e := range x.Entries {
			if _, ok := where[e.EKey]; !ok && e.Size > 0 {
				l := loc{size: int64(e.Size)}
				if x.HasOffsets {
					l.archive, l.offset = keys[i], int64(e.Offset)
				}
				where[e.EKey] = l
			}
		}
	}
	m.Log("%d indexes, %d stored files on the CDN", len(keys), len(where))

	// The build's own files, then the manifests' files for the tags.
	blob := func(e tact.Key) ([]byte, error) {
		l, ok := where[e]
		if ok && !l.archive.IsZero() {
			data, _, _, ok, err := m.Range(ctx, p, l.archive, l.offset, l.offset+l.size)
			if err == nil && !ok {
				err = errors.New("too large to keep")
			}
			return data, err
		}
		data, _, err := m.Loose(ctx, p, e)
		return data, err
	}
	decode := func(name string, k [2]tact.Key) ([]byte, error) {
		raw, err := blob(k[1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return tact.Decode(raw, k[1], k[0])
	}
	encC, encE, err := build.KeyPair("encoding")
	if err != nil {
		return nil, err
	}
	encData, err := decode("encoding file", [2]tact.Key{encC, encE})
	if err != nil {
		return nil, err
	}
	enc, err := tact.ParseEncoding(encData)
	if err != nil {
		return nil, err
	}
	system, err := build.BuildFiles(enc)
	if err != nil {
		return nil, err
	}
	want := map[tact.Key]bool{}
	for _, k := range system {
		want[k[1]] = true
	}
	instData, err := decode("install manifest", system["install"])
	if err != nil {
		return nil, err
	}
	inst, err := tact.ParseInstall(instData)
	if err != nil {
		return nil, err
	}
	dlData, err := decode("download manifest", system["download"])
	if err != nil {
		return nil, err
	}
	dl, err := tact.ParseDownload(dlData)
	if err != nil {
		return nil, err
	}
	files, err := inst.Select(o.Tags)
	if err != nil {
		return nil, fmt.Errorf("install manifest: %w", err)
	}
	for _, f := range files {
		ent, ok := enc.Lookup(f.CKey)
		if !ok {
			return nil, fmt.Errorf("install manifest file %s (%s) is not in the encoding file", f.Name, f.CKey)
		}
		want[ent.EKeys[0]] = true
	}
	data, err := dl.Select(o.Tags)
	if err != nil {
		return nil, fmt.Errorf("download manifest: %w", err)
	}
	for _, d := range data {
		want[d.EKey] = true
	}
	rep.Files = len(want)

	// Plan the requests: archive files merged into ranges as a launcher would, but with no size limit on a merged
	// range, then cut into requests of about cdn.SpanMax at file starts, so the requests touch end to end.
	byArchive := map[tact.Key][]loc{}
	var loose []tact.Key
	for e := range want {
		l, ok := where[e]
		if ok && !l.archive.IsZero() {
			byArchive[l.archive] = append(byArchive[l.archive], l)
		} else {
			loose = append(loose, e)
		}
	}
	var reqs []request
	for a, ls := range byArchive {
		reqs = append(reqs, planArchive(a, ls, cdn.SpanGap, cdn.SpanMax)...)
	}
	for _, e := range loose {
		reqs = append(reqs, request{key: e, end: where[e].size})
	}
	// Skip what's stored; plan the rest.
	var todo []request
	var total int64
	for _, q := range reqs {
		stored := false
		if q.key.IsZero() {
			stored, _, err = m.Store.Covered(p, q.archive, q.start, q.end)
			if err != nil {
				return nil, err
			}
		} else {
			stored = m.Store.HasWhole(p, "data", q.key, "")
		}
		if stored {
			rep.Already++
			continue
		}
		todo = append(todo, q)
		total += q.end - q.start
	}
	rep.Requests = len(reqs)
	m.Log("%d files: %d requests, %d already stored, %.1f GB to fetch", rep.Files, len(reqs), rep.Already,
		float64(total)/(1<<30))
	if o.PlanOnly {
		return rep, nil
	}

	lim := newLimiter(o.RateMB)
	var done atomic.Int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		began := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				d := done.Load()
				rate := float64(d) / (1 << 20) / time.Since(began).Seconds()
				m.Log("prefetch: %.1f of %.1f GB (%.0f%%), %.1f MB/s", float64(d)/(1<<30), float64(total)/(1<<30),
					100*float64(d)/float64(max(total, 1)), rate)
			}
		}
	}()
	var mu sync.Mutex
	err = parallel(ctx, o.Jobs, len(todo), func(ctx context.Context, i int) error {
		q := todo[i]
		if err := lim.wait(ctx, q.end-q.start); err != nil {
			return err
		}
		var err error
		if q.key.IsZero() {
			var ok bool
			_, _, _, ok, err = m.Range(ctx, p, q.archive, q.start, q.end)
			if err == nil && !ok {
				err = fmt.Errorf("archive %s %d-%d: too large to keep", q.archive, q.start, q.end)
			}
		} else {
			_, _, err = m.Loose(ctx, p, q.key)
		}
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, cdn.ErrNotFound):
			mu.Lock()
			rep.Missing = append(rep.Missing, q.String())
			mu.Unlock()
		case err != nil: // one bad request doesn't stop the rest; the next run tries it again
			m.Log("prefetch: %s: %v", q, err)
			mu.Lock()
			rep.Failed = append(rep.Failed, q.String())
			mu.Unlock()
		default:
			done.Add(q.end - q.start)
		}
		return nil
	})
	rep.Bytes = done.Load()
	sort.Strings(rep.Missing)
	sort.Strings(rep.Failed)
	if err == nil && len(rep.Failed) > 0 {
		err = fmt.Errorf("%d requests failed; run the prefetch again to retry them", len(rep.Failed))
	}
	return rep, err
}

// planArchive merges one archive's wanted files into ranges as a launcher would (reading through gaps up to gap
// bytes), with no size limit on a merged range, and cuts each into requests of about maxSpan at file starts, so the
// requests of one merged range touch end to end.
func planArchive(a tact.Key, ls []loc, gap, maxSpan int64) []request {
	sort.Slice(ls, func(i, j int) bool { return ls[i].offset < ls[j].offset })
	var reqs []request
	var cur *request
	for _, l := range ls {
		start, end := l.offset, l.offset+l.size
		switch {
		case cur != nil && start-cur.end <= gap && end-cur.start <= maxSpan:
			cur.end = max(cur.end, end)
		case cur != nil && start-cur.end <= gap:
			cur.end = max(cur.end, start) // the next request starts where this one ends
			reqs = append(reqs, request{archive: a, start: start, end: end})
			cur = &reqs[len(reqs)-1]
		default:
			reqs = append(reqs, request{archive: a, start: start, end: end})
			cur = &reqs[len(reqs)-1]
		}
	}
	return reqs
}

// loc is a stored file's place on the CDN: an archive range, or loose (archive zero).
type loc struct {
	archive      tact.Key
	offset, size int64
}

// request is one prefetch request: [start, end) of an archive, or a loose file (key set, end = its size).
type request struct {
	archive    tact.Key
	key        tact.Key
	start, end int64
}

func (q request) String() string {
	if q.key.IsZero() {
		return fmt.Sprintf("archive %s %d-%d", q.archive, q.start, q.end)
	}
	return "loose " + q.key.String()
}

// parallel runs fn for 0..n-1 on jobs goroutines and returns the first error (which stops the rest).
func parallel(ctx context.Context, jobs, n int, fn func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var first error
	var once sync.Once
	var wg sync.WaitGroup
	for w := 0; w < min(jobs, n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n || ctx.Err() != nil {
					return
				}
				if err := fn(ctx, i); err != nil {
					once.Do(func() { first = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return first
}

// limiter spreads requests out so bytes go at about rate MB per second.
type limiter struct {
	mu   sync.Mutex
	rate float64 // bytes per second; 0 = no cap
	next time.Time
}

func newLimiter(mb float64) *limiter { return &limiter{rate: mb * (1 << 20)} }

func (l *limiter) wait(ctx context.Context, n int64) error {
	if l.rate <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = l.next.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	l.mu.Unlock()
	select {
	case <-time.After(time.Until(at)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

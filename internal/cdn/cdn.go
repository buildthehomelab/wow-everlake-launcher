// Package cdn fetches files from Blizzard's CDN (or a mirror laid out the same way) and from the version server.
//
// CDN paths are http://<host>/<path>/<kind>/<ab>/<cd>/<key><suffix>, where kind is "config" or "data". Every call
// tries each host in turn, with retries, and returns bytes as received: checking them is the caller's job (the
// tact package checks everything against its hashes).
package cdn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// UserAgent names the launcher in every request.
var UserAgent = "everlake-launcher"

// ErrNotFound is returned when every host answered 404.
var ErrNotFound = errors.New("not on the CDN")

// Client fetches from one CDN path on a list of hosts.
type Client struct {
	Hosts []string // tried in order, e.g. "level3.blizzard.com"
	Path  string   // e.g. "tpr/wow"
	HTTP  *http.Client
	Tries int // attempts per host
}

// New returns a client with sensible timeouts and connection reuse for many parallel requests.
func New(hosts []string, path string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	return &Client{Hosts: hosts, Path: path, Tries: 3, HTTP: &http.Client{Transport: tr, Timeout: 10 * time.Minute}}
}

// URL is the path of a file on a host.
func (c *Client) URL(host, kind string, key tact.Key, suffix string) string {
	s := key.String()
	return fmt.Sprintf("http://%s/%s/%s/%s/%s/%s%s", host, c.Path, kind, s[:2], s[2:4], s, suffix)
}

// Request is one fetch. With Size > 0 it reads Size bytes at Offset (a file inside an archive); otherwise the whole
// file, at most Max bytes. Check, when set, judges the bytes from each host: bytes that fail it count as that host's
// error and the next host is tried, so one host or proxy serving a damaged copy doesn't stop an install.
type Request struct {
	Kind   string // "config" or "data"
	Key    tact.Key
	Suffix string // ".index" for archive indexes
	Offset int64
	Size   int64
	Max    int64 // whole-file cap; 0 means DefaultMax
	Check  func([]byte) error
}

// DefaultMax caps a whole-file download (the largest WoW files are a few hundred MB).
const DefaultMax = 2 << 30

// Get fetches a whole file.
func (c *Client) Get(ctx context.Context, kind string, key tact.Key, suffix string) ([]byte, error) {
	return c.Do(ctx, Request{Kind: kind, Key: key, Suffix: suffix})
}

// GetRange fetches size bytes at offset of a file (used to read one file out of an archive).
func (c *Client) GetRange(ctx context.Context, kind string, key tact.Key, offset int64, size int64) ([]byte, error) {
	return c.Do(ctx, Request{Kind: kind, Key: key, Offset: offset, Size: size})
}

// Do runs a request against each host in turn, with retries, until one answers with bytes that pass the check.
func (c *Client) Do(ctx context.Context, q Request) ([]byte, error) {
	if len(c.Hosts) == 0 {
		return nil, errors.New("no CDN hosts")
	}
	if q.Size < 0 || q.Offset < 0 || (q.Size == 0 && q.Offset != 0) {
		return nil, fmt.Errorf("bad range %d+%d", q.Offset, q.Size)
	}
	if q.Max <= 0 {
		q.Max = DefaultMax
	}
	var last error
	notFound := 0
	for _, host := range c.Hosts {
		url := c.URL(host, q.Kind, q.Key, q.Suffix)
		for try := 0; try < c.Tries; try++ {
			if try > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(try*try) * time.Second):
				}
			}
			data, status, err := c.once(ctx, url, q)
			if err == nil && q.Check != nil {
				if err = q.Check(data); err != nil {
					err = fmt.Errorf("%s: %w", url, err)
				}
			}
			if err == nil {
				return data, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			last = err
			if status == http.StatusNotFound {
				notFound++
				break // no point retrying a 404 on the same host
			}
		}
	}
	if notFound == len(c.Hosts) {
		return nil, fmt.Errorf("%s/%s %s%s: %w", c.Path, q.Kind, q.Key, q.Suffix, ErrNotFound)
	}
	return nil, last
}

func (c *Client) once(ctx context.Context, url string, q Request) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	ranged := q.Size > 0
	if ranged {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(q.Offset, 10)+"-"+strconv.FormatInt(q.Offset+q.Size-1, 10))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case ranged && resp.StatusCode == http.StatusPartialContent:
	case ranged && resp.StatusCode == http.StatusOK:
		// The host ignored the range: skip to it. Rare, but a plain mirror might do this.
		if _, err := io.CopyN(io.Discard, resp.Body, q.Offset); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("%s: %w", url, err)
		}
	case !ranged && resp.StatusCode == http.StatusOK:
	default:
		return nil, resp.StatusCode, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	limit := q.Max + 1
	if ranged {
		limit = q.Size
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s: %w", url, err)
	}
	if ranged && int64(len(data)) != q.Size {
		return nil, resp.StatusCode, fmt.Errorf("%s: asked for %d bytes, got %d", url, q.Size, len(data))
	}
	if !ranged && int64(len(data)) > q.Max {
		return nil, resp.StatusCode, fmt.Errorf("%s: larger than %d bytes", url, q.Max)
	}
	return data, resp.StatusCode, nil
}

// VersionServer is Blizzard's patch server, which says which build of a product is live and which CDN hosts serve it.
const VersionServer = "http://us.patch.battle.net:1119"

// Table fetches one of a product's version tables ("versions" or "cdns") and returns the row for region.
func Table(ctx context.Context, server, product, table, region string) (map[string]string, error) {
	url := fmt.Sprintf("%s/%s/%s", server, product, table)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	// (the version server answers a few KB)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	rows, err := tact.ParseTable(string(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	for _, r := range rows {
		if r["Region"] == region || r["Name"] == region {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%s has no row for region %q", url, region)
}

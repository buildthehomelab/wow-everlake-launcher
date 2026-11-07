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

// Get fetches a whole file.
func (c *Client) Get(ctx context.Context, kind string, key tact.Key, suffix string) ([]byte, error) {
	return c.fetch(ctx, kind, key, suffix, -1, 0)
}

// GetRange fetches size bytes at offset of a file (used to read one file out of an archive).
func (c *Client) GetRange(ctx context.Context, kind string, key tact.Key, offset int64, size int64) ([]byte, error) {
	return c.fetch(ctx, kind, key, "", offset, size)
}

func (c *Client) fetch(ctx context.Context, kind string, key tact.Key, suffix string, offset, size int64) ([]byte, error) {
	if len(c.Hosts) == 0 {
		return nil, errors.New("no CDN hosts")
	}
	var last error
	notFound := 0
	for _, host := range c.Hosts {
		url := c.URL(host, kind, key, suffix)
		for try := 0; try < c.Tries; try++ {
			if try > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(try*try) * time.Second):
				}
			}
			data, status, err := c.once(ctx, url, offset, size)
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
		return nil, fmt.Errorf("%s/%s %s: %w", c.Path, kind, key, ErrNotFound)
	}
	return nil, last
}

func (c *Client) once(ctx context.Context, url string, offset, size int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if offset >= 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(offset+size-1, 10))
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case offset >= 0 && resp.StatusCode == http.StatusPartialContent:
	case offset >= 0 && resp.StatusCode == http.StatusOK:
		// The host ignored the range: skip to it. Rare, but a plain mirror might do this.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("%s: %w", url, err)
		}
	case offset < 0 && resp.StatusCode == http.StatusOK:
	default:
		return nil, resp.StatusCode, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	var body io.Reader = resp.Body
	if offset >= 0 {
		body = io.LimitReader(resp.Body, size)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%s: %w", url, err)
	}
	if offset >= 0 && int64(len(data)) != size {
		return nil, resp.StatusCode, fmt.Errorf("%s: asked for %d bytes, got %d", url, size, len(data))
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
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
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

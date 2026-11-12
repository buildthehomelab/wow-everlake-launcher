package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
)

type classicOverlay struct {
	BaseBuild string   `json:"blizzard_build_key"`
	BaseCDN   string   `json:"blizzard_cdn_key"`
	Version   string   `json:"version"`
	CDNPath   string   `json:"cdn_path"`
	Build     string   `json:"build_key"`
	CDN       string   `json:"cdn_key"`
	Added     []uint32 `json:"added_file_data_ids"`
}

func installSharedClient(ctx context.Context, dir string, verify bool, log func(string)) error {
	if verify {
		if state, err := install.ReadState(dir); err == nil && state.Pin.BuildConfig == pin.Default().BuildConfig {
			return runInstall(ctx, installOptions(ctx, dir, true), log)
		}
	}
	if !verify && gamesRunning() != 0 {
		return fmt.Errorf("close the running game before updating the shared client")
	}

	var data []byte
	var host string
	var err error
	if mirror := strings.TrimSpace(os.Getenv("EVERLAKE_CLIENT_CDN")); mirror != "" {
		host = mirror
		data, err = fetchOverlayManifest(ctx, host)
	} else {
		var closeOverlay func()
		data, host, closeOverlay, err = openLocalOverlay(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if state, stateErr := install.ReadState(dir); stateErr == nil &&
				state.Pin.BuildConfig != pin.Default().BuildConfig {
				return fmt.Errorf("restore the shared client overlay before verifying or repairing this install")
			}
			return runInstall(ctx, installOptions(ctx, dir, verify), log)
		}
		if closeOverlay != nil {
			defer closeOverlay()
		}
	}
	if err != nil {
		return err
	}
	var manifest classicOverlay
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("read Classic overlay manifest: %w", err)
	}
	p := pin.Default()
	if manifest.BaseBuild != p.BuildConfig || manifest.BaseCDN != p.CDNConfig ||
		manifest.Version != p.Version || manifest.CDNPath != p.CDNPath ||
		manifest.Build == p.BuildConfig || manifest.CDN == p.CDNConfig || len(manifest.Added) == 0 {
		return fmt.Errorf("Classic overlay does not describe an add-on build for %s", p.Version)
	}
	p.BuildConfig, p.CDNConfig = manifest.Build, manifest.CDN
	if err := p.Check(); err != nil {
		return fmt.Errorf("Classic overlay pin: %w", err)
	}

	opts := installOptions(ctx, dir, verify)
	opts.Pin = p
	if !verify {
		if state, err := install.ReadState(dir); err == nil {
			opts.NoData, opts.MaxPriority = state.NoData, state.MaxPriority
		}
	}
	opts.CDN = cdn.New(append([]string{host}, opts.GameHosts...), p.CDNPath)
	if verify {
		state, err := install.ReadState(dir)
		if err != nil {
			return err
		}
		if state.Pin.BuildConfig != p.BuildConfig || state.Pin.CDNConfig != p.CDNConfig {
			return fmt.Errorf("the available overlay differs from the installed build")
		}
	}
	log(fmt.Sprintf("Shared client %s overlay build %s (%d added FileDataIDs)", p.Version, p.BuildConfig, len(manifest.Added)))
	return runInstall(ctx, opts, log)
}

func openLocalOverlay(dir string) ([]byte, string, func(), error) {
	overlay := filepath.Join(dir, "_era_", "overlay")
	root, err := os.OpenRoot(overlay)
	if os.IsNotExist(err) {
		if exe, exeErr := os.Executable(); exeErr == nil {
			overlay = filepath.Join(filepath.Dir(exe), "Classic-overlay")
			root, err = os.OpenRoot(overlay)
		}
	}
	if err != nil {
		return nil, "", nil, fmt.Errorf("open the Classic overlay in %s: %w", overlay, err)
	}
	data, err := fs.ReadFile(root.FS(), "build.json")
	if err != nil {
		root.Close()
		return nil, "", nil, fmt.Errorf("read the Classic overlay in %s: %v", overlay, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		root.Close()
		return nil, "", nil, err
	}
	server := &http.Server{Handler: http.FileServerFS(root.FS())}
	go server.Serve(listener)
	return data, listener.Addr().String(), func() {
		server.Close()
		root.Close()
	}, nil
}

func fetchOverlayManifest(ctx context.Context, origin string) ([]byte, error) {
	endpoint, err := url.Parse(origin)
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") ||
		endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" ||
		endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, fmt.Errorf("EVERLAKE_CLIENT_CDN must be an HTTP or HTTPS origin")
	}
	endpoint.Path = "/everlake/build.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch shared client manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch shared client manifest: HTTP %d", resp.StatusCode)
	}
	const limit = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read shared client manifest: %w", err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("shared client manifest exceeds %d bytes", limit)
	}
	return data, nil
}

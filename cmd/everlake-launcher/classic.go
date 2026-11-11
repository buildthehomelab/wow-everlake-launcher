package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"

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

func prepareClassic(ctx context.Context, dir string, log func(string)) (string, error) {
	overlay := filepath.Join(dir, "_era_", "overlay")
	root, err := os.OpenRoot(overlay)
	if os.IsNotExist(err) {
		if exe, exeErr := os.Executable(); exeErr == nil {
			overlay = filepath.Join(filepath.Dir(exe), "Classic-overlay")
			root, err = os.OpenRoot(overlay)
		}
	}
	if err != nil {
		return "", fmt.Errorf("open the Classic overlay in %s: %w", overlay, err)
	}
	defer root.Close()
	data, err := fs.ReadFile(root.FS(), "build.json")
	if err != nil {
		return "", fmt.Errorf("open the Classic overlay in %s: %w", overlay, err)
	}
	var manifest classicOverlay
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("read Classic overlay manifest: %w", err)
	}
	p := pin.Default()
	if manifest.BaseBuild != p.BuildConfig || manifest.BaseCDN != p.CDNConfig ||
		manifest.Version != p.Version || manifest.CDNPath != p.CDNPath ||
		manifest.Build == p.BuildConfig || manifest.CDN == p.CDNConfig || len(manifest.Added) == 0 {
		return "", fmt.Errorf("Classic overlay does not describe an add-on build for %s", p.Version)
	}
	p.BuildConfig, p.CDNConfig = manifest.Build, manifest.CDN
	if err := p.Check(); err != nil {
		return "", fmt.Errorf("Classic overlay pin: %w", err)
	}

	classic := filepath.Join(dir, "Classic")
	if err := os.Mkdir(classic, 0755); err != nil && !os.IsExist(err) {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(classic)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if resolved != filepath.Join(parent, "Classic") {
		return "", fmt.Errorf("the Classic install folder is redirected: %s", classic)
	}
	log(fmt.Sprintf("Classic %s overlay build %s (%d added FileDataIDs)", p.Version, p.BuildConfig, len(manifest.Added)))
	if state, err := install.ReadState(classic); err == nil &&
		state.Pin.BuildConfig == p.BuildConfig && state.Pin.CDNConfig == p.CDNConfig {
		if report, err := install.QuickVerify(classic); err == nil && !report.Failed() {
			return classic, nil
		}
	}

	if gamesRunning() != 0 {
		return "", fmt.Errorf("close the running game before preparing Classic")
	}

	// The full Classic install preloads the custom files, so the local CDN is needed only during installation.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	server := &http.Server{Handler: http.FileServerFS(root.FS())}
	go server.Serve(listener)
	defer server.Close()

	hosts := p.Hosts(ctx, nil)
	opts := install.Options{
		Dir: classic, Pin: p, Tags: splitTags(defaultTags()), Jobs: 8, GameHosts: hosts,
		CDN: cdn.New(append([]string{listener.Addr().String()}, hosts...), p.CDNPath),
	}
	log("Preparing the separate Classic install in " + classic)
	if err := runInstall(ctx, opts, log); err != nil {
		return "", fmt.Errorf("prepare Classic install: %w", err)
	}
	return classic, nil
}

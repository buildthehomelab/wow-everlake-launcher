// Package pin holds the one client build this launcher installs.
//
// The pin is a file in the repo (wowf.json), compiled into the binary, so a launcher release always installs
// exactly the build the realm's server core supports. Moving to a new Blizzard build means a reviewed change to
// that file (see `everlake-launcher pin`), released together with the matching server core.
package pin

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/cdn"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

//go:embed wowf.json
var wowfJSON []byte

// Pin names one build of one product on the CDN.
type Pin struct {
	Product     string   `json:"product"`      // TACT product code, "wowf" for WoW Forever
	Region      string   `json:"region"`       // version server region the hosts are read for
	Version     string   `json:"version"`      // e.g. 1.60.1.70790
	BuildID     int      `json:"build_id"`     // e.g. 70790
	BuildConfig string   `json:"build_config"` // build config key
	CDNConfig   string   `json:"cdn_config"`   // CDN config key
	CDNPath     string   `json:"cdn_path"`     // e.g. tpr/wow
	CDNHosts    []string `json:"cdn_hosts"`    // fallback hosts when the version server can't be asked
	GameFolder  string   `json:"game_folder"`  // where the game's loose files go, e.g. _forever_
	Note        string   `json:"note,omitempty"`
}

// Default is the pin compiled into this launcher.
func Default() Pin {
	p, err := Parse(wowfJSON)
	if err != nil {
		panic("embedded pin: " + err.Error())
	}
	return p
}

// Parse reads and checks a pin file.
func Parse(data []byte) (Pin, error) {
	var p Pin
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	return p, p.Check()
}

// Check reports the first missing or malformed field.
func (p Pin) Check() error {
	if p.Product == "" || p.Region == "" || p.CDNPath == "" || p.GameFolder == "" {
		return fmt.Errorf("pin needs product, region, cdn_path and game_folder")
	}
	if _, err := tact.ParseKey(p.BuildConfig); err != nil {
		return fmt.Errorf("pin build_config: %w", err)
	}
	if _, err := tact.ParseKey(p.CDNConfig); err != nil {
		return fmt.Errorf("pin cdn_config: %w", err)
	}
	if strings.ContainsAny(p.GameFolder, `/\`) || p.GameFolder == "." || p.GameFolder == ".." {
		return fmt.Errorf("pin game_folder %q must be a plain folder name", p.GameFolder)
	}
	if !strings.HasSuffix(p.Version, "."+strconv.Itoa(p.BuildID)) {
		return fmt.Errorf("pin version %q doesn't end in build_id %d", p.Version, p.BuildID)
	}
	return nil
}

// Keys returns the parsed build and CDN config keys.
func (p Pin) Keys() (build, cdnConfig tact.Key) {
	build, _ = tact.ParseKey(p.BuildConfig)
	cdnConfig, _ = tact.ParseKey(p.CDNConfig)
	return build, cdnConfig
}

// Hosts asks the version server which CDN hosts serve the pin's region now, and falls back to the pin's own list.
// Extra hosts (our mirror, later) go first when given.
func (p Pin) Hosts(ctx context.Context, extra []string) []string {
	hosts := append([]string{}, extra...)
	if row, err := cdn.Table(ctx, cdn.VersionServer, p.Product, "cdns", p.Region); err == nil && row["Path"] == p.CDNPath {
		hosts = append(hosts, strings.Fields(row["Hosts"])...)
	}
	for _, h := range p.CDNHosts {
		hosts = appendNew(hosts, h)
	}
	return hosts
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// Live reads the build the version server offers for a product now, as a pin. It is how a new pin is proposed;
// the launcher itself never follows the live build.
func Live(ctx context.Context, product, region, gameFolder string) (Pin, error) {
	v, err := cdn.Table(ctx, cdn.VersionServer, product, "versions", region)
	if err != nil {
		return Pin{}, err
	}
	c, err := cdn.Table(ctx, cdn.VersionServer, product, "cdns", region)
	if err != nil {
		return Pin{}, err
	}
	id, _ := strconv.Atoi(v["BuildId"])
	p := Pin{
		Product: product, Region: region, Version: v["VersionsName"], BuildID: id,
		BuildConfig: v["BuildConfig"], CDNConfig: v["CDNConfig"], CDNPath: c["Path"],
		CDNHosts: strings.Fields(c["Hosts"]), GameFolder: gameFolder,
	}
	return p, p.Check()
}

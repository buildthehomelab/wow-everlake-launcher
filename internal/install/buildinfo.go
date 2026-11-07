package install

import (
	"fmt"
	"strings"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/pin"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/tact"
)

// buildInfoHeader names .build.info's columns with their types, as Battle.net writes them. The column order and the
// row shape below are copied from the .build.info of a Battle.net install of WoW Forever 1.60.1.70790:
//
//	us|1|<build key>|<CDN key>|||tpr/wow|<hosts>|<servers>|<tags>|||1.60.1.70790||wowf
const buildInfoHeader = "Branch!STRING:0|Active!DEC:1|Build Key!HEX:16|CDN Key!HEX:16|Install Key!HEX:16|" +
	"IM Size!DEC:4|CDN Path!STRING:0|CDN Hosts!STRING:0|CDN Servers!STRING:0|Tags!STRING:0|Armadillo!STRING:0|" +
	"Last Activated!STRING:0|Version!STRING:0|KeyRing!HEX:16|Product!STRING:0"

// buildInfo is the .build.info the game reads next to its Data folder: which build it is, and which CDN to stream
// files it doesn't have from. The game checks its own version against this row.
func buildInfo(p pin.Pin, hosts []string, tags string) []byte {
	var servers []string
	for _, h := range hosts {
		servers = append(servers, fmt.Sprintf("http://%s/?maxhosts=4", h))
	}
	row := []string{p.Region, "1", p.BuildConfig, p.CDNConfig, "", "", p.CDNPath, strings.Join(hosts, " "),
		strings.Join(servers, " "), tags, "", "", p.Version, "", p.Product}
	return []byte(buildInfoHeader + "\n" + strings.Join(row, "|") + "\n")
}

// flavorInfo is <game folder>/.flavor.info, which Battle.net writes next to the game: the game picks its tables
// (and its schema, e.g. WOWSTATIC_1_60_1_70399 for Forever) by this flavor.
func flavorInfo(p pin.Pin) []byte { return []byte("Product Flavor!STRING:0\n" + p.Flavor + "\n") }

// buildInfoTags writes the install's tags the way .build.info lists them: platform, architecture and region
// (marked with ?), then the locale once for speech and once for text, e.g.
// "Windows x86_64 US? enUS speech?:Windows x86_64 US? enUS text?". known says which type each tag name is.
func buildInfoTags(tags []string, known []tact.Tag) string {
	types := map[string]uint16{}
	for _, t := range known {
		types[t.Name] = t.Type
	}
	var base, locales []string
	var region string
	for _, t := range tags {
		switch types[t] {
		case tact.TagRegion:
			region = t + "?"
		case tact.TagLocale:
			locales = append(locales, t)
		case tact.TagCategory:
			// speech and text are added below
		default:
			base = append(base, t)
		}
	}
	if region != "" {
		base = append(base, region)
	}
	var parts []string
	for _, cat := range []string{"speech?", "text?"} {
		parts = append(parts, strings.Join(append(append(append([]string{}, base...), locales...), cat), " "))
	}
	return strings.Join(parts, ":")
}

package pin

import (
	"strings"
	"testing"
)

func TestDefaultPin(t *testing.T) {
	p := Default()
	if p.Product != "wowf" || p.GameFolder != "_forever_" || p.Flavor != "wowf" || p.BuildID != 70938 {
		t.Errorf("default pin = %+v", p)
	}
}

// An install on a build this launcher pinned earlier is a plain Forever client; anything else is an overlay build.
func TestBlizzardBuilds(t *testing.T) {
	if !Blizzard(Default().BuildConfig) {
		t.Error("the pinned build isn't taken for Blizzard's")
	}
	if !Blizzard("9006ba6948388dd0d4a0578e64b8a92e") {
		t.Error("1.60.1.70790, the pin before this one, isn't taken for Blizzard's")
	}
	if Blizzard("11111111111111111111111111111111") {
		t.Error("an unknown build config is taken for Blizzard's")
	}
	if v, ok := earlier[Default().BuildConfig]; ok {
		t.Errorf("the pinned build is also listed as an earlier one (%s)", v)
	}
	for key := range earlier {
		p := Default()
		p.BuildConfig = key
		if p.Check() != nil {
			t.Errorf("earlier build config %q is malformed", key)
		}
	}
}

func TestPinCheck(t *testing.T) {
	good := Default()
	for name, change := range map[string]func(*Pin){
		"short build key":   func(p *Pin) { p.BuildConfig = "9006ba69" },
		"bad CDN key":       func(p *Pin) { p.CDNConfig = strings.Repeat("z", 32) },
		"folder with slash": func(p *Pin) { p.GameFolder = "../x" },
		"folder dot-dot":    func(p *Pin) { p.GameFolder = ".." },
		"no flavor":         func(p *Pin) { p.Flavor = "" },
		"version mismatch":  func(p *Pin) { p.BuildID = 70862 },
		"no product":        func(p *Pin) { p.Product = "" },
	} {
		p := good
		change(&p)
		if p.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse([]byte(`{"product": "wowf"}`)); err == nil {
		t.Error("incomplete pin file accepted")
	}
}

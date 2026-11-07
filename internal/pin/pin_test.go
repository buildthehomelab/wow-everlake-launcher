package pin

import (
	"strings"
	"testing"
)

func TestDefaultPin(t *testing.T) {
	p := Default()
	if p.Product != "wowf" || p.GameFolder != "_forever_" || p.BuildID != 70790 {
		t.Errorf("default pin = %+v", p)
	}
}

func TestPinCheck(t *testing.T) {
	good := Default()
	for name, change := range map[string]func(*Pin){
		"short build key":   func(p *Pin) { p.BuildConfig = "9006ba69" },
		"bad CDN key":       func(p *Pin) { p.CDNConfig = strings.Repeat("z", 32) },
		"folder with slash": func(p *Pin) { p.GameFolder = "../x" },
		"folder dot-dot":    func(p *Pin) { p.GameFolder = ".." },
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

package appwindow

import "testing"

func TestParseHex(t *testing.T) {
	for in, want := range map[string]uint32{"#0e1024": 0x0e1024, " #43303A ": 0x43303a, "#abc": 0xaabbcc, "0b1526": 0x0b1526} {
		got, err := parseHex(in)
		if err != nil || got != want {
			t.Errorf("parseHex(%q) = %06x, %v; want %06x", in, got, err, want)
		}
	}
	for _, in := range []string{"", "#12345", "#gggggg", "rgb(1,2,3)", "#1234567"} {
		if _, err := parseHex(in); err == nil {
			t.Errorf("parseHex(%q) took it", in)
		}
	}
}

func TestColorref(t *testing.T) {
	if got := colorref(0x112233); got != 0x332211 {
		t.Errorf("colorref = %06x, want 332211", got)
	}
}

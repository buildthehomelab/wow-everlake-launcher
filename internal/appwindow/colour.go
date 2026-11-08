package appwindow

import (
	"fmt"
	"strconv"
	"strings"
)

// colorref turns 0xRRGGBB into Windows' 0x00BBGGRR.
func colorref(rgb uint32) uint32 {
	return (rgb&0xff)<<16 | rgb&0xff00 | rgb>>16&0xff
}

// parseHex reads a CSS colour as the page sends it: "#rrggbb" or "#rgb", with or without spaces around it.
func parseHex(s string) (uint32, error) {
	t := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(t) == 3 {
		t = string([]byte{t[0], t[0], t[1], t[1], t[2], t[2]})
	}
	if len(t) != 6 {
		return 0, fmt.Errorf("not a #rrggbb colour: %q", s)
	}
	v, err := strconv.ParseUint(t, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("not a #rrggbb colour: %q", s)
	}
	return uint32(v), nil
}

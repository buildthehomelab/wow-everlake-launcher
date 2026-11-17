package main

import (
	"strings"
	"testing"
	"time"
)

func TestEarlyExit(t *testing.T) {
	for _, c := range []struct {
		code int
		ran  time.Duration
		want string // a part of the message; "" for none
	}{
		{0xC0000135, 2 * time.Second, "exit code 0xC0000135): Windows couldn't find a DLL"},
		{0xC000007B, time.Second, "built for another kind of Windows"},
		{0xC0000142, time.Second, "failed to start"},
		{0xC0000022, time.Second, "antivirus"},
		{1, 3 * time.Second, "closed 3s after starting (exit code 1)"},
		{-1, time.Second, "closed 1s after starting"},
		{0xC0000135, 2 * time.Minute, ""}, // it ran: a normal quit or a crash in play, not a failed start
	} {
		got := earlyExit(c.code, c.ran)
		if c.want == "" {
			if got != "" {
				t.Errorf("earlyExit(%#x, %s) = %q, want nothing", c.code, c.ran, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("earlyExit(%#x, %s) = %q, want it to contain %q", c.code, c.ran, got, c.want)
		}
	}
}

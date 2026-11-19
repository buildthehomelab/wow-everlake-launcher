package gamepatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrUnsupported reports platforms without a supported game launch helper.
var ErrUnsupported = errors.New("joining a realm from the launcher requires Windows or macOS")

// Options say how to start the game and what to patch.
type Options struct {
	Exe  string   // the game executable
	Args []string // its command line
	Dir  string   // its working folder
	Env  []string // its environment; nil inherits the launcher's

	ServerKey   string // the realm's public key, 64 hex characters (StockServerKey for an unchanged core)
	RegionGroup int    // the key entry the realm signs for (DefaultRegionGroup)
	AnyPortal   bool   // switch off the client's Blizzard-domains-only check on the login name

	// PortalWait is how long to keep looking for the login-name check while the game's protection unpacks its image
	// (default 2 minutes).
	PortalWait time.Duration
	Log        func(string)
}

// Game is a started game with its patches running.
type Game struct {
	Pid int
	// Done is closed when the game process has exited.
	Done <-chan struct{}
	// ExitCode is the game's exit code, set before Done is closed (-1 if it isn't known).
	ExitCode int
}

func (o *Options) defaults() {
	if o.ServerKey == "" {
		o.ServerKey = StockServerKey
	}
	if o.RegionGroup == 0 {
		o.RegionGroup = DefaultRegionGroup
	}
	if o.PortalWait == 0 {
		o.PortalWait = 2 * time.Minute
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

// KeyLogName is where the server key lines also go, under the install folder (the game folder's parent), so they
// outlive the launcher window and come with Save diagnostics.
var KeyLogName = filepath.Join(".everlake", "server-key.log")

// keyLog sends a line to o.Log and appends it, with the time, to KeyLogName.
func keyLog(o Options) func(string) {
	path := filepath.Join(filepath.Dir(o.Dir), KeyLogName)
	return func(line string) {
		if o.Log != nil {
			o.Log(line)
		}
		if o.Dir == "" {
			return
		}
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), line)
			f.Close()
		}
	}
}

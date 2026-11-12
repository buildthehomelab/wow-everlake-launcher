package gamepatch

import (
	"errors"
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

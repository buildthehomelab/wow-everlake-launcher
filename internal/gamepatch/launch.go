package gamepatch

import (
	"errors"
	"time"
)

// ErrUnsupported: the in-memory patches are written for Windows only. On macOS the game is hardened and is patched
// from inside instead (wow-project-everlake's client/macos/everlake-client.sh).
var ErrUnsupported = errors.New("joining a realm from the launcher works on Windows only for now; on macOS use " +
	"wow-project-everlake's client/macos/everlake-client.sh")

// Options say how to start the game and what to patch.
type Options struct {
	Exe  string   // the game executable
	Args []string // its command line
	Dir  string   // its working folder
	Env  []string // its environment; nil inherits the launcher's

	ServerKey   string // the realm's public key, 64 hex characters (StockServerKey for an unchanged core)
	RegionGroup int    // the key entry the realm signs for (DefaultRegionGroup)
	AnyPortal   bool   // switch off the client's Blizzard-domains-only check on the login name

	// UnpackWait is how long to wait for the game's protection to unpack its image before patching the login name
	// (default 15 seconds). The patch goes in as soon as the image is unpacked; after the wait it goes in anyway.
	UnpackWait time.Duration
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
	if o.UnpackWait == 0 {
		o.UnpackWait = 15 * time.Second
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

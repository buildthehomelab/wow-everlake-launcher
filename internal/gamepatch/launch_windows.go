//go:build windows

package gamepatch

import (
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Launch starts the game (normally: it is never paused), switches off the login-name check through its .data table
// as soon as that's in memory, keeps the server key in the game's key store until the game exits, and puts the
// login-name check back while the player is logged in. It opens the game with the rights Karrz's launcher uses and
// writes only where the game's memory is writable already (see Process.Write).
func Launch(o Options) (*Game, error) {
	o.defaults()
	kp, err := NewKeyPatcher(o.ServerKey, o.RegionGroup)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(o.Exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", o.Exe, err)
	}
	fail := func(err error) (*Game, error) {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	proc, err := OpenProcess(cmd.Process.Pid)
	if err != nil {
		return fail(err)
	}
	base, err := proc.ImageBase()
	if err != nil {
		proc.Close()
		return fail(err)
	}
	size, err := proc.ImageSize(base)
	if err != nil {
		proc.Close()
		return fail(err)
	}
	// The handle stays open until every patch goroutine is done with it.
	var users sync.WaitGroup
	pc := &portalControl{p: proc, log: o.Log}
	if o.AnyPortal {
		users.Add(1)
		go func() {
			defer users.Done()
			pc.find(base, size, o.PortalWait)
		}()
	}

	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	go func() {
		kp.Watch(proc, o.Log, pc.loggedIn, pc.loggedOut)
		<-done
		users.Wait()
		proc.Close()
	}()
	return &Game{Pid: cmd.Process.Pid, Done: done}, nil
}

// portalControl keeps the login-name check switched off only while it's needed: from start-up until the player is
// logged in, and again whenever they're back at the login screen (see portal.go).
type portalControl struct {
	p   *Process
	log func(string)

	mu  sync.Mutex
	pp  PortalPatch // the table entries, once found
	off bool        // the check is switched off right now
	in  bool        // the player is logged in (the key store is up)
}

func (c *portalControl) set(off bool) {
	if off {
		n := c.pp.Apply(c.p)
		c.off = n > 0
		c.log(fmt.Sprintf("login name: any name allowed (%d of %d table entries)", n, len(c.pp.Entries)))
		return
	}
	n := c.pp.Restore(c.p)
	c.off = n < len(c.pp.Entries)
	c.log(fmt.Sprintf("login name: check put back now you're logged in (%d of %d table entries); it comes off "+
		"again at the login screen", n, len(c.pp.Entries)))
}

// loggedIn: the key store is up, so the login went through and the check can go back.
func (c *portalControl) loggedIn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = true
	if c.off {
		c.set(false)
	}
}

// loggedOut: the game freed its key store; the next login needs the check off again.
func (c *portalControl) loggedOut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = false
	if c.pp.Found() && !c.off {
		c.set(true)
	}
}

// find reads the game's image every 100 ms, with the game running, until the login-name table is in memory (the
// protection unpacks the image first), and switches the check off.
func (c *portalControl) find(base, size uint64, wait time.Duration) {
	for end := time.Now().Add(wait); time.Now().Before(end) && c.p.Alive(); time.Sleep(100 * time.Millisecond) {
		pp := FindPortal(c.p.ReadImage(base, size), base)
		if !pp.Found() {
			continue
		}
		c.mu.Lock()
		c.pp = pp
		if !c.in {
			c.set(true)
		}
		c.mu.Unlock()
		return
	}
	if c.p.Alive() {
		c.log("login name: never found the Blizzard-domains check, so not switched off; logging in at a name of " +
			"our own will fail")
	}
}

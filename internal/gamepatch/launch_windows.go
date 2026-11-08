//go:build windows

package gamepatch

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const createSuspended = 0x00000004

// Launch starts the game suspended and, once its image is unpacked (Arctium's order: resume, wait for the image to
// be remapped as one block, suspend, patch, resume), switches off the login-name check. It keeps the server key in the
// game's key store until the game exits, and puts the login-name check back while the player is logged in.
func Launch(o Options) (*Game, error) {
	o.defaults()
	kp, err := NewKeyPatcher(o.ServerKey, o.RegionGroup)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(o.Exe, o.Args...)
	cmd.Dir = o.Dir
	cmd.Env = o.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended}
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
	if err := proc.Resume(); err != nil {
		proc.Close()
		return fail(fmt.Errorf("resume the game: %w", err))
	}
	// The handle stays open until every patch goroutine is done with it.
	var users sync.WaitGroup
	pc := &portalControl{p: proc, log: o.Log}
	if o.AnyPortal {
		pc.find(base, size, o, &users)
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

	mu      sync.Mutex
	pp      PortalPatch // where the check is, once found
	patched bool        // the check is switched off right now
	in      bool        // the player is logged in (the key store is up)
}

// set writes the patch (on) or the original bytes (off), with the game paused for the moment of the write.
func (c *portalControl) set(on bool) {
	if err := c.p.Suspend(); err != nil {
		if c.p.Alive() {
			c.log("login name: pause the game: " + err.Error())
		}
		return
	}
	defer c.p.Resume()
	if on {
		s, l := c.pp.Apply(c.p)
		c.patched = s > 0
		c.log(fmt.Sprintf("login name: any name allowed (%d of %d endings blanked, %d table lengths zeroed)", s,
			len(c.pp.Suffixes), l))
		return
	}
	s, l := c.pp.Restore(c.p)
	c.patched = s < len(c.pp.Suffixes)
	c.log(fmt.Sprintf("login name: check put back now you're logged in (%d of %d endings, %d table lengths); it "+
		"comes off again at the login screen", s, len(c.pp.Suffixes), l))
}

// loggedIn: the key store is up, so the login went through and the check can go back.
func (c *portalControl) loggedIn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = true
	if c.patched {
		c.set(false)
	}
}

// loggedOut: the game freed its key store; the next login needs the check off again.
func (c *portalControl) loggedOut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = false
	if c.pp.Found() && !c.patched {
		c.set(true)
	}
}

// find waits for the image to unpack and patches the check. The first look is made with the game paused; if the
// check isn't in memory yet, later looks read with the game running and pause it only to write, for up to a minute.
func (c *portalControl) find(base, size uint64, o Options, users *sync.WaitGroup) {
	deadline := time.Now().Add(o.UnpackWait)
	for c.p.firstRegionSize(base) <= 0x1000 && time.Now().Before(deadline) && c.p.Alive() {
		time.Sleep(2 * time.Millisecond)
	}
	if c.p.firstRegionSize(base) <= 0x1000 {
		c.log("login name: the game's image wasn't remapped in time; patching anyway")
	}
	look := func(img []byte) bool {
		pp := FindPortal(img, base)
		if !pp.Found() {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.pp = pp
		if !c.in {
			c.set(true)
		}
		return true
	}
	var img []byte
	if err := c.p.Suspend(); err == nil {
		img = c.p.ReadImage(base, size)
		c.p.Resume()
	}
	if look(img) {
		return
	}
	c.log("login name: the Blizzard-domains check isn't in memory yet; still looking")
	users.Add(1)
	go func() {
		defer users.Done()
		for end := time.Now().Add(time.Minute); time.Now().Before(end) && c.p.Alive(); {
			time.Sleep(500 * time.Millisecond)
			if look(c.p.ReadImage(base, size)) {
				return
			}
		}
		if c.p.Alive() {
			c.log("login name: never found the Blizzard-domains check, so not patched; logging in at a name of " +
				"our own will fail")
		}
	}()
}

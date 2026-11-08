//go:build windows

package gamepatch

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const createSuspended = 0x00000004

// Launch starts the game suspended, patches the login-name check once its image is unpacked (Arctium's order:
// resume, wait for the image to be remapped as one block, suspend, patch, resume), then keeps the server key in its
// key store until it exits.
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
	if o.AnyPortal {
		patchPortal(proc, base, size, o, &users)
	}

	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	go func() {
		kp.Watch(proc, o.Log, nil)
		<-done
		users.Wait()
		proc.Close()
	}()
	return &Game{Pid: cmd.Process.Pid, Done: done}, nil
}

// patchPortal waits for the image to unpack, then patches the login-name check with the game suspended. If the
// ending isn't there yet, it keeps looking for a minute in the background. Once patched, it keeps an eye on the
// patched bytes for a few minutes and patches again if the game's start-up puts the originals back.
func patchPortal(p *Process, base, size uint64, o Options, users *sync.WaitGroup) {
	deadline := time.Now().Add(o.UnpackWait)
	for p.firstRegionSize(base) <= 0x1000 && time.Now().Before(deadline) && p.Alive() {
		time.Sleep(2 * time.Millisecond)
	}
	if p.firstRegionSize(base) <= 0x1000 {
		o.Log("login name: the game's image wasn't remapped in time; patching anyway")
	}
	try := func() (PortalPatch, bool) {
		if err := p.Suspend(); err != nil {
			o.Log("login name: suspend the game: " + err.Error())
		} else {
			defer p.Resume()
		}
		pp := FindPortal(p.ReadImage(base, size), base)
		if !pp.Found() {
			return pp, false
		}
		s, l := pp.Apply(p)
		o.Log(fmt.Sprintf("login name: any name allowed (%d of %d endings blanked, %d table lengths zeroed)", s,
			len(pp.Suffixes), l))
		return pp, s > 0
	}
	users.Add(1)
	if pp, ok := try(); ok {
		go func() {
			defer users.Done()
			guardPortal(p, pp, o)
		}()
		return
	}
	o.Log("login name: the Blizzard-domains check isn't in memory yet; still looking")
	go func() {
		defer users.Done()
		for end := time.Now().Add(time.Minute); time.Now().Before(end) && p.Alive(); {
			time.Sleep(500 * time.Millisecond)
			if pp, ok := try(); ok {
				guardPortal(p, pp, o)
				return
			}
		}
		if p.Alive() {
			o.Log("login name: never found the Blizzard-domains check, so not patched; logging in at a name of " +
				"our own will fail")
		}
	}()
}

// guardPortal re-applies the patch if the original ending shows up again at a patched address.
func guardPortal(p *Process, pp PortalPatch, o Options) {
	suf := portalSuffix()
	for end := time.Now().Add(5 * time.Minute); time.Now().Before(end) && p.Alive(); time.Sleep(time.Second) {
		for _, a := range pp.Suffixes {
			if bytes.Equal(p.Read(a, len(suf)), suf) {
				pp.Apply(p)
				o.Log("login name: the game restored the check; patched it again")
				break
			}
		}
	}
}

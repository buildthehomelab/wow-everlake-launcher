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

// Launch starts the game suspended and, once its image is unpacked (Arctium's order: resume, wait for the image to
// be remapped as one block, suspend, patch, resume), switches off the login-name check and puts the server's key in
// the game's built-in keys. It then reports whether the key reached the game's key store, until the game exits.
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
	patchImage(proc, base, size, kp, o, &users)

	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	go func() {
		kp.Report(proc, o.Log)
		<-done
		users.Wait()
		proc.Close()
	}()
	return &Game{Pid: cmd.Process.Pid, Done: done}, nil
}

// imagePatch is what patchImage has put in so far.
type imagePatch struct {
	portal     PortalPatch // the login-name check, once found and blanked
	portalDone bool
	keys       []uint64 // where the built-in key was written
	keyDone    bool
}

// patchImage waits for the image to unpack, then, with the game suspended, blanks the login-name check (if asked)
// and writes the server's key over the built-in one. Whatever isn't there yet it keeps looking for, in the
// background, for a minute. Once in, it keeps an eye on the patched bytes for a few minutes and patches again if the
// game's start-up puts the originals back.
func patchImage(p *Process, base, size uint64, kp *KeyPatcher, o Options, users *sync.WaitGroup) {
	deadline := time.Now().Add(o.UnpackWait)
	for p.firstRegionSize(base) <= 0x1000 && time.Now().Before(deadline) && p.Alive() {
		time.Sleep(2 * time.Millisecond)
	}
	if p.firstRegionSize(base) <= 0x1000 {
		o.Log("game image: it wasn't remapped in time; patching anyway")
	}
	ip := imagePatch{portalDone: !o.AnyPortal}
	try := func() {
		if err := p.Suspend(); err != nil {
			o.Log("game image: suspend the game: " + err.Error())
		} else {
			defer p.Resume()
		}
		img := p.ReadImage(base, size)
		if !ip.portalDone {
			if pp := FindPortal(img, base); pp.Found() {
				s, l := pp.Apply(p)
				o.Log(fmt.Sprintf("login name: any name allowed (%d of %d endings blanked, %d table lengths zeroed)", s,
					len(pp.Suffixes), l))
				ip.portal, ip.portalDone = pp, s > 0
			}
		}
		if !ip.keyDone {
			orig, ours := kp.FindBuiltIn(img, base)
			switch {
			case len(orig) > 0:
				n := kp.PatchBuiltIn(p, orig)
				o.Log(fmt.Sprintf("server key: written over the game's built-in key for region group %d (%d of %d "+
					"places)", kp.group, n, len(orig)))
				ip.keys, ip.keyDone = orig, n == len(orig)
			case ours > 0:
				o.Log("server key: the game's built-in key is ours already")
				ip.keyDone = true
			}
		}
	}
	try()
	if !ip.portalDone {
		o.Log("login name: the Blizzard-domains check isn't in memory yet; still looking")
	}
	if !ip.keyDone {
		o.Log("server key: the game's built-in keys aren't in memory yet; still looking")
	}
	users.Add(1)
	go func() {
		defer users.Done()
		for end := time.Now().Add(time.Minute); (!ip.portalDone || !ip.keyDone) && time.Now().Before(end) &&
			p.Alive(); {
			time.Sleep(500 * time.Millisecond)
			try()
		}
		if !p.Alive() {
			return
		}
		if !ip.portalDone {
			o.Log("login name: never found the Blizzard-domains check, so not patched; logging in at a name of " +
				"our own will fail")
		}
		if !ip.keyDone {
			o.Log("server key: never found the game's built-in keys, so not changed; entering the world will fail")
		}
		guardImage(p, kp, ip, o)
	}()
}

// guardImage re-applies a patch if the original bytes show up again at a patched address.
func guardImage(p *Process, kp *KeyPatcher, ip imagePatch, o Options) {
	suf := portalSuffix()
	orig := kp.keys[kp.group-1][:]
	for end := time.Now().Add(5 * time.Minute); time.Now().Before(end) && p.Alive(); time.Sleep(time.Second) {
		for _, a := range ip.portal.Suffixes {
			if bytes.Equal(p.Read(a, len(suf)), suf) {
				ip.portal.Apply(p)
				o.Log("login name: the game restored the check; patched it again")
				break
			}
		}
		for _, a := range ip.keys {
			if bytes.Equal(p.Read(a, len(orig)), orig) {
				kp.PatchBuiltIn(p, ip.keys)
				o.Log("server key: the game restored its built-in key; written again")
				break
			}
		}
	}
}

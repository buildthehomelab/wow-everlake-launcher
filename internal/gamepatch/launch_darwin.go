//go:build darwin

package gamepatch

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed macos/keypatch.c.txt
var macPatch []byte

// Launch prepares a private signed app copy, then uses the project's macOS
// patch helper. The original downloaded app remains available for recovery.
func Launch(o Options) (*Game, error) {
	o.defaults()
	if _, err := NewKeyPatcher(o.ServerKey, o.RegionGroup); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(o.Dir, ".everlake-launch-")
	if err != nil {
		return nil, err
	}
	app := work + ".app"
	success := false
	defer func() {
		if !success {
			os.RemoveAll(work)
			os.RemoveAll(app)
		}
	}()
	run := func(name string, args ...string) error {
		output, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	source := filepath.Dir(filepath.Dir(filepath.Dir(o.Exe)))

	if err := run("/usr/bin/ditto", source, app); err != nil {
		return nil, err
	}
	c := filepath.Join(work, "keypatch.c")
	lib := filepath.Join(work, "keypatch.dylib")
	// Release apps ship a universal helper, so players do not need Xcode.
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	bundled := filepath.Join(filepath.Dir(binary), "..", "Resources", "keypatch.dylib")
	if data, err := os.ReadFile(bundled); err == nil {
		if err := os.WriteFile(lib, data, 0700); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		if err := os.WriteFile(c, macPatch, 0600); err != nil {
			return nil, err
		}
		if err := run("/usr/bin/clang", "-O2", "-dynamiclib", "-o", lib, c); err != nil {
			return nil, err
		}
	}
	ent := filepath.Join(work, "entitlements.plist")
	xml := `<?xml version="1.0"?><plist version="1.0"><dict><key>com.apple.security.cs.allow-jit</key><true/><key>com.apple.security.cs.allow-unsigned-executable-memory</key><true/><key>com.apple.security.cs.disable-executable-page-protection</key><true/><key>com.apple.security.cs.disable-library-validation</key><true/><key>com.apple.security.cs.allow-dyld-environment-variables</key><true/></dict></plist>`
	if err := os.WriteFile(ent, []byte(xml), 0600); err != nil {
		return nil, err
	}
	if err := run("/usr/bin/codesign", "--force", "--sign", "-", lib); err != nil {
		return nil, err
	}
	if err := run("/usr/bin/codesign", "--force", "--deep", "--sign", "-", "--entitlements", ent, app); err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(app, "Contents", "MacOS", filepath.Base(o.Exe)), o.Args...)
	cmd.Dir = o.Dir
	env := o.Env
	if env == nil {
		env = os.Environ()
	}
	for _, v := range env {
		if !strings.HasPrefix(v, "DYLD_INSERT_LIBRARIES=") && !strings.HasPrefix(v, "FOREVER_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "DYLD_INSERT_LIBRARIES="+lib, "FOREVER_SERVER_KEY="+o.ServerKey, "FOREVER_REGION_GROUP="+strconv.Itoa(o.RegionGroup), "FOREVER_PATCH_LOG="+filepath.Join(o.Dir, "everlake-patch.log"))
	if o.AnyPortal {
		cmd.Env = append(cmd.Env, "FOREVER_ANY_PORTAL=1")
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	success = true
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		if err != nil {
			o.Log("Game exited: " + err.Error())
		}
		os.RemoveAll(work)
		os.RemoveAll(app)
		close(done)
	}()
	return &Game{Pid: cmd.Process.Pid, Done: done}, nil
}

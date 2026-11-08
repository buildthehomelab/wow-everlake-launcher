package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/gamepatch"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/realm"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/secret"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/settings"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/webui"
)

// stdin is shared by the email and password prompts, so neither loses what the other buffered. main sets it once
// the Windows GUI build has borrowed its terminal's handles.
var stdin *bufio.Reader

// cmdPlay signs in to a realm's login server, hands the game the login, and starts it with the patches it needs to
// talk to that realm. It stays until the game closes, keeping the patches in place and reporting on the server key.
func cmdPlay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("play", flag.ExitOnError)
	dir := fs.String("dir", "", "install folder (required)")
	host := fs.String("login-host", "", "the realm's login server name (saved for next time)")
	email := fs.String("email", "", "account email (saved for next time)")
	key := fs.String("server-key", "", "the realm's public key, 64 hex characters (saved; default: TrinityCore's stock key)")
	ca := fs.String("ca", "", "a CA certificate to trust for the login server (saved; default: the system's)")
	remember := fs.Bool("remember", false, "remember the password for this Windows user (encrypted with DPAPI); a "+
		"remembered password is used when EVERLAKE_PASSWORD is empty")
	fs.Parse(args)
	if *dir == "" {
		return fmt.Errorf("play: --dir is required")
	}
	if runtime.GOOS != "windows" {
		return gamepatch.ErrUnsupported
	}
	s, err := realm.LoadSettings(*dir)
	if err != nil {
		return err
	}
	if *host != "" {
		s = s.ForHost(*host)
	}
	if *email != "" {
		s.Email = *email
	}
	if *key != "" {
		s.ServerKey = *key
	}
	if *ca != "" {
		s.CAFile = *ca
	}
	if err := s.Check(); err != nil {
		return err
	}
	if s.Email == "" {
		fmt.Print("Email: ")
		line, _ := stdin.ReadString('\n')
		s.Email = strings.TrimSpace(line)
	}
	log := func(l string) { logf("%s", l) }
	password, saved := os.Getenv("EVERLAKE_PASSWORD"), false
	if password == "" {
		if password, saved = rememberedPassword(s.LoginHost, s.Email); saved {
			logf("using the remembered password")
		} else if password, err = readPassword(fmt.Sprintf("Password for %s: ", s.Email)); err != nil {
			return err
		}
	}
	game, err := joinRealm(ctx, *dir, s, password, func() {
		if *remember && !saved {
			rememberPassword(s.LoginHost, s.Email, password, log)
		}
	}, log)
	if err != nil {
		if saved && errors.Is(err, realm.ErrRefused) {
			forgetPassword(s.LoginHost, s.Email, log)
		}
		return err
	}
	logf("leave this window open while you play: it keeps the patches in place and reports on them (Ctrl-C closes it)")
	select {
	case <-game.Done:
		logf("the game closed")
	case <-ctx.Done():
	}
	return nil
}

// joinRealm signs in, stores the login for the game, saves the settings, and starts the patched game. signedIn, if
// set, runs once the login server has accepted the password.
func joinRealm(ctx context.Context, dir string, s realm.Settings, password string, signedIn func(),
	log func(string)) (*gamepatch.Game, error) {
	if runtime.GOOS != "windows" {
		return nil, gamepatch.ErrUnsupported
	}
	if err := s.Check(); err != nil {
		return nil, err
	}
	if s.Email == "" || password == "" {
		return nil, fmt.Errorf("email and password are needed to sign in")
	}
	// The key is checked before anything is signed in, saved or written.
	if _, err := gamepatch.NewKeyPatcher(keyOrStock(s.ServerKey), groupOrDefault(s.RegionGroup)); err != nil {
		return nil, fmt.Errorf("realm settings: %w", err)
	}
	st, err := install.ReadState(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, install.ErrNoInstall)
	}
	exe := install.GameExe(dir, st.Pin.GameFolder)
	if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("no game in %s: run install first", dir)
	}
	gameDir := filepath.Join(dir, st.Pin.GameFolder)

	log(fmt.Sprintf("signing in to %s as %s", s.LoginHost, s.Email))
	login, err := realm.SignIn(ctx, s, nil, s.Email, password)
	if err != nil {
		return nil, err
	}
	if signedIn != nil {
		signedIn()
	}
	if err := realm.SaveSettings(dir, s); err != nil {
		log("note: couldn't save the realm settings: " + err.Error())
	}
	if err := realm.WriteLaunchOptions(s.Portal(), login); err != nil {
		return nil, err
	}
	if err := realm.WriteWTF(gameDir, s.LoginHost); err != nil {
		return nil, fmt.Errorf("game config: %w", err)
	}
	log(fmt.Sprintf("signed in, game account %s", login.GameAccount))

	args := append(st.Pin.StartArgs(), "-launcherlogin", "-config", realm.WTFName)
	game, err := gamepatch.Launch(gamepatch.Options{
		Exe: exe, Args: args, Dir: gameDir, Env: gameEnv(),
		ServerKey: s.ServerKey, RegionGroup: s.RegionGroup,
		// Blizzard's own login names pass the client's check already; anything else needs it switched off.
		AnyPortal: !strings.HasSuffix(strings.ToLower(s.LoginHost), ".actual.battle.net"),
		Log:       log,
	})
	if err != nil {
		return nil, err
	}
	log(fmt.Sprintf("started the game (process %d)", game.Pid))
	return game, nil
}

// uiPlay is the page's Play button: with a login server, join that realm; without one, start the game on its own.
func uiPlay() func(ctx context.Context, dir string, in webui.PlayInput, log func(string)) error {
	return func(ctx context.Context, dir string, in webui.PlayInput, log func(string)) error {
		if strings.TrimSpace(in.LoginHost) == "" || runtime.GOOS != "windows" {
			pid, err := install.Start(dir)
			if err != nil {
				return err
			}
			log(fmt.Sprintf("started the game (process %d)", pid))
			return nil
		}
		s, err := realm.LoadSettings(dir)
		if err != nil {
			return err
		}
		s = s.ForHost(strings.TrimSpace(in.LoginHost))
		s.Email = strings.TrimSpace(in.Email)
		// An empty password field means the remembered one.
		password, saved := in.Password, false
		if password == "" {
			if password, saved = rememberedPassword(s.LoginHost, s.Email); saved {
				log("using the remembered password")
			}
		}
		// The game outlives the job; its patches keep logging to the page and stop when it closes.
		game, err := joinRealm(context.Background(), dir, s, password, func() {
			if in.Remember && !saved {
				rememberPassword(s.LoginHost, s.Email, password, log)
			} else if !in.Remember {
				forgetPassword(s.LoginHost, s.Email, log)
			}
		}, log)
		if err != nil {
			if saved && errors.Is(err, realm.ErrRefused) {
				forgetPassword(s.LoginHost, s.Email, log)
			}
			return err
		}
		trackGame(game)
		log("the launcher keeps the patches in place until the game closes; closing this window is fine, the " +
			"launcher keeps running in the background until then")
		return nil
	}
}

// The games the ui joined to a realm and is still patching. Closing the window doesn't end the launcher while one
// runs: it keeps the patches in place.
var (
	gamesMu sync.Mutex
	games   = map[*gamepatch.Game]bool{}
)

func trackGame(g *gamepatch.Game) {
	gamesMu.Lock()
	games[g] = true
	gamesMu.Unlock()
	go func() {
		<-g.Done
		gamesMu.Lock()
		delete(games, g)
		gamesMu.Unlock()
	}()
}

func gamesRunning() int {
	gamesMu.Lock()
	defer gamesMu.Unlock()
	return len(games)
}

// waitForGames returns once every tracked game has closed (or ctx ends).
func waitForGames(ctx context.Context) {
	for gamesRunning() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func uiRealm() func(dir string) webui.RealmStatus {
	return func(dir string) webui.RealmStatus {
		s, _ := realm.LoadSettings(dir)
		saved := false
		if st, err := settings.Load(); err == nil && s.LoginHost != "" && s.Email != "" {
			saved = len(st.Passwords[settings.PasswordKey(s.LoginHost, s.Email)]) > 0
		}
		return webui.RealmStatus{LoginHost: s.LoginHost, Email: s.Email, Supported: runtime.GOOS == "windows",
			CanRemember: secret.Available(), PasswordSaved: saved}
	}
}

// rememberedPassword is the password remembered for a login server and account, if there is one this user can
// open.
func rememberedPassword(host, email string) (string, bool) {
	if host == "" || email == "" || !secret.Available() {
		return "", false
	}
	st, err := settings.Load()
	if err != nil {
		return "", false
	}
	sealed := st.Passwords[settings.PasswordKey(host, email)]
	if len(sealed) == 0 {
		return "", false
	}
	plain, err := secret.Open(sealed)
	if err != nil || len(plain) == 0 {
		return "", false
	}
	return string(plain), true
}

// rememberPassword keeps a password that just signed in, sealed for this Windows user.
func rememberPassword(host, email, password string, log func(string)) {
	sealed, err := secret.Seal([]byte(password))
	if err == nil {
		err = settings.Update(func(st *settings.Settings) {
			if st.Passwords == nil {
				st.Passwords = map[string][]byte{}
			}
			st.Passwords[settings.PasswordKey(host, email)] = sealed
		})
	}
	if err != nil {
		log("note: couldn't remember the password: " + err.Error())
		return
	}
	log("password remembered for this Windows user (encrypted; untick Remember password to forget it)")
}

// forgetPassword drops a remembered password, if there is one.
func forgetPassword(host, email string, log func(string)) {
	st, err := settings.Load()
	key := settings.PasswordKey(host, email)
	if err != nil || len(st.Passwords[key]) == 0 {
		return
	}
	if err := settings.Update(func(st *settings.Settings) { delete(st.Passwords, key) }); err != nil {
		log("note: couldn't forget the remembered password: " + err.Error())
		return
	}
	log("the remembered password is forgotten")
}

func keyOrStock(k string) string {
	if k == "" {
		return gamepatch.StockServerKey
	}
	return k
}

func groupOrDefault(g int) int {
	if g == 0 {
		return gamepatch.DefaultRegionGroup
	}
	return g
}

// gameEnv is the launcher's environment without the password.
func gameEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "EVERLAKE_PASSWORD=") {
			env = append(env, kv)
		}
	}
	return env
}

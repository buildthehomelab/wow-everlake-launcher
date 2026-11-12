// Package realm signs in to an Everlake login server and prepares the game to log in with the ticket it hands out,
// the way Battle.net does for Blizzard's realms: a login ticket where the game reads it when started with
// -launcherlogin, and a game config (WTF) naming the login server.
package realm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Settings name the realm to join. They are saved in the install folder so a player types them once.
type Settings struct {
	LoginHost   string `json:"login_host"`             // the login server's name, e.g. login.realm.example.com
	BnetPort    int    `json:"bnet_port,omitempty"`    // game login port (1119)
	RestPort    int    `json:"rest_port,omitempty"`    // web login port (8081)
	ServerKey   string `json:"server_key,omitempty"`   // the realm's public key; empty = TrinityCore's stock key
	RegionGroup int    `json:"region_group,omitempty"` // 0 = 8
	CAFile      string `json:"ca_file,omitempty"`      // a CA to trust for the login server instead of the system's
	Email       string `json:"email,omitempty"`        // the last account used, to offer it again
}

// SettingsFile is where Settings live in an install folder.
const SettingsFile = ".everlake/realm.json"

// ForHost returns the settings for a login server. Moving to a different one drops what belonged to the old realm
// (its ports, key, CA and region group); the email stays.
func (s Settings) ForHost(host string) Settings {
	if strings.EqualFold(host, s.LoginHost) {
		return s
	}
	return Settings{LoginHost: host, Email: s.Email}
}

// WithDefaults fills in the standard ports.
func (s Settings) WithDefaults() Settings {
	if s.BnetPort == 0 {
		s.BnetPort = 1119
	}
	if s.RestPort == 0 {
		s.RestPort = 8081
	}
	return s
}

// Check reports what's missing or malformed.
func (s Settings) Check() error {
	if s.LoginHost == "" {
		return errors.New("no login server set (--login-host)")
	}
	for _, c := range s.LoginHost {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return fmt.Errorf("login host %q must be a plain name (letters, digits, dots, dashes), without "+
				"scheme or port", s.LoginHost)
		}
	}
	return nil
}

// LoadSettings reads the install folder's realm settings; a missing file is empty settings.
func LoadSettings(dir string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(dir, SettingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", SettingsFile, err)
	}
	return s, nil
}

// SaveSettings writes them back.
func SaveSettings(dir string, s Settings) error {
	p := filepath.Join(dir, SettingsFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Portal is the login server as the game wants it: name:port.
func (s Settings) Portal() string {
	s = s.WithDefaults()
	return net.JoinHostPort(s.LoginHost, strconv.Itoa(s.BnetPort))
}

// Login is what a successful sign-in gives the game.
type Login struct {
	Ticket      string // the login ticket the game presents to the login server
	GameAccount string // e.g. WoW1
}

// ErrRefused is a sign-in the login server turned down (a wrong email or password, a locked account), as opposed to
// one that never reached it.
var ErrRefused = errors.New("sign-in refused")

// SignIn signs in with email and password the way Battle.net's launcher does against a TrinityCore login server:
// /login/ (email + password -> ticket), /launcher/remember/ (ticket -> launcher token), /launcher/login/
// (token -> a fresh ticket and the game accounts). client may be nil.
func SignIn(ctx context.Context, s Settings, client *http.Client, email, password string) (*Login, error) {
	s = s.WithDefaults()
	if err := s.Check(); err != nil {
		return nil, err
	}
	if client == nil {
		var err error
		if client, err = httpClient(s.CAFile); err != nil {
			return nil, err
		}
	}
	base := "https://" + net.JoinHostPort(s.LoginHost, strconv.Itoa(s.RestPort)) + "/bnetserver"
	post := func(path string, body any, ticket string) (int, map[string]any, error) {
		var rd io.Reader = http.NoBody
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, rd)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if ticket != "" {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(ticket+":")))
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("login server %s: %w", s.LoginHost, err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		if len(bytes.TrimSpace(raw)) > 0 {
			json.Unmarshal(raw, &out)
		}
		return res.StatusCode, out, nil
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }

	status, r, err := post("/login/", map[string]any{"version": "1.0", "inputs": []map[string]string{
		{"input_id": "account_name", "value": email},
		{"input_id": "password", "value": password},
	}}, "")
	if err != nil {
		return nil, err
	}
	ticket := str(r, "login_ticket")
	if ticket == "" {
		// Only the login server's own answer is a refusal: a proxy's error page while it restarts is not.
		state := str(r, "authentication_state")
		if status != http.StatusOK || state == "" {
			return nil, fmt.Errorf("login server %s answered %d without a ticket; try again in a minute", s.LoginHost, status)
		}
		why := str(r, "error_message")
		if why == "" {
			why = "wrong email or password"
		}
		return nil, fmt.Errorf("%w: %s (%s)", ErrRefused, why, state)
	}
	status, r, err = post("/launcher/remember/", nil, ticket)
	if err != nil {
		return nil, err
	}
	token := str(r, "token")
	if token == "" {
		return nil, fmt.Errorf("sign-in: launcher/remember refused (%d)", status)
	}
	status, r, err = post("/launcher/login/", nil, token)
	if err != nil {
		return nil, err
	}
	l := &Login{Ticket: str(r, "login_ticket"), GameAccount: "WoW1"}
	if status != http.StatusOK || l.Ticket == "" {
		return nil, fmt.Errorf("sign-in: launcher/login refused (%d)", status)
	}
	if accts, ok := r["game_accounts"].([]any); ok {
		for _, a := range accts {
			if m, ok := a.(map[string]any); ok && str(m, "name") != "" {
				l.GameAccount = str(m, "name")
				break
			}
		}
	}
	return l, nil
}

func httpClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s: no certificate in it", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: 20 * time.Second}, nil
}

// WTFName is the game config the launcher starts the game with (-config), so the player's own Config.wtf is left
// alone.
const WTFName = "Everlake.wtf"

// WriteWTF writes WTF/Everlake.wtf into the game folder: the player's Config.wtf (or the previous Everlake.wtf)
// with the portal set to the login server and the language set.
func WriteWTF(gameDir, portal string) error {
	dir := filepath.Join(gameDir, "WTF")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, WTFName)
	text, err := os.ReadFile(path)
	if err != nil {
		text, _ = os.ReadFile(filepath.Join(dir, "Config.wtf"))
	}
	wanted := [][2]string{{"portal", portal}, {"textLocale", "enUS"}, {"audioLocale", "enUS"}}
	// This launcher's private config explicitly disables narrated onboarding and
	// screen narration on every launch. The player's original Config.wtf is untouched.
	wanted = append(wanted, [2]string{"accessibilityScreenNarrationEnabled", "0"}, [2]string{"showScreenNarrationDialog", "0"})

	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n") {
		keep := strings.TrimSpace(l) != ""
		for _, w := range wanted {
			fields := strings.Fields(l)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "SET") && strings.EqualFold(fields[1], w[0]) {
				keep = false
			}
		}
		if keep {
			lines = append(lines, l)
		}
	}
	for _, w := range wanted {
		lines = append(lines, fmt.Sprintf("SET %s %q", w[0], w[1]))
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644)
}

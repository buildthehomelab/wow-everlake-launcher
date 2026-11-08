package realm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeLoginServer answers like a TrinityCore login server's REST service.
func fakeLoginServer(t *testing.T) *httptest.Server {
	auth := func(r *http.Request) string {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Basic "))
		return strings.TrimSuffix(string(b), ":")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bnetserver/login/", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs []struct {
				ID    string `json:"input_id"`
				Value string `json:"value"`
			} `json:"inputs"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		in := map[string]string{}
		for _, i := range body.Inputs {
			in[i.ID] = i.Value
		}
		if in["account_name"] == "proxy@example.com" { // the login server is down behind its proxy
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html>502 Bad Gateway</html>"))
			return
		}
		if in["account_name"] == "player@example.com" && in["password"] == "secret" {
			json.NewEncoder(w).Encode(map[string]any{"authentication_state": "DONE", "login_ticket": "TC-first"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"authentication_state": "LOGIN", "error_code": "UNABLE_TO_DECODE"})
	})
	mux.HandleFunc("/bnetserver/launcher/remember/", func(w http.ResponseWriter, r *http.Request) {
		if auth(r) != "TC-first" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "REMEMBER"})
	})
	mux.HandleFunc("/bnetserver/launcher/login/", func(w http.ResponseWriter, r *http.Request) {
		if auth(r) != "REMEMBER" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"login_ticket": "TC-fresh",
			"game_accounts": []map[string]any{{"name": "WoW3"}}})
	})
	return httptest.NewTLSServer(mux)
}

func settingsFor(t *testing.T, ts *httptest.Server) Settings {
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "https://"))
	p, _ := strconv.Atoi(port)
	return Settings{LoginHost: host, RestPort: p}
}

func TestSignIn(t *testing.T) {
	ts := fakeLoginServer(t)
	defer ts.Close()
	s := settingsFor(t, ts)
	l, err := SignIn(context.Background(), s, ts.Client(), "player@example.com", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if l.Ticket != "TC-fresh" || l.GameAccount != "WoW3" {
		t.Errorf("login: %+v", l)
	}
	if _, err := SignIn(context.Background(), s, ts.Client(), "player@example.com", "wrong"); err == nil ||
		!strings.Contains(err.Error(), "LOGIN") || !errors.Is(err, ErrRefused) {
		t.Errorf("wrong password: %v", err)
	}
	// A proxy's error page is not a refusal: a remembered password must survive the login server restarting.
	if _, err := SignIn(context.Background(), s, ts.Client(), "proxy@example.com", "secret"); err == nil ||
		errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "502") {
		t.Errorf("login server down: %v", err)
	}
	// The system's CAs don't trust the test server: the default client refuses it.
	if _, err := SignIn(context.Background(), s, nil, "player@example.com", "secret"); err == nil {
		t.Error("an untrusted certificate was accepted")
	}
}

func TestSignInWithCAFile(t *testing.T) {
	ts := fakeLoginServer(t)
	defer ts.Close()
	s := settingsFor(t, ts)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	cert := ts.Certificate()
	pem := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(cert.Raw) + "\n-----END CERTIFICATE-----\n"
	os.WriteFile(ca, []byte(pem), 0o644)
	s.CAFile = ca
	if _, err := SignIn(context.Background(), s, nil, "player@example.com", "secret"); err != nil {
		t.Fatal(err)
	}
}

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadSettings(dir)
	if err != nil || s.LoginHost != "" {
		t.Fatalf("empty folder: %+v %v", s, err)
	}
	s = Settings{LoginHost: "login.realm.example.com", Email: "a@b.c"}
	if err := SaveSettings(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(dir)
	if err != nil || got != s {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if got.Portal() != "login.realm.example.com:1119" {
		t.Errorf("portal %q", got.Portal())
	}
	old := Settings{LoginHost: "a.example.com", ServerKey: "ab", CAFile: "ca.pem", RegionGroup: 5, Email: "e@x"}
	if got := old.ForHost("A.example.com"); got != old {
		t.Errorf("same host changed the settings: %+v", got)
	}
	if got := old.ForHost("b.example.com"); got != (Settings{LoginHost: "b.example.com", Email: "e@x"}) {
		t.Errorf("new host kept the old realm's settings: %+v", got)
	}
	for _, bad := range []string{"", "https://x", "x:1119", "a b", "user@host", "host?x", "host#x"} {
		if (Settings{LoginHost: bad}).Check() == nil {
			t.Errorf("accepted login host %q", bad)
		}
	}
}

func TestWriteWTF(t *testing.T) {
	game := t.TempDir()
	os.MkdirAll(filepath.Join(game, "WTF"), 0o755)
	os.WriteFile(filepath.Join(game, "WTF", "Config.wtf"),
		[]byte("SET gxWindow \"1\"\r\nSET portal \"US\"\r\nSET textLocale \"deDE\"\r\n"), 0o644)
	if err := WriteWTF(game, "login.realm.example.com"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(game, "WTF", WTFName))
	want := "SET gxWindow \"1\"\r\nSET portal \"login.realm.example.com\"\r\nSET textLocale \"enUS\"\r\n" +
		"SET audioLocale \"enUS\"\r\n"
	if string(b) != want {
		t.Errorf("Everlake.wtf:\n%q\nwant\n%q", b, want)
	}
	// Writing again keeps the file as it is (no duplicate lines); the player's Config.wtf is untouched.
	WriteWTF(game, "login.realm.example.com")
	if b2, _ := os.ReadFile(filepath.Join(game, "WTF", WTFName)); string(b2) != want {
		t.Errorf("second write:\n%q", b2)
	}
	if c, _ := os.ReadFile(filepath.Join(game, "WTF", "Config.wtf")); !strings.Contains(string(c), `portal "US"`) {
		t.Error("Config.wtf changed")
	}
}

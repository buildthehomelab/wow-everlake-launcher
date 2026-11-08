package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPageAndStatus(t *testing.T) {
	ts := httptest.NewServer(New(t.TempDir(), "1.60.1.70790", Actions{}).Handler())
	defer ts.Close()

	body := get(t, ts.URL+"/")
	if !strings.Contains(body, "Everlake launcher") || !strings.Contains(body, "events") {
		t.Error("page missing")
	}
	var s statusResp
	if err := json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s); err != nil {
		t.Fatal(err)
	}
	if s.Installed || s.Version != "1.60.1.70790" {
		t.Errorf("status of an empty folder: %+v", s)
	}
}

func TestRunStreamsAndGuards(t *testing.T) {
	release := make(chan struct{})
	ran := 0
	srv := New(t.TempDir(), "x", Actions{
		Install: func(ctx context.Context, log func(string)) error {
			ran++
			log("line one")
			<-release
			log("line two")
			return nil
		},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusAccepted {
		t.Fatalf("first run: %d", code)
	}
	// Open the event stream and read the lines so far.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if t := strings.TrimPrefix(sc.Text(), "data: "); t != sc.Text() {
				var s string
				json.Unmarshal([]byte(t), &s)
				lines <- s
			}
		}
	}()
	waitFor(t, lines, "line one")

	// A second run while the first is going is refused.
	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusConflict {
		t.Errorf("second run during the first: %d", code)
	}
	close(release)
	waitFor(t, lines, "line two")
	waitFor(t, lines, "=== done ===")
	if ran != 1 {
		t.Errorf("install ran %d times", ran)
	}

	// After it finishes, a new run is allowed.
	release2 := make(chan struct{})
	close(release2)
	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusAccepted {
		t.Errorf("run after done: %d", code)
	}
}

func TestRunValidation(t *testing.T) {
	ts := httptest.NewServer(New(t.TempDir(), "x", Actions{}).Handler())
	defer ts.Close()
	if code := post(t, ts.URL+"/run?name=nonsense"); code != http.StatusBadRequest {
		t.Errorf("unknown job: %d", code)
	}
	res, _ := http.Get(ts.URL + "/run?name=install")
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET on run: %d", res.StatusCode)
	}
	// play with no Play action set reports it, not a crash.
	res, _ = http.Post(ts.URL+"/run?name=play", "application/json", strings.NewReader("{}"))
	res.Body.Close()
	// Play only takes JSON (a cross-site form post can't send that without a preflight).
	res, _ = http.Post(ts.URL+"/run?name=play", "text/plain", strings.NewReader(`{"login_host":"x"}`))
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain play: %d", res.StatusCode)
	}
}

func TestLocalOnly(t *testing.T) {
	ts := httptest.NewServer(New(t.TempDir(), "x", Actions{}).Handler())
	defer ts.Close()
	for _, c := range []struct {
		host, origin string
		want         int
	}{
		{"", "", http.StatusOK},
		{"", ts.URL, http.StatusOK},
		{"", "https://evil.example", http.StatusForbidden},
		{"evil.example", "", http.StatusForbidden},
		{"localhost:1234", "", http.StatusOK},
	} {
		req, _ := http.NewRequest("GET", ts.URL+"/status", nil)
		if c.host != "" {
			req.Host = c.host
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Errorf("host %q origin %q: %d, want %d", c.host, c.origin, res.StatusCode, c.want)
		}
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := new(strings.Builder)
	bufio.NewReader(res.Body).WriteTo(b)
	return b.String()
}

func post(t *testing.T, url string) int {
	t.Helper()
	res, err := http.Post(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func waitFor(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	for {
		select {
		case l := <-lines:
			if l == want {
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestPlayGetsSignInAndRealmStatus(t *testing.T) {
	got := make(chan PlayInput, 1)
	srv := New(t.TempDir(), "x", Actions{
		Play: func(ctx context.Context, in PlayInput, log func(string)) error {
			got <- in
			return nil
		},
		Realm: func() RealmStatus { return RealmStatus{LoginHost: "login.realm.example.com", Supported: true} },
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var s statusResp
	json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s)
	if s.Realm.LoginHost != "login.realm.example.com" || !s.Realm.Supported {
		t.Errorf("realm in status: %+v", s.Realm)
	}
	body := `{"login_host":"login.realm.example.com","email":"a@b.c","password":"pw"}`
	res, err := http.Post(ts.URL+"/run?name=play", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("play: %d", res.StatusCode)
	}
	select {
	case in := <-got:
		if in != (PlayInput{LoginHost: "login.realm.example.com", Email: "a@b.c", Password: "pw"}) {
			t.Errorf("play got %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("play never ran")
	}
	res, _ = http.Post(ts.URL+"/run?name=play", "application/json", strings.NewReader("{nope"))
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body: %d", res.StatusCode)
	}
}

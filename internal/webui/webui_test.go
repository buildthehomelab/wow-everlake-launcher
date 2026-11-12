package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
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
		Install: func(ctx context.Context, dir string, log func(string)) error {
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
		Play: func(ctx context.Context, dir string, in PlayInput, log func(string)) error {
			got <- in
			return nil
		},
		Realm: func(dir string) RealmStatus {
			return RealmStatus{LoginHost: "login.realm.example.com", Supported: true}
		},
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

func TestUnwatched(t *testing.T) {
	srv := New(t.TempDir(), "x", Actions{})
	if srv.Unwatched(time.Hour) {
		t.Error("unwatched right after starting")
	}
	if !srv.Unwatched(0) {
		t.Error("not unwatched with no page and no job")
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A page with its log stream open keeps the launcher going.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if srv.Unwatched(0) {
		t.Error("unwatched while a page is open")
	}
	cancel()
	res.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for !srv.Unwatched(0) {
		if time.Now().After(deadline) {
			t.Fatal("still watched after the page closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if srv.Unwatched(time.Hour) {
		t.Error("the closed page's time wasn't recorded")
	}

	// So does a running job.
	release := make(chan struct{})
	defer close(release)
	srv.act.Install = func(ctx context.Context, dir string, log func(string)) error { <-release; return nil }
	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusAccepted {
		t.Fatalf("run: %d", code)
	}
	if srv.Unwatched(0) {
		t.Error("unwatched while a job runs")
	}
}

func postJSON(t *testing.T, url, body string) (int, string) {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b := new(strings.Builder)
	bufio.NewReader(res.Body).WriteTo(b)
	return res.StatusCode, b.String()
}

func TestFolderChange(t *testing.T) {
	start, other := t.TempDir(), t.TempDir()
	var used, saved []string
	jobDir := make(chan string, 1)
	release := make(chan struct{})
	pickReply, pickCancelled := other, false
	srv := New(start, "x", Actions{
		Install: func(ctx context.Context, dir string, log func(string)) error {
			jobDir <- dir
			<-release
			return nil
		},
		PickFolder: func(from string) (string, bool, error) {
			if from == "" {
				t.Error("dialog not opened at the current folder")
			}
			return pickReply, pickCancelled, nil
		},
		UseFolder: func(p string) (string, bool, error) {
			used = append(used, p)
			if p == "/bad" {
				return "", false, errors.New("can't write to /bad")
			}
			return p, false, nil
		},
		SaveFolder: func(d string) error { saved = append(saved, d); return nil },
		Installed:  func(d string) bool { return d == start },
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var s statusResp
	json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s)
	if s.Dir != start || !s.CanPick {
		t.Fatalf("status before: %+v", s)
	}

	// A typed folder is used; moving away from an install says the old copy stays.
	code, body := postJSON(t, ts.URL+"/folder", `{"dir":`+quote(other)+`}`)
	var fr folderResp
	json.Unmarshal([]byte(body), &fr)
	if code != http.StatusOK || fr.Dir != other || !strings.Contains(fr.Note, "stays where it is") {
		t.Fatalf("typed folder: %d %s", code, body)
	}
	json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s)
	if s.Dir != other {
		t.Errorf("status after change: %s", s.Dir)
	}

	// A folder that can't be used is refused and the folder stays.
	if code, body := postJSON(t, ts.URL+"/folder", `{"dir":"/bad"}`); code != http.StatusBadRequest ||
		!strings.Contains(body, "can't write") {
		t.Errorf("bad folder: %d %s", code, body)
	}

	// Browse: a cancelled dialog changes nothing; a picked folder is used, and an install there is noticed.
	pickCancelled = true
	code, body = postJSON(t, ts.URL+"/folder", `{"browse":true}`)
	fr = folderResp{}
	json.Unmarshal([]byte(body), &fr)
	if code != http.StatusOK || !fr.Cancelled || fr.Dir != other {
		t.Errorf("cancelled browse: %d %s", code, body)
	}
	pickReply, pickCancelled = start, false
	code, body = postJSON(t, ts.URL+"/folder", `{"browse":true}`)
	fr = folderResp{}
	json.Unmarshal([]byte(body), &fr)
	if code != http.StatusOK || fr.Dir != start || !strings.Contains(fr.Note, "already installed") {
		t.Errorf("browse: %d %s", code, body)
	}
	if len(used) != 3 || len(saved) != 2 || saved[1] != start {
		t.Errorf("UseFolder calls %v, saves %v", used, saved)
	}
	// The current folder picked again is kept as it is (no subfolder for a half-finished download).
	code, body = postJSON(t, ts.URL+"/folder", `{"dir":`+quote(start+"/")+`}`)
	fr = folderResp{}
	json.Unmarshal([]byte(body), &fr)
	if code != http.StatusOK || fr.Dir != start || len(used) != 3 || len(saved) != 2 {
		t.Errorf("same folder again: %d %s, used %v", code, body, used)
	}

	// Jobs run in the folder current when they start, and the folder can't change under one.
	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusAccepted {
		t.Fatalf("run: %d", code)
	}
	if d := <-jobDir; d != start {
		t.Errorf("job ran in %s", d)
	}
	if code, _ := postJSON(t, ts.URL+"/folder", `{"dir":`+quote(other)+`}`); code != http.StatusConflict {
		t.Errorf("change during a job: %d", code)
	}
	close(release)

	// Only JSON, only POST.
	res, _ := http.Post(ts.URL+"/folder", "text/plain", strings.NewReader(`{"dir":"/x"}`))
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain folder: %d", res.StatusCode)
	}
	res, _ = http.Get(ts.URL + "/folder")
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET folder: %d", res.StatusCode)
	}
}

func TestFolderUnavailable(t *testing.T) {
	ts := httptest.NewServer(New(t.TempDir(), "x", Actions{}).Handler())
	defer ts.Close()
	var s statusResp
	json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s)
	if s.CanPick {
		t.Error("can_pick with no dialog")
	}
	if code, _ := postJSON(t, ts.URL+"/folder", `{"dir":"/x"}`); code != http.StatusNotImplemented {
		t.Errorf("folder with no UseFolder: %d", code)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The launcher's own jobs (its update) run like the page's: the page sees them busy, and buttons wait.
func TestBeginLauncherJob(t *testing.T) {
	srv := New(t.TempDir(), "x", Actions{LauncherVersion: "v0.1.0-alpha.14"})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	release := make(chan struct{})
	done := make(chan struct{})
	if !srv.Begin("update", func(log func(string)) error {
		log("launcher: 1.0 of 2.0 MB")
		<-release
		close(done)
		return nil
	}) {
		t.Fatal("Begin refused on an idle launcher")
	}
	var s statusResp
	json.Unmarshal([]byte(get(t, ts.URL+"/status")), &s)
	if s.Busy != "update" || s.Launcher != "v0.1.0-alpha.14" || srv.Busy() != "update" {
		t.Errorf("status during the update: %+v", s)
	}
	if code := post(t, ts.URL+"/run?name=install"); code != http.StatusConflict {
		t.Errorf("install during the update: %d", code)
	}
	if srv.Begin("update", func(func(string)) error { return nil }) {
		t.Error("a second launcher job started during the first")
	}
	close(release)
	<-done
	for i := 0; srv.Busy() != "" && i < 100; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.Busy() != "" {
		t.Fatal("still busy after the job ended")
	}
}

func TestLookSavedAndShown(t *testing.T) {
	var saved Look
	srv := New(t.TempDir(), "x", Actions{
		Look:     func() Look { return saved },
		SaveLook: func(l Look) error { saved = l; return nil },
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Nothing saved: the page follows the system and picks its own colour.
	if body := get(t, ts.URL+"/"); strings.Contains(body, "data-animation=") || strings.Contains(body, "data-saved-school=") {
		t.Error("page carries a look nobody picked")
	}

	if code, msg := postJSON(t, ts.URL+"/look", `{"animation":false}`); code != http.StatusNoContent {
		t.Fatalf("animation off: %d %s", code, msg)
	}
	if code, msg := postJSON(t, ts.URL+"/look", `{"school":"frost"}`); code != http.StatusNoContent {
		t.Fatalf("colour: %d %s", code, msg)
	}
	// One change at a time keeps the other.
	if saved.School != "frost" || saved.Animation == nil || *saved.Animation {
		t.Errorf("saved look: %+v", saved)
	}
	body := get(t, ts.URL+"/")
	if !strings.Contains(body, `<body data-saved-school="frost" data-animation="false" data-school=`) {
		t.Errorf("page doesn't start with the saved look: %q", body[strings.Index(body, "<body"):][:80])
	}

	// The colour goes into the page's HTML, so only a plain name is taken.
	for _, bad := range []string{`{"school":"x\" onload=\"y"}`, `{"school":"Frost"}`, `{nope`} {
		if code, _ := postJSON(t, ts.URL+"/look", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	if saved.School != "frost" {
		t.Errorf("a refused change was kept: %+v", saved)
	}
	if code := post(t, ts.URL+"/look"); code != http.StatusUnsupportedMediaType {
		t.Errorf("not JSON: %d", code)
	}
}

func TestLookWithoutStore(t *testing.T) {
	ts := httptest.NewServer(New(t.TempDir(), "x", Actions{}).Handler())
	defer ts.Close()
	if code, _ := postJSON(t, ts.URL+"/look", `{"animation":true}`); code != http.StatusNoContent {
		t.Errorf("no store: %d", code)
	}
}

func TestClientImportRoute(t *testing.T) {
	called := make(chan PlayInput, 1)
	s := New(t.TempDir(), "test", Actions{Client: func(ctx context.Context, dir, action string, in PlayInput, log func(string)) error {
		if action != "client-import" {
			t.Errorf("action = %s", action)
		}
		called <- in
		return nil
	}})
	req := httptest.NewRequest("POST", "http://localhost/run?name=client-import", strings.NewReader(`{"source":"/source","destination":"/target","product":"wowf"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	select {
	case in := <-called:
		if in.Source != "/source" || in.Destination != "/target" || in.Product != "wowf" {
			t.Fatalf("input %#v", in)
		}
	case <-time.After(time.Second):
		t.Fatal("import action never called")
	}
}

func TestQuickCheckDoesNotBlockOrDuplicatePolling(t *testing.T) {
	srv := New(t.TempDir(), "test", Actions{})
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	srv.quickCheck = func(string) (*install.Report, error) { started <- struct{}{}; <-release; return &install.Report{}, nil }
	result := srv.cachedQuick("client")
	if result.Checked {
		t.Fatal("unchecked files reported intact")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	for i := 0; i < 10; i++ {
		srv.cachedQuick("client")
	}
	select {
	case <-started:
		t.Fatal("duplicate disk scan")
	default:
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for !srv.cachedQuick("client").Checked {
		if time.Now().After(deadline) {
			t.Fatal("result was not published")
		}
		time.Sleep(time.Millisecond)
	}
	if !srv.cachedQuick("client").OK {
		t.Fatal("intact result lost")
	}
	select {
	case <-started:
		t.Fatal("cached result scanned again")
	default:
	}
}

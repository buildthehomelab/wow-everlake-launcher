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
	post(t, ts.URL+"/run?name=play")
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

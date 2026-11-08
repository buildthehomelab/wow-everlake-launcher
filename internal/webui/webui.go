// Package webui serves a small page on the local machine so the launcher can be driven with buttons instead of
// typed commands. It runs Install, Verify and Play and streams their log lines live. It listens only on the
// loopback address and does nothing a command line couldn't; it is not a server for other machines.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
)

//go:embed page.html
var files embed.FS

// Actions are the launcher jobs the page triggers. Each gets a log function to stream lines to the page; lines
// logged after a job ends (the running game's patches) still reach the page. Play gets what the player typed. They
// are injected so the package needs no network of its own and can be tested with fakes.
type Actions struct {
	Install func(ctx context.Context, log func(string)) error
	Verify  func(ctx context.Context, log func(string)) error
	Play    func(ctx context.Context, in PlayInput, log func(string)) error
	// Realm reports the saved realm settings, to fill in the page's sign-in fields.
	Realm func() RealmStatus
}

// PlayInput is the sign-in the page sends with Play. An empty LoginHost starts the game without joining a realm.
type PlayInput struct {
	LoginHost string `json:"login_host"`
	Email     string `json:"email"`
	Password  string `json:"password"`
}

// RealmStatus is what the page shows about joining a realm.
type RealmStatus struct {
	LoginHost string `json:"login_host"`
	Email     string `json:"email"`
	Supported bool   `json:"supported"` // false where the launcher can't join a realm (macOS for now)
}

// Server is the launcher's local page.
type Server struct {
	dir     string
	version string
	act     Actions

	mu   sync.Mutex
	busy string // the running job's name, or ""

	submu    sync.Mutex
	subs     map[chan string]struct{}
	history  []string  // recent lines, replayed to a page that opens mid-job
	lastSeen time.Time // when the last page stopped listening (or the server started)
}

// New makes a server for one install folder.
func New(dir, version string, act Actions) *Server {
	return &Server{dir: dir, version: version, act: act, subs: map[chan string]struct{}{}, lastSeen: time.Now()}
}

// Unwatched reports whether no page has been open for at least d and no job is running: a launcher shown in the
// browser, with no terminal to stop it from, ends then. A page holds its log stream open while it's showing and
// reconnects within seconds when it drops.
func (s *Server) Unwatched(d time.Duration) bool {
	s.mu.Lock()
	busy := s.busy != ""
	s.mu.Unlock()
	s.submu.Lock()
	defer s.submu.Unlock()
	return !busy && len(s.subs) == 0 && time.Since(s.lastSeen) >= d
}

// Handler is the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/status", s.status)
	mux.HandleFunc("/run", s.run)
	mux.HandleFunc("/events", s.events)
	return localOnly(mux)
}

// localOnly turns away requests another web page could make: a Host that isn't a loopback name (DNS rebinding) and
// a request from a page of another origin.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "this page answers on the loopback address only", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "requests from other pages are refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, _ := files.ReadFile("page.html")
	w.Write(data)
}

type statusResp struct {
	Dir       string      `json:"dir"`
	Version   string      `json:"version"`
	Installed bool        `json:"installed"`
	Busy      string      `json:"busy"`
	Realm     RealmStatus `json:"realm"`
	Quick     struct {
		Checked bool   `json:"checked"`
		OK      bool   `json:"ok"`
		Changed int    `json:"changed"`
		Missing int    `json:"missing"`
		Note    string `json:"note"`
	} `json:"quick"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var resp statusResp
	resp.Dir, resp.Version = s.dir, s.version
	s.mu.Lock()
	resp.Busy = s.busy
	s.mu.Unlock()
	if s.act.Realm != nil {
		resp.Realm = s.act.Realm()
	}
	if _, err := install.ReadState(s.dir); err == nil {
		resp.Installed = true
		if rep, err := install.QuickVerify(s.dir); err == nil {
			resp.Quick.Checked = true
			resp.Quick.OK = !rep.Failed()
			resp.Quick.Changed, resp.Quick.Missing = len(rep.Bad), len(rep.Missing)
		} else {
			resp.Quick.Note = err.Error()
		}
	}
	writeJSON(w, resp)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name != "install" && name != "verify" && name != "play" {
		http.Error(w, "unknown job", http.StatusBadRequest)
		return
	}
	var in PlayInput
	if name == "play" {
		// JSON only: a browser won't send that cross-origin without asking first, unlike a form or text/plain post.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "play takes application/json", http.StatusUnsupportedMediaType)
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "bad sign-in: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	if s.busy != "" {
		busy := s.busy
		s.mu.Unlock()
		http.Error(w, busy+" is already running", http.StatusConflict)
		return
	}
	s.busy = name
	s.mu.Unlock()

	s.submu.Lock()
	s.history = nil // a fresh job starts a fresh log
	s.submu.Unlock()
	s.broadcast(fmt.Sprintf("--- %s ---", name))

	go func() {
		err := s.do(name, in)
		if err != nil {
			s.broadcast("error: " + err.Error())
		}
		s.broadcast("=== done ===")
		s.mu.Lock()
		s.busy = ""
		s.mu.Unlock()
	}()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) do(name string, in PlayInput) error {
	ctx := context.Background()
	switch name {
	case "install":
		if s.act.Install == nil {
			return fmt.Errorf("install isn't available")
		}
		return s.act.Install(ctx, s.broadcast)
	case "verify":
		if s.act.Verify == nil {
			return fmt.Errorf("verify isn't available")
		}
		return s.act.Verify(ctx, s.broadcast)
	case "play":
		if s.act.Play == nil {
			return fmt.Errorf("play isn't available")
		}
		return s.act.Play(ctx, in, s.broadcast)
	}
	return nil
}

// events streams log lines as Server-Sent Events: the job's lines so far, then each new one.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan string, 256)
	s.submu.Lock()
	past := append([]string(nil), s.history...)
	s.subs[ch] = struct{}{}
	s.submu.Unlock()
	defer func() {
		s.submu.Lock()
		delete(s.subs, ch)
		s.lastSeen = time.Now()
		s.submu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	for _, line := range past {
		writeSSE(w, line)
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			writeSSE(w, line)
			flusher.Flush()
		case <-time.After(20 * time.Second):
			fmt.Fprint(w, ": ping\n\n") // keep the connection open through proxies
			flusher.Flush()
		}
	}
}

// broadcast records a line and sends it to every open page.
func (s *Server) broadcast(line string) {
	s.submu.Lock()
	if len(s.history) < 5000 {
		s.history = append(s.history, line)
	}
	for ch := range s.subs {
		select {
		case ch <- line:
		default: // a slow page is skipped rather than blocking the job
		}
	}
	s.submu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w http.ResponseWriter, line string) {
	b, _ := json.Marshal(line)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

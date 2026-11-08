// Package webui serves a small page on the local machine so the launcher can be driven with buttons instead of
// typed commands. It runs Install, Verify and Play and streams their log lines live. It listens only on the
// loopback address and does nothing a command line couldn't; it is not a server for other machines.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
)

//go:embed page.html
var files embed.FS

// Actions are the launcher jobs the page triggers. Install and Verify get a log function to stream lines to the
// page; Play starts the game and returns its process id. They are injected so the package needs no network of its
// own and can be tested with fakes.
type Actions struct {
	Install func(ctx context.Context, log func(string)) error
	Verify  func(ctx context.Context, log func(string)) error
	Play    func() (int, error)
}

// Server is the launcher's local page.
type Server struct {
	dir     string
	version string
	act     Actions

	mu   sync.Mutex
	busy string // the running job's name, or ""

	submu   sync.Mutex
	subs    map[chan string]struct{}
	history []string // recent lines, replayed to a page that opens mid-job
}

// New makes a server for one install folder.
func New(dir, version string, act Actions) *Server {
	return &Server{dir: dir, version: version, act: act, subs: map[chan string]struct{}{}}
}

// Handler is the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/status", s.status)
	mux.HandleFunc("/run", s.run)
	mux.HandleFunc("/events", s.events)
	return mux
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
	Dir       string `json:"dir"`
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Busy      string `json:"busy"`
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
		err := s.do(name)
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

func (s *Server) do(name string) error {
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
		pid, err := s.act.Play()
		if err != nil {
			return err
		}
		s.broadcast(fmt.Sprintf("started the game (process %d)", pid))
		return nil
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

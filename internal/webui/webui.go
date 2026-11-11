// Package webui serves a small page on the local machine so the launcher can be driven with buttons instead of
// typed commands. It runs Install, Verify and Play and streams their log lines live. It listens only on the
// loopback address and does nothing a command line couldn't; it is not a server for other machines.
package webui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/buildthehomelab/wow-everlake-launcher/internal/install"
)

//go:embed page.html
var files embed.FS

// Actions are the launcher jobs the page triggers. Each gets the install folder as it was when the job started and
// a log function to stream lines to the page; lines logged after a job ends (the running game's patches) still
// reach the page. Play gets what the player typed. They are injected so the package needs no network of its own and
// can be tested with fakes.
type Actions struct {
	EraMaps func(ctx context.Context, dir string, log func(string)) error
	Install func(ctx context.Context, dir string, log func(string)) error
	Verify  func(ctx context.Context, dir string, log func(string)) error
	Play    func(ctx context.Context, dir string, in PlayInput, log func(string)) error
	// Realm reports the realm settings saved in an install folder, to fill in the page's sign-in fields.
	Realm func(dir string) RealmStatus

	// PickFolder shows the system's folder dialog, opened at start, and reports cancelled when the player closes it
	// without picking. Nil means there's no dialog and the page can only take a typed path.
	PickFolder func(start string) (picked string, cancelled bool, err error)
	// UseFolder turns a picked or typed folder into the install folder (see settings.ChooseInstallDir); inside
	// reports that it made a folder inside the one picked. SaveFolder keeps the choice for next time, once the
	// launcher has moved to it.
	UseFolder  func(picked string) (dir string, inside bool, err error)
	SaveFolder func(dir string) error
	// Installed reports whether a folder holds a finished install; nil means install.ReadState.
	Installed func(dir string) bool

	// Look reports the page's saved look and SaveLook keeps a change to it for next time. Nil means the page's own
	// choices last only as long as it's open.
	Look     func() Look
	SaveLook func(Look) error

	// LauncherVersion is the launcher's own version, shown on the page.
	LauncherVersion string
}

// PlayInput is the sign-in the page sends with Play. An empty LoginHost starts the game without joining a realm.
type PlayInput struct {
	LoginHost string `json:"login_host"`
	Email     string `json:"email"`
	Password  string `json:"password"` // empty: use the remembered one
	Remember  bool   `json:"remember"` // keep the password once it signs in; false forgets a remembered one
}

// RealmStatus is what the page shows about joining a realm.
type RealmStatus struct {
	LoginHost string `json:"login_host"`
	Email     string `json:"email"`
	Supported bool   `json:"supported"` // false where the launcher can't join a realm (macOS for now)
	// CanRemember: a password can be remembered here. PasswordSaved: one is, for LoginHost and Email. The password
	// itself never goes to the page.
	CanRemember   bool `json:"can_remember"`
	PasswordSaved bool `json:"password_saved"`
}

// Look is how the page looks: its colour (a school of magic) and whether its art moves. The page itself lives at a
// new address each launch, so its own storage doesn't last; the launcher keeps these instead.
type Look struct {
	School string `json:"school,omitempty"` // empty: a different one each launch
	// Animation false keeps the art still; nil follows the system's "reduce motion" setting.
	Animation *bool `json:"animation,omitempty"`
}

// schoolName is a colour name the page could have sent: it goes into the page's HTML, so nothing else is kept.
var schoolName = regexp.MustCompile(`^[a-z]{1,16}$`)

// Server is the launcher's local page.
type Server struct {
	dir     string
	version string
	act     Actions

	mu     sync.Mutex
	busy   string // the running job's name, or ""
	notice string // something the launcher wants the player to know about itself (an update it can't do)

	lookMu sync.Mutex // one look change at a time, so two quick clicks don't lose one

	submu    sync.Mutex
	subs     map[chan string]struct{}
	history  []string  // recent lines, replayed to a page that opens mid-job
	lastSeen time.Time // when the last page stopped listening (or the server started)
}

// New makes a server, starting on one install folder; the page can move it to another (POST /folder).
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
	mux.HandleFunc("/diagnostics", s.diagnostics)
	mux.HandleFunc("/run", s.run)
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/folder", s.folder)
	mux.HandleFunc("/look", s.look)
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
	// The saved look goes on <body> before the page draws anything, so it doesn't start moving (or in another
	// colour) and then change once it asks.
	if s.act.Look != nil {
		look := s.act.Look()
		attrs := ""
		if schoolName.MatchString(look.School) {
			attrs += ` data-saved-school="` + look.School + `"`
		}
		if look.Animation != nil {
			attrs += fmt.Sprintf(` data-animation="%t"`, *look.Animation)
		}
		data = bytes.Replace(data, []byte("<body "), []byte("<body"+attrs+" "), 1)
	}
	w.Write(data)
}

// look keeps a change to the page's look (POST, JSON with the fields that changed).
func (s *Server) look(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "look takes application/json", http.StatusUnsupportedMediaType)
		return
	}
	var change Look
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&change); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if change.School != "" && !schoolName.MatchString(change.School) {
		http.Error(w, "unknown colour", http.StatusBadRequest)
		return
	}
	if s.act.Look == nil || s.act.SaveLook == nil {
		w.WriteHeader(http.StatusNoContent) // nothing to keep it in; the page still changes while it's open
		return
	}
	s.lookMu.Lock()
	defer s.lookMu.Unlock()
	look := s.act.Look()
	if change.School != "" {
		look.School = change.School
	}
	if change.Animation != nil {
		look.Animation = change.Animation
	}
	if err := s.act.SaveLook(look); err != nil {
		http.Error(w, "couldn't save it: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type statusResp struct {
	Dir       string      `json:"dir"`
	CanPick   bool        `json:"can_pick"` // the page may offer the system's folder dialog
	Version   string      `json:"version"`
	Launcher  string      `json:"launcher,omitempty"`
	Notice    string      `json:"notice,omitempty"`
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
	s.mu.Lock()
	resp.Dir, resp.Busy, resp.Notice = s.dir, s.busy, s.notice
	s.mu.Unlock()
	resp.Version, resp.Launcher = s.version, s.act.LauncherVersion
	resp.CanPick = s.act.PickFolder != nil
	if s.act.Realm != nil {
		resp.Realm = s.act.Realm(resp.Dir)
	}
	if _, err := install.ReadState(resp.Dir); err == nil {
		resp.Installed = true
		if rep, err := install.QuickVerify(resp.Dir); err == nil {
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
	if name != "install" && name != "verify" && name != "play" && name != "era-maps" {
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
	if busy, ok := s.begin(name, func(dir string) error { return s.do(name, dir, in) }); !ok {
		http.Error(w, busy+" is already running", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// Begin runs job in the background as the named job, the way the page's buttons start theirs: the page shows it
// running with its log lines, and nothing else starts until it ends. The launcher uses it for jobs of its own (its
// update). It reports false, starting nothing, while another job is running.
func (s *Server) Begin(name string, job func(log func(string)) error) bool {
	_, ok := s.begin(name, func(string) error { return job(s.broadcast) })
	return ok
}

// SetNotice shows a line about the launcher itself on the page (an update it can't do by itself); "" clears it.
func (s *Server) SetNotice(text string) {
	s.mu.Lock()
	s.notice = text
	s.mu.Unlock()
}

// Busy names the running job, or is "".
func (s *Server) Busy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// begin marks the server busy with name and runs job (given the install folder) in the background; it reports the
// running job instead when there is one.
func (s *Server) begin(name string, job func(dir string) error) (string, bool) {
	s.mu.Lock()
	if s.busy != "" {
		busy := s.busy
		s.mu.Unlock()
		return busy, false
	}
	s.busy = name
	dir := s.dir
	s.mu.Unlock()

	s.submu.Lock()
	s.history = nil // a fresh job starts a fresh log
	s.submu.Unlock()
	s.broadcast(fmt.Sprintf("--- %s ---", name))

	go func() {
		err := job(dir)
		if err != nil {
			s.broadcast("error: " + err.Error())
		}
		s.broadcast("=== done ===")
		s.mu.Lock()
		s.busy = ""
		s.mu.Unlock()
	}()
	return "", true
}

func (s *Server) do(name, dir string, in PlayInput) error {
	ctx := context.Background()
	switch name {
	case "era-maps":
		if s.act.EraMaps == nil {
			return fmt.Errorf("Era map downloads are unavailable")
		}
		return s.act.EraMaps(ctx, dir, s.broadcast)
	case "install":
		if s.act.Install == nil {
			return fmt.Errorf("install isn't available")
		}
		return s.act.Install(ctx, dir, s.broadcast)
	case "verify":
		if s.act.Verify == nil {
			return fmt.Errorf("verify isn't available")
		}
		return s.act.Verify(ctx, dir, s.broadcast)
	case "play":
		if s.act.Play == nil {
			return fmt.Errorf("play isn't available")
		}
		return s.act.Play(ctx, dir, in, s.broadcast)
	}
	return nil
}

// folderReq is a change of install folder: a typed path, or Browse to ask the system's folder dialog.
type folderReq struct {
	Dir    string `json:"dir"`
	Browse bool   `json:"browse"`
}

type folderResp struct {
	Dir       string `json:"dir"`
	Cancelled bool   `json:"cancelled,omitempty"`
	Note      string `json:"note,omitempty"`
}

// folder moves the launcher to another install folder. Nothing is copied: a folder that already holds the game is
// used as it is, and anywhere else the next Install downloads it there.
func (s *Server) folder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "folder takes application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req folderReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.act.UseFolder == nil {
		http.Error(w, "the install folder can't be changed here", http.StatusNotImplemented)
		return
	}
	s.mu.Lock()
	old, busy := s.dir, s.busy
	s.mu.Unlock()
	if busy != "" {
		http.Error(w, "wait for "+busy+" to finish before changing the folder", http.StatusConflict)
		return
	}
	picked := strings.TrimSpace(req.Dir)
	if req.Browse {
		if s.act.PickFolder == nil {
			http.Error(w, "no folder dialog here; type the folder instead", http.StatusNotImplemented)
			return
		}
		p, cancelled, err := s.act.PickFolder(old)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if cancelled {
			writeJSON(w, folderResp{Dir: old, Cancelled: true})
			return
		}
		picked = p
	}
	// The current folder picked again stays as it is, even half downloaded (its install record is written last).
	if picked != "" && filepath.Clean(picked) == filepath.Clean(old) {
		writeJSON(w, folderResp{Dir: old})
		return
	}
	dir, inside, err := s.act.UseFolder(picked)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.busy != "" { // a job started while the dialog was open
		busy := s.busy
		s.mu.Unlock()
		http.Error(w, "wait for "+busy+" to finish before changing the folder", http.StatusConflict)
		return
	}
	s.dir = dir
	s.mu.Unlock()

	resp := folderResp{Dir: dir}
	if s.act.SaveFolder != nil {
		if err := s.act.SaveFolder(dir); err != nil {
			resp.Note = "The launcher uses this folder now, but couldn't save it for next time: " + err.Error() + "."
		}
	}
	installed := s.act.Installed
	if installed == nil {
		installed = func(d string) bool { _, err := install.ReadState(d); return err == nil }
	}
	note := ""
	switch {
	case installed(dir):
		note = "Found the game already installed here."
	case inside:
		note = "That folder has other things in it, so the game goes in its own folder inside it."
	}
	if note != "" {
		resp.Note = strings.TrimSpace(note + " " + resp.Note)
	}
	if dir != old && installed(old) && !installed(dir) {
		if resp.Note != "" {
			resp.Note += " "
		}
		resp.Note += "The copy in " + old + " stays where it is: move its contents here to keep it, or Install " +
			"downloads the game again (delete the old folder afterwards to free the space)."
	}
	s.broadcast("install folder: " + dir)
	writeJSON(w, resp)
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

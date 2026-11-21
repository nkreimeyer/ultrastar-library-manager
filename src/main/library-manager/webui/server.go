// Package webui serves the browser UI of library-manager on a loopback address.
//
// The UI can move and rewrite files, so every request is checked: the Host must be the
// address the server listens on (no DNS rebinding), the session cookie must be set (it is
// handed out for the secret token in the start URL, which works once), and a request that changes
// anything must be a same-origin JSON POST (no cross-site forms).
//
// Only one job (an analysis, an apply or an undo) runs at a time: the modules log through
// the global logrus logger, and the job's log is what the hook collects while it runs.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

//go:embed static
var staticFiles embed.FS

// maxBody limits JSON request bodies.
const maxBody = 4 << 20

// Server is the UI server for one listening address.
type Server struct {
	addr    string // host:port the server listens on
	token   string // secret of the start URL
	session string // value of the session cookie
	// cookie is the name of the session cookie: "lm_session_<port>". Browsers keep cookies
	// per host, not per port, so a second UI on another port would replace a shared one.
	cookie  string
	version string
	mux     *http.ServeMux
	jobs    *jobRunner
	// closed is closed by Close: the event streams end, so http.Server.Shutdown does not
	// wait for them.
	closed    chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	tokenUsed bool // the start URL signed a browser in already
	organize  map[string]*organizeAnalysis
	removal   *removeAnalysis // the newest remove check
	checkRes  *checkAnalysis  // the newest library check
	nextID    int
}

// New returns a server for the loopback address addr ("127.0.0.1:8080"). version is shown
// in the UI.
func New(addr, version string) (*Server, error) {
	if err := CheckAddr(addr); err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(addr)
	s := &Server{
		addr: addr, token: randomHex(16), session: randomHex(32), cookie: "lm_session_" + port, version: version,
		mux: http.NewServeMux(), jobs: newJobRunner(), closed: make(chan struct{}),
		organize: map[string]*organizeAnalysis{},
	}
	s.routes()
	return s, nil
}

// CheckAddr reports whether addr is an address the UI may listen on: a loopback IP and a
// port (0 = any free one).
func CheckAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the UI only listens on a loopback address (127.0.0.1 or ::1), not %q", host)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	return nil
}

// StartURL is the URL to open: it carries the token that sets the session cookie. The
// token works once: a link that leaked (browser history, the command line of the browser
// start) signs no one else in.
func (s *Server) StartURL() string {
	return "http://" + s.addr + "/?token=" + s.token
}

// Close cancels a running job, waits for it to end, then ends the event streams (each sends
// the job's final state first, so an open page shows that it stopped).
func (s *Server) Close() {
	s.jobs.close()
	s.closeOnce.Do(func() { close(s.closed) })
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b)
}

func (s *Server) routes() {
	static, _ := fs.Sub(staticFiles, "static")
	files := http.FileServer(http.FS(static))
	s.mux.Handle("GET /", files)
	s.mux.HandleFunc("GET /api/system", s.handleSystem)
	s.mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	s.mux.HandleFunc("GET /api/job", s.handleJob)
	s.mux.HandleFunc("GET /api/job/events", s.handleJobEvents)
	s.mux.HandleFunc("POST /api/job/cancel", s.handleJobCancel)
	s.mux.HandleFunc("POST /api/organize/validate", s.handleOrganizeValidate)
	s.mux.HandleFunc("POST /api/reports/list", s.handleReportsList)
	s.mux.HandleFunc("POST /api/reports/read", s.handleReportsRead)
	s.mux.HandleFunc("POST /api/setaside/validate", s.handleSetAsideValidate)
	s.mux.HandleFunc("POST /api/setaside/list", s.handleSetAsideList)
	s.mux.HandleFunc("POST /api/setaside/restore", s.handleSetAsideRestore)
	s.mux.HandleFunc("POST /api/check/validate", s.handleCheckValidate)
	s.mux.HandleFunc("POST /api/check/analyse", s.handleCheckAnalyse)
	s.mux.HandleFunc("GET /api/check/analysis", s.handleCheckAnalysis)
	s.mux.HandleFunc("POST /api/check/apply", s.handleCheckApply)
	s.mux.HandleFunc("POST /api/remove/validate", s.handleRemoveValidate)
	s.mux.HandleFunc("POST /api/remove/analyse", s.handleRemoveAnalyse)
	s.mux.HandleFunc("GET /api/remove/analysis", s.handleRemoveAnalysis)
	s.mux.HandleFunc("POST /api/remove/apply", s.handleRemoveApply)
	s.mux.HandleFunc("POST /api/organize/analyse", s.handleOrganizeAnalyse)
	s.mux.HandleFunc("GET /api/organize/analysis", s.handleOrganizeAnalysis)
	s.mux.HandleFunc("POST /api/organize/exclude", s.handleOrganizeExclude)
	s.mux.HandleFunc("POST /api/organize/join", s.handleOrganizeJoin)
	s.mux.HandleFunc("POST /api/organize/apply", s.handleOrganizeApply)
	s.mux.HandleFunc("POST /api/organize/history", s.handleOrganizeHistory)
	s.mux.HandleFunc("POST /api/organize/undo", s.handleOrganizeUndo)
}

// ServeHTTP checks the Host, the session and (for POST) the origin, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostOK(r.Host) {
		http.Error(w, "wrong host", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/" && r.URL.Query().Has("token") {
		if !s.signedIn(r) { // a signed-in browser opening the link again is only redirected
			if err := s.useToken(r.URL.Query().Get("token")); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		http.Redirect(w, r, "/", http.StatusSeeOther) // the token leaves the address bar
		return
	}
	if !s.signedIn(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "Not signed in. Open the address library-manager printed when it started (it contains ?token=…). "+
			"It works once: if this browser lost its sign-in, restart library-manager and open the new address.\n")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "cross-origin request", http.StatusForbidden)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "requests must be JSON", http.StatusUnsupportedMediaType)
			return
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

// signedIn reports whether r carries the session cookie.
func (s *Server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(s.cookie)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.session)) == 1
}

// useToken accepts the start URL's token once.
func (s *Server) useToken(token string) error {
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return errors.New("wrong token: open the link that library-manager printed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenUsed {
		return errors.New("this link signed a browser in already and works only once: restart library-manager for a new one")
	}
	s.tokenUsed = true
	return nil
}

// hostOK accepts the listening address and its "localhost" spelling.
func (s *Server) hostOK(host string) bool {
	if host == s.addr {
		return true
	}
	_, port, _ := net.SplitHostPort(s.addr)
	return host == "localhost:"+port
}

// apiError is the JSON body of a failed API call.
type apiError struct {
	Error string `json:"error"`
	// Details are extra lines, e.g. how a plan changed.
	Details []string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Debugf("webui: writing a response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, apiError{Error: err.Error()})
}

// readJSON decodes the request body into v; unknown fields are an error.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return false
	}
	return true
}

// errBusy is returned when a job is started while another one runs.
var errBusy = errors.New("another job is running; wait for it or cancel it first")

func (s *Server) newID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return fmt.Sprintf("%s%d", prefix, s.nextID)
}

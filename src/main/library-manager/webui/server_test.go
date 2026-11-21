package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/utils"
)

const R = "�"

// testServer is a running UI server and a signed-in client.
type testServer struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	client *http.Client
	base   string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	t.Setenv("PATH", t.TempDir()) // no ffmpeg/ffprobe: media are not probed
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(ln.Addr().String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv)
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})
	jar, _ := cookiejar.New(nil)
	s := &testServer{t: t, srv: srv, ts: ts, client: &http.Client{Jar: jar, Timeout: 30 * time.Second}, base: "http://" + ln.Addr().String()}
	resp, err := s.client.Get(srv.StartURL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.RawQuery != "" {
		t.Fatalf("sign-in: %d %s", resp.StatusCode, resp.Request.URL)
	}
	return s
}

func (s *testServer) do(method, path string, body any, out any) int {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, s.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", s.base)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			s.t.Fatalf("%s %s: %v\n%s", method, path, err, data)
		}
	}
	return resp.StatusCode
}

// wait polls the job until it is no longer running.
func (s *testServer) wait() Job {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var j Job
		s.do("GET", "/api/job", nil, &j)
		if j.State != stateRunning {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatal("the job did not end")
	return Job{}
}

// The first use of the token sets the session cookie, named after the port, and redirects
// to the plain address.
func TestSignIn(t *testing.T) {
	srv, err := New("127.0.0.1:4711", "")
	if err != nil {
		t.Fatal(err)
	}
	signIn := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", srv.StartURL(), nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	rec := signIn()
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(cookies) != 1 {
		t.Fatalf("token: %d %q %v", rec.Code, rec.Header().Get("Location"), cookies)
	}
	if c := cookies[0]; c.Name != "lm_session_4711" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value != srv.session {
		t.Errorf("cookie = %+v", c)
	}
	if rec := signIn(); rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
		t.Errorf("second use: %d %v", rec.Code, rec.Result().Cookies())
	}
	// Another instance's cookie (another port) does not sign in.
	req := httptest.NewRequest("GET", "http://127.0.0.1:4711/api/system", nil)
	req.AddCookie(&http.Cookie{Name: "lm_session_4712", Value: srv.session})
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cookie of another port: %d", rec.Code)
	}
}

func TestNewLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:80", "192.168.1.2:80", "example.com:80", "nonsense", "127.0.0.1:99999", "127.0.0.1:http", "127.0.0.1:-1"} {
		if _, err := New(addr, ""); err == nil {
			t.Errorf("New(%q) accepted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:8080"} {
		if _, err := New(addr, ""); err != nil {
			t.Errorf("New(%q): %v", addr, err)
		}
	}
	log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
}

func TestSecurity(t *testing.T) {
	s := newTestServer(t)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(c *http.Client, req *http.Request) int {
		t.Helper()
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	get := func(path string) *http.Request { r, _ := http.NewRequest("GET", s.base+path, nil); return r }

	for _, p := range []string{"/", "/api/system", "/api/job", "/?token=wrong"} {
		if got := status(anon, get(p)); got != http.StatusForbidden {
			t.Errorf("anonymous GET %s = %d, want 403", p, got)
		}
	}
	// The token works once: newTestServer signed its client in with it.
	resp, err := anon.Get(s.srv.StartURL())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 || !strings.Contains(string(body), "only once") {
		t.Errorf("used token: %d %v %s", resp.StatusCode, resp.Cookies(), body)
	}
	// A signed-in browser opening the link again is only redirected.
	resp, err = s.client.Get(s.srv.StartURL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.RawQuery != "" {
		t.Errorf("signed-in browser with the link: %d %s", resp.StatusCode, resp.Request.URL)
	}

	// DNS rebinding: another host name is refused even with the cookie.
	req := get("/api/system")
	req.Host = "evil.example:" + strings.Split(s.srv.addr, ":")[1]
	req.AddCookie(&http.Cookie{Name: s.srv.cookie, Value: s.srv.session})
	if got := status(anon, req); got != http.StatusForbidden {
		t.Errorf("wrong Host = %d", got)
	}
	// "localhost" is the same server (a browser keeps its cookie apart from 127.0.0.1's).
	req = get("/api/system")
	req.Host = "localhost:" + strings.Split(s.srv.addr, ":")[1]
	req.AddCookie(&http.Cookie{Name: s.srv.cookie, Value: s.srv.session})
	if got := status(anon, req); got != http.StatusOK {
		t.Errorf("localhost Host = %d", got)
	}

	post := func(origin, ctype string) int {
		req, _ := http.NewRequest("POST", s.base+"/api/job/cancel", strings.NewReader("{}"))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		return status(s.client, req)
	}
	if got := post("http://evil.example", "application/json"); got != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d", got)
	}
	if got := post(s.base, "text/plain"); got != http.StatusUnsupportedMediaType {
		t.Errorf("form POST = %d", got)
	}
	if got := post(s.base, "application/json"); got != http.StatusOK {
		t.Errorf("same-origin POST = %d", got)
	}
	if got := post("", "application/json"); got != http.StatusOK {
		t.Errorf("POST without Origin = %d", got)
	}

	// Signed in: the page and the API work, with protective headers.
	resp, err = s.client.Get(s.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") ||
		resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("index: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestSystem(t *testing.T) {
	s := newTestServer(t)
	var info systemInfo
	if got := s.do("GET", "/api/system", nil, &info); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if info.Version != "test" || info.ToolsOK || len(info.Tools) != 2 || info.Tools[0].Found || info.InstallHint == "" || info.MinVersion != "5.1" {
		t.Errorf("system = %+v", info)
	}
}

func TestFSList(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	for _, d := range []string{"b", "A", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "b"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	var l dirListing
	s.do("GET", "/api/fs/list?path="+dir, nil, &l)
	var names []string
	for _, d := range l.Dirs {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "A,b,link" || l.Parent != filepath.Dir(dir) || l.Error != "" || len(l.Roots) == 0 {
		t.Errorf("listing = %+v", l)
	}
	s.do("GET", "/api/fs/list?hidden=1&path="+dir, nil, &l)
	if len(l.Dirs) != 4 {
		t.Errorf("with hidden: %+v", l.Dirs)
	}
	s.do("GET", "/api/fs/list?path="+filepath.Join(dir, "missing"), nil, &l)
	if l.Error == "" || len(l.Dirs) != 0 {
		t.Errorf("missing folder: %+v", l)
	}
}

func TestJobsBusyCancelAndLogs(t *testing.T) {
	s := newTestServer(t)
	started := make(chan struct{})
	_, err := s.srv.jobs.start("x1", "test", fsops.New(true), func(ctx context.Context, c jobControl) (any, error) {
		c.Progress("work", 1, 3)
		log.Infof("first line")
		log.Warnf("second line")
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := s.srv.jobs.start("x2", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { return nil, nil }); err != errBusy {
		t.Errorf("second job: %v, want errBusy", err)
	}
	// The event stream sends the job and its log lines.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := readEvents(t, resp.Body, func(evs []event) bool {
		for _, e := range evs {
			if e.name == "log" {
				return true
			}
		}
		return false
	})
	var j Job
	var lines []LogLine
	for _, e := range events {
		switch e.name {
		case "job":
			json.Unmarshal([]byte(e.data), &j)
		case "log":
			json.Unmarshal([]byte(e.data), &lines)
		}
	}
	if j.ID != "x1" || j.State != stateRunning || j.Phase != "work" || j.Done != 1 || j.Total != 3 || !j.DryRun {
		t.Errorf("job = %+v", j)
	}
	var msgs []string
	for _, l := range lines {
		msgs = append(msgs, l.Level+":"+l.Msg)
	}
	if strings.Join(msgs, "|") != "info:first line|warning:second line" {
		t.Errorf("log = %v", msgs)
	}
	var res map[string]bool
	s.do("POST", "/api/job/cancel", struct{}{}, &res)
	if !res["cancelled"] {
		t.Error("cancel found no job")
	}
	if j := s.wait(); j.State != stateCancelled || j.Ended.IsZero() {
		t.Errorf("after cancel: %+v", j)
	}
	s.do("POST", "/api/job/cancel", struct{}{}, &res)
	if res["cancelled"] {
		t.Error("cancel of a finished job")
	}
	// A panicking job fails instead of taking the UI down.
	s.srv.jobs.start("x3", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { panic("boom") })
	if j := s.wait(); j.State != stateFailed || !strings.Contains(j.Error, "boom") {
		t.Errorf("panic: %+v", j)
	}
	// A panic in a module's worker pool costs only that item: it is counted as a failure
	// and the job finishes.
	ran := 0
	s.srv.jobs.start("x4", "test", fsops.New(true), func(ctx context.Context, c jobControl) (any, error) {
		ops := c.j.ops
		err := utils.ForEachParallel(ctx, []int{0, 1, 2}, utils.Parallel{Workers: 1, OnPanic: func(int, any) { ops.Fail() }}, func(i, _ int) {
			if i == 1 {
				panic("worker boom")
			}
			ran++
		})
		return nil, err
	})
	if j := s.wait(); j.State != stateDone || j.Stats.Failed != 1 || ran != 2 {
		t.Errorf("panic in a pool: %+v, %d other items ran", j, ran)
	}
}

type event struct{ name, data string }

// readEvents reads SSE events until done says so.
func readEvents(t *testing.T, r io.Reader, done func([]event) bool) []event {
	t.Helper()
	return scanEvents(t, bufio.NewScanner(r), done)
}

// scanEvents is readEvents on a scanner that can be read on afterwards.
func scanEvents(t *testing.T, sc *bufio.Scanner, done func([]event) bool) []event {
	t.Helper()
	var evs []event
	var cur event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "" && cur.name != "":
			evs = append(evs, cur)
			cur = event{}
			if done(evs) {
				return evs
			}
		}
	}
	t.Fatalf("event stream ended: %v (%v)", sc.Err(), evs)
	return nil
}

// notesTxt is a song txt with 10 notes sung to lyric (different lyrics, different content).
func notesTxt(artist, title, lyric string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#ARTIST:%s\n#TITLE:%s\n#MP3:song.mp3\n#BPM:300\n#GAP:0\n", artist, title)
	for i := range 10 {
		fmt.Fprintf(&b, ": %d 2 0 %s\n", i*4, lyric)
	}
	b.WriteString("E\n")
	return b.String()
}

func writeSong(t *testing.T, dir, artist, title string, extra map[string]string) {
	t.Helper()
	files := map[string]string{"song.txt": notesTxt(artist, title, artist+" "+title), "song.mp3": "audio " + dir}
	for k, v := range extra {
		files[k] = v
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// tree lists the files below root with their content.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			m[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	})
	return m
}

func equalTrees(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

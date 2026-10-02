package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"depot/internal/testdir"
)

type fakeProc struct {
	exit   chan error
	killed atomic.Bool
}

func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) Kill() error {
	if p.killed.CompareAndSwap(false, true) {
		p.exit <- errors.New("killed")
	}
	return nil
}

type starts struct {
	mu      sync.Mutex
	procs   []*fakeProc
	configs []string
}

func (s *starts) start(ctx context.Context, binary, config string) (Process, error) {
	data, err := os.ReadFile(config)
	if err != nil {
		return nil, err
	}
	p := &fakeProc{exit: make(chan error, 2)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs = append(s.procs, p)
	s.configs = append(s.configs, string(data))
	return p, nil
}

func (s *starts) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *starts) proc(i int) *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[i]
}

func (s *starts) config(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configs[i]
}

// waits records the waits between restarts. Each one ends at once unless
// block says otherwise.
type waits struct {
	mu    sync.Mutex
	got   []time.Duration
	block func(d time.Duration) bool
}

func (w *waits) after(d time.Duration) <-chan time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.got = append(w.got, d)
	if w.block != nil && w.block(d) {
		return nil // only a source change ends this wait
	}
	return time.After(0)
}

func (w *waits) list() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.got)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testSupervisor(t *testing.T) (*Supervisor, *starts) {
	t.Helper()
	st := &starts{}
	sup := New(Config{Binary: "go2rtc.exe", DataDir: testdir.New(t)})
	sup.start = st.start
	sup.port = func() (int, error) { return 18555, nil }
	sup.exists = func(string) bool { return true }
	sup.ready = func(context.Context, string, string, string) error { return nil }
	sup.minBackoff = 10 * time.Millisecond
	return sup, st
}

// run starts sup.Run and stops it when the test ends.
func run(t *testing.T, sup *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func running(sup *Supervisor, st *starts, n int) func() bool {
	return func() bool { return st.count() == n && sup.Status().State == StateRunning }
}

func TestSupervisorStartsRestartsAndStopsGo2rtc(t *testing.T) {
	sup, st := testSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	waitFor(t, "off", func() bool { return sup.Status().State == StateOff })
	sup.SetSource("rtsp://admin:pw@cam1:554/h264Preview_01_sub")
	waitFor(t, "first start", running(sup, st, 1))
	if addr, user, pass := sup.target(); addr != "127.0.0.1:18555" || user == "" || pass == "" {
		t.Fatalf("target = %q %q %q", addr, user, pass)
	}

	st.proc(0).exit <- errors.New("crashed")
	waitFor(t, "restart after a crash", running(sup, st, 2))

	sup.SetSource("rtsp://admin:pw@cam2:554/h264Preview_01_sub")
	waitFor(t, "restart with the new source", func() bool { return st.count() == 3 })
	if !st.proc(1).killed.Load() || !strings.Contains(st.config(2), "cam2") {
		t.Fatalf("old process killed %v, new config %s", st.proc(1).killed.Load(), st.config(2))
	}

	sup.SetSource("")
	waitFor(t, "stop", func() bool { return sup.Status().State == StateOff && st.proc(2).killed.Load() })
	if addr, _, _ := sup.target(); addr != "" {
		t.Fatalf("target after stop = %q", addr)
	}
	cancel()
	<-done
}

func TestSupervisorStartsOnceWhenTheSourceIsSetBeforeRun(t *testing.T) {
	sup, st := testSupervisor(t)
	sup.SetSource("rtsp://admin:pw@cam:554/s") // leaves a wake for Run to find
	run(t, sup)
	waitFor(t, "start", running(sup, st, 1))
	time.Sleep(100 * time.Millisecond)
	if n, state := st.count(), sup.Status().State; n != 1 || state != StateRunning || st.proc(0).killed.Load() {
		t.Fatalf("starts %d, state %s, killed %v; want one start that keeps running", n, state, st.proc(0).killed.Load())
	}
}

func TestSupervisorBacksOffDoublingUpToTheCap(t *testing.T) {
	if sup := New(Config{}); sup.minBackoff != time.Second || sup.maxBackoff != 30*time.Second {
		t.Fatalf("default backoff %v to %v; want 1s to 30s", sup.minBackoff, sup.maxBackoff)
	}
	sup, st := testSupervisor(t)
	w := &waits{}
	sup.after = w.after
	sup.maxBackoff = 40 * time.Millisecond
	run(t, sup)
	sup.SetSource("rtsp://admin:pw@cam:554/s")
	for i := range 5 {
		waitFor(t, fmt.Sprintf("start %d", i+1), running(sup, st, i+1))
		st.proc(i).exit <- errors.New("crashed")
	}
	waitFor(t, "start 6", running(sup, st, 6))
	ms := time.Millisecond
	if got, want := w.list(), []time.Duration{10 * ms, 20 * ms, 40 * ms, 40 * ms, 40 * ms}; !slices.Equal(got, want) {
		t.Fatalf("waits = %v; want %v", got, want)
	}
}

func TestSupervisorResetsTheBackoffWhenTheSourceChangesWhileWaiting(t *testing.T) {
	sup, st := testSupervisor(t)
	w := &waits{block: func(d time.Duration) bool { return d == 20*time.Millisecond }}
	sup.after = w.after
	run(t, sup)
	sup.SetSource("rtsp://admin:pw@cam1:554/s")
	waitFor(t, "start 1", running(sup, st, 1))
	st.proc(0).exit <- errors.New("crashed") // waits 10ms
	waitFor(t, "start 2", running(sup, st, 2))
	st.proc(1).exit <- errors.New("crashed") // waits 20ms: blocks
	waitFor(t, "failing", func() bool { return sup.Status().State == StateFailing })

	sup.SetSource("rtsp://admin:pw@cam2:554/s")
	waitFor(t, "start with the new source", running(sup, st, 3))
	if !strings.Contains(st.config(2), "cam2") {
		t.Fatalf("config = %s", st.config(2))
	}
	st.proc(2).exit <- errors.New("crashed")
	waitFor(t, "start 4", running(sup, st, 4))
	ms := time.Millisecond
	if got, want := w.list(), []time.Duration{10 * ms, 20 * ms, 10 * ms}; !slices.Equal(got, want) {
		t.Fatalf("waits = %v; want %v (back to the minimum after the source changed)", got, want)
	}
}

func TestSupervisorRestartsGo2rtcThatDoesNotBecomeReady(t *testing.T) {
	sup, st := testSupervisor(t)
	var calls atomic.Int32
	sup.ready = func(ctx context.Context, addr, user, pass string) error {
		if addr != "127.0.0.1:18555" || user == "" || pass == "" {
			return fmt.Errorf("probe got %q %q %q", addr, user, pass)
		}
		if calls.Add(1) == 1 {
			return errors.New("its API answered 200 without the password; want 401")
		}
		return nil
	}
	run(t, sup)
	sup.SetSource("rtsp://admin:pw@cam:554/s")
	waitFor(t, "second start", running(sup, st, 2))
	if !st.proc(0).killed.Load() {
		t.Fatal("go2rtc that failed the probe was left running")
	}
}

func TestSupervisorIsNotRunningUntilGo2rtcIsReady(t *testing.T) {
	sup, st := testSupervisor(t)
	probing, timeUp := make(chan time.Duration, 1), make(chan struct{})
	sup.ready = func(ctx context.Context, _, _, _ string) error {
		deadline, _ := ctx.Deadline()
		probing <- time.Until(deadline)
		<-timeUp // stands in for the probe's time running out
		return errors.New("its API at 127.0.0.1:18555 did not answer")
	}
	sup.after = (&waits{block: func(time.Duration) bool { return true }}).after
	run(t, sup)
	sup.SetSource("rtsp://admin:pw@cam:554/s")
	if left := <-probing; left <= 4*time.Second || left > 5*time.Second {
		t.Fatalf("probe has %v; want about 5s", left)
	}
	time.Sleep(20 * time.Millisecond)
	if state := sup.Status().State; state == StateRunning {
		t.Fatal("running before go2rtc answered")
	}
	if addr, _, _ := sup.target(); addr != "" {
		t.Fatalf("proxy target %q before go2rtc answered", addr)
	}
	close(timeUp)
	waitFor(t, "failing after the probe timeout", func() bool { return sup.Status().State == StateFailing })
	if reason := sup.Status().Reason; !strings.Contains(reason, "did not start") || !strings.Contains(reason, "did not answer") {
		t.Fatalf("reason = %q", reason)
	}
	if !st.proc(0).killed.Load() {
		t.Fatal("go2rtc that never answered was left running")
	}
}

func TestSupervisorDeletesTheConfigWhenGo2rtcStops(t *testing.T) {
	sup, st := testSupervisor(t)
	path := filepath.Join(sup.cfg.DataDir, "go2rtc.yaml")
	if err := os.WriteFile(path, []byte("left over"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, sup)
	waitFor(t, "left-over config deleted", func() bool { _, err := os.Stat(path); return errors.Is(err, os.ErrNotExist) })

	sup.SetSource("rtsp://admin:pw@cam:554/s")
	waitFor(t, "start", running(sup, st, 1))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config while running: %v", err)
	}
	sup.SetSource("")
	waitFor(t, "stop", func() bool { return sup.Status().State == StateOff })
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config after stop: %v; want it deleted", err)
	}
}

func TestSupervisorReportsAMissingBinary(t *testing.T) {
	sup, st := testSupervisor(t)
	sup.exists = func(string) bool { return false }
	run(t, sup)
	sup.SetSource("rtsp://x")
	waitFor(t, "missing", func() bool { return sup.Status().State == StateMissing })
	if !strings.Contains(sup.Status().Reason, "go2rtc.exe") || st.count() != 0 {
		t.Fatalf("status = %+v, starts %d", sup.Status(), st.count())
	}
}

func TestConfigListensLocallyWithAPasswordAndOnlyTheModulesInUse(t *testing.T) {
	path := filepath.Join(testdir.New(t), "go2rtc.yaml")
	if err := writeConfig(path, 18555, "u1", "p1", "rtsp://admin:pw@cam:554/s"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil { // JSON is valid YAML for go2rtc
		t.Fatalf("config is not JSON: %v\n%s", err, data)
	}
	api := cfg["api"]
	if api["listen"] != "127.0.0.1:18555" || api["username"] != "u1" || api["password"] != "p1" || api["local_auth"] != true {
		t.Fatalf("api = %v", api)
	}
	if listen, ok := cfg["rtsp"]["listen"]; !ok || listen != "" {
		t.Fatalf("rtsp listen = %v; want \"\" (server off)", listen)
	}
	if fmt.Sprint(cfg["app"]["modules"]) != "[api ws rtsp mp4]" {
		t.Fatalf("modules = %v", cfg["app"]["modules"])
	}
	for _, mod := range []string{"webrtc", "srtp"} {
		if _, ok := cfg[mod]; ok {
			t.Errorf("config has a %s section, but the module is not loaded", mod)
		}
	}
	if cfg["streams"]["gate"] != "rtsp://admin:pw@cam:554/s" {
		t.Fatalf("streams = %v", cfg["streams"])
	}
}

func TestCommandLoadsTheSafeConfigBeforeTheFile(t *testing.T) {
	dir := filepath.Join(testdir.New(t), "data.v2=x") // go2rtc would read this as key=value
	config := filepath.Join(dir, "go2rtc.yaml")
	cmd := command(context.Background(), "go2rtc.exe", config)
	if len(cmd.Args) != 5 || cmd.Args[1] != "-config" || cmd.Args[3] != "-config" {
		t.Fatalf("args = %q", cmd.Args)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("binary %q is relative; Windows would look for it in the working folder", cmd.Path)
	}
	inline, file := cmd.Args[2], cmd.Args[4]
	if !strings.HasPrefix(inline, "{") {
		t.Fatalf("inline config %q does not start with {", inline)
	}
	var safe map[string]map[string]any
	if err := json.Unmarshal([]byte(inline), &safe); err != nil {
		t.Fatal(err)
	}
	if listen, ok := safe["api"]["listen"]; !ok || listen != "" {
		t.Fatalf("inline api listen = %v; want \"\"", listen)
	}
	if listen, ok := safe["rtsp"]["listen"]; !ok || listen != "" {
		t.Fatalf("inline rtsp listen = %v; want \"\"", listen)
	}
	if fmt.Sprint(safe["app"]["modules"]) != "[api ws rtsp mp4]" {
		t.Fatalf("inline modules = %v", safe["app"]["modules"])
	}
	if strings.ContainsAny(file, `=/\{`) || cmd.Dir != dir {
		t.Fatalf("file %q in %q; want a plain name in %q", file, cmd.Dir, dir)
	}
	if cmd.WaitDelay != 5*time.Second {
		t.Fatalf("WaitDelay = %v", cmd.WaitDelay)
	}
}

// fakeGo2rtcAPI answers GET /api like go2rtc with the given credentials.
func fakeGo2rtcAPI(t *testing.T, user, pass string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); user != "" && (!ok || u != user || p != pass) {
			w.Header().Set("Www-Authenticate", `Basic realm="go2rtc"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"version":"1.9.14"}`)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestProbeWantsTheAPIToRefuseRequestsWithoutThePassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probe(ctx, fakeGo2rtcAPI(t, "u1", "p1"), "u1", "p1"); err != nil {
		t.Fatalf("go2rtc with its password: %v", err)
	}
	if err := probe(ctx, fakeGo2rtcAPI(t, "", ""), "u1", "p1"); err == nil || !strings.Contains(err.Error(), "without the password") {
		t.Fatalf("go2rtc without a password: %v; want refused", err)
	}
	if err := probe(ctx, fakeGo2rtcAPI(t, "u1", "other"), "u1", "p1"); err == nil || !strings.Contains(err.Error(), "with the password") {
		t.Fatalf("go2rtc with another password: %v; want refused", err)
	}
}

func TestProbeGivesUpWhenNothingAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := probe(ctx, addr, "u1", "p1"); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("probe = %v; want did not answer", err)
	}
}

// liveBackend is a running supervisor whose go2rtc is a stand-in that
// reports every request it receives.
func liveBackend(t *testing.T) (sup *Supervisor, seen chan *http.Request, addr string) {
	t.Helper()
	seen = make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)
	addr = strings.TrimPrefix(backend.URL, "http://")
	sup = New(Config{})
	sup.set(Status{State: StateRunning}, addr, "u1", "p1")
	return sup, seen, addr
}

// liveFront serves a liveBackend supervisor's handler.
func liveFront(t *testing.T) (front *httptest.Server, seen chan *http.Request, addr string) {
	t.Helper()
	sup, seen, addr := liveBackend(t)
	front = httptest.NewServer(sup.Handler())
	t.Cleanup(front.Close)
	return front, seen, addr
}

func TestProxyPinsThePathAndAddsTheGo2rtcPassword(t *testing.T) {
	front, seen, addr := liveFront(t)
	req, _ := http.NewRequest("GET", front.URL+"/api/live/ws?src=exec:calc", nil)
	req.Header.Set("Cookie", "depot_access=secret")
	req.Header.Set("Origin", front.URL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got := <-seen
	if got.URL.Path != "/api/ws" || got.URL.RawQuery != "src=gate" {
		t.Fatalf("backend got %s?%s; want /api/ws?src=gate", got.URL.Path, got.URL.RawQuery)
	}
	if got.Host != addr || got.Header.Get("Origin") != "http://"+addr {
		t.Fatalf("Host %q Origin %q; want go2rtc's own address", got.Host, got.Header.Get("Origin"))
	}
	if u, p, ok := got.BasicAuth(); !ok || u != "u1" || p != "p1" {
		t.Fatalf("basic auth = %q %q %v", u, p, ok)
	}
	if got.Header.Get("Cookie") != "" {
		t.Fatal("the hub's cookie was forwarded to go2rtc")
	}
}

func TestProxyRefusesCrossSiteRequests(t *testing.T) {
	front, seen, _ := liveFront(t)
	req, _ := http.NewRequest("GET", front.URL+"/api/live/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	want := `{"error":{"code":"E5006","message":"cross-site live video request refused"}}`
	if res.StatusCode != http.StatusForbidden || strings.TrimSpace(string(body)) != want {
		t.Fatalf("answer = %d %s; want 403 %s", res.StatusCode, body, want)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q", res.Header.Get("Content-Type"))
	}
	select {
	case r := <-seen:
		t.Fatalf("go2rtc was asked for %s", r.URL)
	default:
	}
}

func TestSameOrigin(t *testing.T) {
	for _, c := range []struct {
		host   string
		origin []string
		ok     bool
	}{
		{"192.168.1.5:8080", nil, true}, // not a browser
		{"192.168.1.5:8080", []string{"http://192.168.1.5:8080"}, true},
		{"gatepc.local:8080", []string{"http://GatePC.local:8080"}, true},
		{"192.168.1.5:8080", []string{"http://192.168.1.5:9090"}, false},
		{"192.168.1.5:8080", []string{"https://evil.example"}, false},
		{"192.168.1.5:8080", []string{"null"}, false},
		{"192.168.1.5:8080", []string{""}, false},
		{"192.168.1.5:8080", []string{"http://192.168.1.5:8080", "https://evil.example"}, false},
	} {
		r := httptest.NewRequest("GET", "/api/live/ws", nil)
		r.Host = c.host
		for _, o := range c.origin {
			r.Header.Add("Origin", o)
		}
		if got := sameOrigin(r); got != c.ok {
			t.Errorf("host %s origin %q: %v; want %v", c.host, c.origin, got, c.ok)
		}
	}
}

func TestProxyKeepsThePathPinnedForSmuggledPaths(t *testing.T) {
	sup, seen, _ := liveBackend(t)
	// Called directly, so the path reaches the handler exactly as sent.
	req := httptest.NewRequest("GET", "/api/live/ws/..%2fapi%2fstreams?src=exec:calc", nil)
	rec := httptest.NewRecorder()
	sup.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	got := <-seen
	if got.RequestURI != "/api/ws?src=gate" || got.URL.Path != "/api/ws" || got.URL.RawQuery != "src=gate" {
		t.Fatalf("backend got %q (path %q, query %q); want /api/ws?src=gate", got.RequestURI, got.URL.Path, got.URL.RawQuery)
	}
}

func TestProxyCarriesTheWebSocketUpgrade(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		msg := make([]byte, 4)
		if _, err := io.ReadFull(buf, msg); err == nil {
			_, _ = conn.Write(msg)
		}
	}))
	defer backend.Close()
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, strings.TrimPrefix(backend.URL, "http://"), "u1", "p1")
	front := httptest.NewServer(sup.Handler())
	defer front.Close()

	host := strings.TrimPrefix(front.URL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /api/live/ws HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", host, host)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d; want 101", res.StatusCode)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, %v", echo, err)
	}
}

func TestProxyAnswers503WhenLiveVideoIsNotRunning(t *testing.T) {
	front := httptest.NewServer(New(Config{}).Handler())
	defer front.Close()
	res, err := http.Get(front.URL + "/api/live/ws")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "E5030") {
		t.Fatalf("answer = %d %s", res.StatusCode, body)
	}
}

func TestProxyAnswers503QuietlyWhenGo2rtcIsGone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, addr, "u1", "p1")
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	rec := httptest.NewRecorder()
	sup.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/live/ws", nil))
	want := `{"error":{"code":"E5030","message":"live video is not running"}}`
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("answer = %d %s; want 503 %s", rec.Code, rec.Body, want)
	}
	if logged.Len() != 0 {
		t.Fatalf("logged %q", logged.String())
	}
}

func TestProxyNeverAsksTheBrowserForGo2rtcsPassword(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Basic realm="go2rtc"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer backend.Close()
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, strings.TrimPrefix(backend.URL, "http://"), "u1", "stale")
	rec := httptest.NewRecorder()
	sup.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/live/ws", nil))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Www-Authenticate") != "" {
		t.Fatalf("answer %d with Www-Authenticate %q; want it removed", rec.Code, rec.Header().Get("Www-Authenticate"))
	}
}

func TestRedactHidesCredentialsInAnyAddress(t *testing.T) {
	for in, want := range map[string]string{
		"WRN [rtsp] url=rtsp://admin:pw-secret@192.168.1.50:554/h264Preview_01_sub i/o timeout": "WRN [rtsp] url=rtsp://***@192.168.1.50:554/h264Preview_01_sub i/o timeout",
		"dial RTSP://Admin:PW-secret@cam:554/s":                                                 "dial RTSP://***@cam:554/s",
		"rtsps://u:p%40ss@cam/x and http://u2:pw2@cam:80/cgi":                                   "rtsps://***@cam/x and http://***@cam:80/cgi",
		"no address here: user@example.com":                                                     "no address here: user@example.com",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestGo2rtcOutputIsLoggedInWholeLinesWithoutCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	r, w := io.Pipe()
	done := make(chan struct{})
	go func() { logOutput(r); close(done) }()
	// An address split across two writes, as a pipe may deliver it.
	_, _ = io.WriteString(w, "WRN [rtsp] url=RTSP://admin:pw-")
	_, _ = io.WriteString(w, "secret@192.168.1.50:554/h264Preview_01_sub i/o timeout\nINF second line\n")
	_ = w.Close()
	<-done
	out := buf.String()
	if strings.Contains(out, "pw-secret") || strings.Contains(out, "secret@") || !strings.Contains(out, "RTSP://***@192.168.1.50") {
		t.Fatalf("log = %q", out)
	}
	if n := strings.Count(out, "go2rtc: "); n != 2 {
		t.Fatalf("logged %d lines; want 2:\n%s", n, out)
	}
}

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
	sup.minBackoff = 10 * time.Millisecond
	return sup, st
}

func TestSupervisorStartsRestartsAndStopsGo2rtc(t *testing.T) {
	sup, st := testSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	waitFor(t, "off", func() bool { return sup.Status().State == StateOff })
	sup.SetSource("rtsp://admin:pw@cam1:554/h264Preview_01_sub")
	waitFor(t, "first start", func() bool { return st.count() == 1 && sup.Status().State == StateRunning })
	if addr, user, pass := sup.target(); addr != "127.0.0.1:18555" || user == "" || pass == "" {
		t.Fatalf("target = %q %q %q", addr, user, pass)
	}

	st.proc(0).exit <- errors.New("crashed")
	waitFor(t, "restart after a crash", func() bool { return st.count() == 2 && sup.Status().State == StateRunning })

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

func TestSupervisorReportsAMissingBinary(t *testing.T) {
	sup, st := testSupervisor(t)
	sup.exists = func(string) bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.Run(ctx)
	sup.SetSource("rtsp://x")
	waitFor(t, "missing", func() bool { return sup.Status().State == StateMissing })
	if !strings.Contains(sup.Status().Reason, "go2rtc.exe") || st.count() != 0 {
		t.Fatalf("status = %+v, starts %d", sup.Status(), st.count())
	}
}

func TestConfigListensLocallyWithAPassword(t *testing.T) {
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
	for _, mod := range []string{"rtsp", "webrtc", "srtp"} {
		if cfg[mod]["listen"] != "127.0.0.1:0" {
			t.Errorf("%s listen = %v; want 127.0.0.1:0", mod, cfg[mod]["listen"])
		}
	}
	if cfg["streams"]["gate"] != "rtsp://admin:pw@cam:554/s" {
		t.Fatalf("streams = %v", cfg["streams"])
	}
}

func TestProxyPinsThePathAndAddsTheGo2rtcPassword(t *testing.T) {
	seen := make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, addr, "u1", "p1")
	front := httptest.NewServer(sup.Handler())
	defer front.Close()

	req, _ := http.NewRequest("GET", front.URL+"/api/live/ws?src=exec:calc", nil)
	req.Header.Set("Cookie", "depot_access=secret")
	req.Header.Set("Origin", "http://10.0.0.5:8080")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
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
	fmt.Fprintf(conn, "GET /api/live/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", host)
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

func TestGo2rtcOutputHidesCameraCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	_, _ = logWriter{}.Write([]byte("WRN [rtsp] url=rtsp://admin:pw-secret@192.168.1.50:554/h264Preview_01_sub i/o timeout\n"))
	if out := buf.String(); strings.Contains(out, "pw-secret") || !strings.Contains(out, "rtsp://***@192.168.1.50") {
		t.Fatalf("log = %q", out)
	}
}

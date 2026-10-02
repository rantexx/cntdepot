// Package live runs go2rtc, which turns the camera's RTSP stream into video a
// browser can play, and passes that video through the hub's own address.
// go2rtc listens on 127.0.0.1 only and requires a user name and password
// generated at every start. Nothing else on the network, and no web page open
// on the PC, can use it.
package live

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Live video states.
const (
	StateOff     = "off"     // no camera with live video
	StateMissing = "missing" // go2rtc is not installed
	StateRunning = "running"
	StateFailing = "failing" // go2rtc stopped; restarting
)

// Status is what the Camera page shows about live video.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Process is a running go2rtc.
type Process interface {
	Wait() error
	Kill() error
}

// Config locates go2rtc and its config file.
type Config struct {
	Binary  string // go2rtc executable
	DataDir string // go2rtc.yaml is written here while go2rtc runs (it holds the camera password)
}

// modules are the only go2rtc modules loaded: the HTTP API, its WebSocket
// endpoint, the RTSP client (its server stays off) and the MSE/MP4 player.
// Nothing else starts even when a client asks for it: no WebRTC (which binds
// UDP on every interface and contacts public STUN servers), no exec or ffmpeg
// sources, no HomeKit, RTMP or HLS.
var modules = []string{"api", "ws", "rtsp", "mp4"}

const (
	configName   = "go2rtc.yaml"
	readyTimeout = 5 * time.Second // go2rtc's API must answer within this after a start
)

// Supervisor keeps go2rtc running while there is a live source.
type Supervisor struct {
	cfg        Config
	start      func(ctx context.Context, binary, config string) (Process, error)
	port       func() (int, error)
	exists     func(path string) bool
	ready      func(ctx context.Context, addr, user, pass string) error // returns once go2rtc's API answers correctly
	after      func(d time.Duration) <-chan time.Time                   // the clock for waits between restarts
	minBackoff time.Duration
	maxBackoff time.Duration

	mu     sync.Mutex
	source string
	addr   string // go2rtc's API address while running
	user   string
	pass   string
	status Status
	wake   chan struct{}
}

// New builds a supervisor. Nothing runs until Run.
func New(cfg Config) *Supervisor {
	return &Supervisor{
		cfg: cfg, start: execStart, port: freePort, exists: fileExists, ready: probe, after: time.After,
		minBackoff: time.Second, maxBackoff: 30 * time.Second,
		status: Status{State: StateOff}, wake: make(chan struct{}, 1),
	}
}

// SetSource sets the RTSP address to show. "" stops go2rtc.
func (s *Supervisor) SetSource(src string) {
	s.mu.Lock()
	changed := s.source != src
	s.source = src
	s.mu.Unlock()
	if changed {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Status reports whether live video is running.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Supervisor) target() (addr, user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr, s.user, s.pass
}

func (s *Supervisor) set(st Status, addr, user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.addr, s.user, s.pass = st, addr, user, pass
}

// forgetTarget stops the proxy from using a go2rtc that was stopped, while
// the page keeps its status until the next one is ready.
func (s *Supervisor) forgetTarget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addr, s.user, s.pass = "", "", ""
}

func (s *Supervisor) currentSource() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source
}

var errChanged = errors.New("live source changed")

// Run keeps go2rtc running for the current source until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.minBackoff
	for {
		src := s.currentSource()
		switch {
		case src == "":
			s.set(Status{State: StateOff}, "", "", "")
			s.removeConfig() // left behind if the hub was killed while go2rtc ran
			if !s.sleep(ctx, 0) {
				return
			}
			continue
		case !s.exists(s.cfg.Binary):
			s.set(Status{State: StateMissing, Reason: "go2rtc not found at " + s.cfg.Binary}, "", "", "")
			if !s.sleep(ctx, 30*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := s.runOnce(ctx, src)
		if ctx.Err() != nil {
			s.set(Status{State: StateOff}, "", "", "")
			return
		}
		if errors.Is(err, errChanged) {
			s.forgetTarget()
			backoff = s.minBackoff
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = s.minBackoff // it ran fine for a while: restart quickly
		}
		log.Printf("live: %v; restarting in %v", err, backoff)
		s.set(Status{State: StateFailing, Reason: err.Error() + "; restarting"}, "", "", "")
		if !s.sleep(ctx, backoff) {
			return
		}
		if s.currentSource() != src {
			backoff = s.minBackoff // new camera settings: try them without the old wait
			continue
		}
		backoff = min(backoff*2, s.maxBackoff)
	}
}

// runOnce starts go2rtc for src and waits until it exits, the source changes,
// or ctx ends. Live video counts as running only once go2rtc's API answers
// and refuses requests without its password.
func (s *Supervisor) runOnce(ctx context.Context, src string) error {
	port, err := s.port()
	if err != nil {
		return fmt.Errorf("no free port for go2rtc: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	user, pass := randomToken(), randomToken()
	path := s.configPath()
	defer s.removeConfig() // it holds the camera password
	if err := writeConfig(path, port, user, pass, src); err != nil {
		return fmt.Errorf("cannot write %s: %w", configName, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	proc, err := s.start(runCtx, s.cfg.Binary, path)
	if err != nil {
		return fmt.Errorf("cannot start go2rtc: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	stop := func() {
		_ = proc.Kill()
		<-exited
	}

	readyCtx, readyCancel := context.WithTimeout(runCtx, readyTimeout)
	defer readyCancel()
	readyc := make(chan error, 1)
	go func() { readyc <- s.ready(readyCtx, addr, user, pass) }()
	pending := (<-chan error)(readyc) // nil once the answer is in

	for {
		select {
		case err := <-pending:
			pending = nil
			if err != nil {
				stop()
				return fmt.Errorf("go2rtc did not start: %w", err)
			}
			s.set(Status{State: StateRunning}, addr, user, pass)
		case err := <-exited:
			if err == nil {
				return errors.New("go2rtc exited")
			}
			return fmt.Errorf("go2rtc exited: %w", err)
		case <-s.wake:
			// A wake can be left over from the SetSource that started this run
			// (or from one that was already acted on). Restart only when the
			// source really differs from the one go2rtc was started with.
			if s.currentSource() == src {
				continue
			}
			stop()
			return errChanged
		case <-ctx.Done():
			stop()
			return ctx.Err()
		}
	}
}

// sleep waits for d (forever when d is 0), a source change, or the end of
// ctx. It reports false when ctx ended.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	var timeout <-chan time.Time
	if d > 0 {
		timeout = s.after(d)
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timeout:
		return true
	}
}

func (s *Supervisor) configPath() string { return filepath.Join(s.cfg.DataDir, configName) }

// removeConfig deletes go2rtc.yaml, which holds the camera password. go2rtc
// reads it only when it starts.
func (s *Supervisor) removeConfig() {
	if err := os.Remove(s.configPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("live: cannot delete %s: %v", configName, err)
	}
}

// writeConfig writes go2rtc's config as JSON, which is valid YAML. The API
// listens on 127.0.0.1 only and needs the password even from this PC
// (local_auth). The RTSP server is off ("" keeps only the RTSP client), and
// only the modules in use are loaded, so go2rtc opens no other port.
//
// The file holds the camera password. Mode 0600 protects it on Unix only: on
// Windows it merely leaves the read-only attribute off, and who may read the
// file is decided by the data folder's permissions, which it inherits. It is
// deleted when go2rtc stops.
func writeConfig(path string, port int, user, pass, src string) error {
	cfg := map[string]any{
		"app": map[string]any{"modules": modules},
		"api": map[string]any{
			"listen":     fmt.Sprintf("127.0.0.1:%d", port),
			"username":   user,
			"password":   pass,
			"local_auth": true,
		},
		"rtsp":    map[string]any{"listen": ""},
		"log":     map[string]any{"level": "warn"},
		"streams": map[string]any{"gate": src},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// safeConfig is go2rtc's first config, given inline on its command line.
// go2rtc skips a config file it cannot read or parse and carries on with its
// defaults, which serve the API on every network interface without a
// password. With this loaded first, such a go2rtc opens no port at all; the
// file then overrides only the keys it sets. Command lines are visible to
// other programs on the PC, so this holds no secrets.
func safeConfig() string {
	data, err := json.Marshal(map[string]any{
		"app":  map[string]any{"modules": modules},
		"api":  map[string]any{"listen": ""},
		"rtsp": map[string]any{"listen": ""},
	})
	if err != nil {
		panic(err) // constant input
	}
	return string(data)
}

// command builds go2rtc's command line: the inline safe config, then the
// file. go2rtc reads a -config value starting with "{" as inline YAML or
// JSON, and one with "=" after a "." (C:\data.old=1\go2rtc.yaml) as an inline
// key=value setting. So the file is named relative to its folder, which
// becomes go2rtc's working folder, and the binary path is made absolute
// (Windows would otherwise look for it in that folder).
func command(ctx context.Context, binary, config string) *exec.Cmd {
	if abs, err := filepath.Abs(binary); err == nil {
		binary = abs
	}
	cmd := exec.CommandContext(ctx, binary, "-config", safeConfig(), "-config", filepath.Base(config))
	cmd.Dir = filepath.Dir(config)
	cmd.WaitDelay = 5 * time.Second
	hideWindow(cmd)
	return cmd
}

func execStart(ctx context.Context, binary, config string) (Process, error) {
	cmd := command(ctx, binary, config)
	// go2rtc writes into a pipe of our own, so Wait never waits on the log
	// reader, and the reader still gets every line up to go2rtc's exit.
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = w, w
	err = cmd.Start()
	_ = w.Close() // go2rtc holds its own copy; the reader sees EOF when it exits
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	if err := keepWithHub(cmd.Process); err != nil {
		log.Printf("live: go2rtc may keep running if the hub is killed: %v", err)
	}
	go logOutput(r)
	return cmdProcess{cmd}, nil
}

type cmdProcess struct{ cmd *exec.Cmd }

func (p cmdProcess) Wait() error { return p.cmd.Wait() }
func (p cmdProcess) Kill() error { return p.cmd.Process.Kill() }

// logOutput copies go2rtc's output into the hub log a whole line at a time:
// a single write can end in the middle of an address, and the credentials
// can only be found in the complete line.
func logOutput(r io.ReadCloser) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		if line := strings.TrimSpace(redact(sc.Text())); line != "" {
			log.Printf("go2rtc: %s", line)
		}
	}
	_, _ = io.Copy(io.Discard, r) // after an over-long line: keep go2rtc's writes flowing
}

var credsRE = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*)://[^@/\s]+@`)

// redact removes the user name and password from every address in a line
// (rtsp://admin:pw@cam becomes rtsp://***@cam), whatever the scheme or case.
func redact(line string) string {
	return credsRE.ReplaceAllString(line, "${1}://***@")
}

// probe waits until go2rtc's API on addr refuses a request without its
// password and accepts one with it, or until ctx ends.
func probe(ctx context.Context, addr, user, pass string) error {
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		Timeout:   time.Second,
	}
	defer client.CloseIdleConnections()
	url := "http://" + addr + "/api"
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		answered, err := checkAPI(ctx, client, url, user, pass)
		if answered {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("its API at %s did not answer", addr)
		case <-tick.C:
		}
	}
}

// checkAPI asks go2rtc's API once without and once with its password.
// answered is false while nothing answers yet; once it is true, err tells
// whether the answers were right.
func checkAPI(ctx context.Context, client *http.Client, url, user, pass string) (answered bool, err error) {
	code, err := getStatus(ctx, client, url, "", "")
	if err != nil {
		return false, err
	}
	if code != http.StatusUnauthorized {
		return true, fmt.Errorf("its API answered %d without the password; want 401", code)
	}
	code, err = getStatus(ctx, client, url, user, pass)
	if err != nil {
		return false, err
	}
	if code != http.StatusOK {
		return true, fmt.Errorf("its API answered %d with the password; want 200", code)
	}
	return true, nil
}

func getStatus(ctx context.Context, client *http.Client, url, user, pass string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	_ = res.Body.Close()
	return res.StatusCode, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Package live runs go2rtc, which turns the camera's RTSP stream into video a
// browser can play, and passes that video through the hub's own address.
// go2rtc listens on 127.0.0.1 only and requires a user name and password
// generated at every start. Nothing else on the network, and no web page open
// on the PC, can use it.
package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
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
	DataDir string // go2rtc.yaml is written here (it holds the camera password)
}

// Supervisor keeps go2rtc running while there is a live source.
type Supervisor struct {
	cfg        Config
	start      func(ctx context.Context, binary, config string) (Process, error)
	port       func() (int, error)
	exists     func(path string) bool
	minBackoff time.Duration

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
		cfg: cfg, start: execStart, port: freePort, exists: fileExists, minBackoff: time.Second,
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

var errChanged = errors.New("live source changed")

// Run keeps go2rtc running for the current source until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.minBackoff
	for {
		s.mu.Lock()
		src := s.source
		s.mu.Unlock()
		switch {
		case src == "":
			s.set(Status{State: StateOff}, "", "", "")
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
			backoff = s.minBackoff
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = s.minBackoff // it ran fine for a while: restart quickly
		}
		log.Printf("live: go2rtc stopped: %v; restarting in %v", err, backoff)
		s.set(Status{State: StateFailing, Reason: fmt.Sprintf("go2rtc stopped (%v); restarting", err)}, "", "", "")
		if !s.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// runOnce starts go2rtc for src and waits until it exits, the source changes,
// or ctx ends.
func (s *Supervisor) runOnce(ctx context.Context, src string) error {
	port, err := s.port()
	if err != nil {
		return err
	}
	user, pass := randomToken(), randomToken()
	path := filepath.Join(s.cfg.DataDir, "go2rtc.yaml")
	if err := writeConfig(path, port, user, pass, src); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	proc, err := s.start(runCtx, s.cfg.Binary, path)
	if err != nil {
		return err
	}
	s.set(Status{State: StateRunning}, fmt.Sprintf("127.0.0.1:%d", port), user, pass)
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	for {
		select {
		case err := <-exited:
			if err == nil {
				err = errors.New("exited")
			}
			return err
		case <-s.wake:
			// A wake can be left over from the SetSource that started this run
			// (or from one that was already acted on). Restart only when the
			// source really differs from the one go2rtc was started with.
			if s.currentSource() == src {
				continue
			}
			_ = proc.Kill()
			<-exited
			return errChanged
		case <-ctx.Done():
			_ = proc.Kill()
			<-exited
			return ctx.Err()
		}
	}
}

func (s *Supervisor) currentSource() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.source
}

// sleep waits for d (forever when d is 0), a source change, or the end of
// ctx. It reports false when ctx ended.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
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

// writeConfig writes go2rtc's config as JSON, which is valid YAML. Every
// listener is bound to 127.0.0.1, and the API needs the password even from
// this PC (local_auth). The file holds the camera password, so only the
// owner may read it.
func writeConfig(path string, port int, user, pass, src string) error {
	cfg := map[string]any{
		"api": map[string]any{
			"listen":     fmt.Sprintf("127.0.0.1:%d", port),
			"username":   user,
			"password":   pass,
			"local_auth": true,
		},
		"rtsp":    map[string]any{"listen": "127.0.0.1:0"},
		"webrtc":  map[string]any{"listen": "127.0.0.1:0"},
		"srtp":    map[string]any{"listen": "127.0.0.1:0"},
		"log":     map[string]any{"level": "warn"},
		"streams": map[string]any{"gate": src},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func execStart(ctx context.Context, binary, config string) (Process, error) {
	cmd := exec.CommandContext(ctx, binary, "-config", config)
	cmd.Stdout, cmd.Stderr = logWriter{}, logWriter{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmdProcess{cmd}, nil
}

type cmdProcess struct{ cmd *exec.Cmd }

func (p cmdProcess) Wait() error { return p.cmd.Wait() }
func (p cmdProcess) Kill() error { return p.cmd.Process.Kill() }

var credsRE = regexp.MustCompile(`rtsp://[^@/\s]+@`)

// logWriter copies go2rtc's output into the hub log, without the camera
// credentials that RTSP addresses carry.
type logWriter struct{}

func (logWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Printf("go2rtc: %s", credsRE.ReplaceAllString(line, "rtsp://***@"))
		}
	}
	return len(b), nil
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

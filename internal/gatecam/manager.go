package gatecam

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"depot/internal/camera"
	"depot/internal/store"
)

// LiveSink receives the camera's live video address. The live supervisor
// implements it.
type LiveSink interface{ SetSource(src string) }

// Builder connects to the camera that settings describe.
type Builder func(camera.Settings) camera.Camera

// Settings keys in the hub database.
const (
	keyHost     = "camera.host"
	keyUser     = "camera.user"
	keyPassword = "camera.password"
	keyAuto     = "camera.auto"
)

// ErrBadSettings means the entered settings cannot work.
var ErrBadSettings = errors.New("invalid camera settings")

// Update is a change from the Camera page. Nil fields keep their value, and
// an empty password keeps the saved one; the page never sees it.
type Update struct {
	Host     *string `json:"host"`
	User     *string `json:"user"`
	Password *string `json:"password"`
	Auto     *bool   `json:"auto"`
}

// Manager keeps the camera settings and applies them to the watcher and the
// live video.
type Manager struct {
	store   *store.Store
	watcher *Watcher
	live    LiveSink
	build   Builder

	mu      sync.Mutex
	current camera.Settings
	fixed   camera.Camera // replaces the saved settings (depotd hub -fake-camera)
	applied bool
}

// NewManager wires the pieces together. Call Load next.
func NewManager(st *store.Store, w *Watcher, live LiveSink, build Builder) *Manager {
	return &Manager{store: st, watcher: w, live: live, build: build}
}

// UseFixed makes the manager use cam, whatever the saved settings say.
func (m *Manager) UseFixed(cam camera.Camera) {
	m.mu.Lock()
	m.fixed = cam
	m.mu.Unlock()
}

// Load reads the saved settings and applies them.
func (m *Manager) Load(ctx context.Context) error {
	vals, err := m.store.Settings(ctx, "camera.")
	if err != nil {
		return err
	}
	m.apply(camera.Settings{Host: vals[keyHost], User: vals[keyUser], Password: vals[keyPassword], Auto: vals[keyAuto] == "1"})
	return nil
}

// Settings returns the current settings, password included. Callers must
// never send the password to a browser.
func (m *Manager) Settings() camera.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Save validates, stores and applies an update.
func (m *Manager) Save(ctx context.Context, u Update) (camera.Settings, error) {
	s := m.Settings()
	if u.Host != nil {
		s.Host = strings.TrimSpace(*u.Host)
	}
	if u.User != nil {
		s.User = strings.TrimSpace(*u.User)
	}
	if u.Password != nil && *u.Password != "" {
		s.Password = *u.Password
	}
	if u.Auto != nil {
		s.Auto = *u.Auto
	}
	if err := checkHost(s.Host); err != nil {
		return s, err
	}
	if len(s.User) > 64 || len(s.Password) > 128 {
		return s, fmt.Errorf("%w: user name or password too long", ErrBadSettings)
	}
	auto := "0"
	if s.Auto {
		auto = "1"
	}
	err := m.store.SetSettings(ctx, map[string]string{keyHost: s.Host, keyUser: s.User, keyPassword: s.Password, keyAuto: auto}, "admin")
	if err != nil {
		return s, err
	}
	m.apply(s)
	return s, nil
}

// apply hands the settings to the watcher and the live video. Changing only
// the automatic switch keeps the connection.
func (m *Manager) apply(s camera.Settings) {
	m.mu.Lock()
	prev, fixed, applied := m.current, m.fixed, m.applied
	m.current, m.applied = s, true
	m.mu.Unlock()
	m.watcher.SetAuto(s.Auto)
	if applied && prev.Host == s.Host && prev.User == s.User && prev.Password == s.Password {
		return
	}
	var cam camera.Camera
	switch {
	case fixed != nil:
		cam = fixed
	case s.Configured():
		cam = m.build(s)
	}
	m.watcher.SetCamera(cam)
	src := ""
	if cam != nil {
		src = cam.LiveSource()
	}
	m.live.SetSource(src)
}

// Status is the watcher's camera status.
func (m *Manager) Status() Status { return m.watcher.Status() }

// Camera is the connected camera, or nil.
func (m *Manager) Camera() camera.Camera { return m.watcher.Camera() }

// Capture starts a visit by hand.
func (m *Manager) Capture(ctx context.Context) (string, error) { return m.watcher.Capture(ctx) }

// checkHost accepts an IPv4 address or a name, optionally with a port, and
// nothing else: no scheme, path or credentials.
func checkHost(h string) error {
	if h == "" {
		return nil // no camera
	}
	bad := fmt.Errorf("%w: enter only the camera's address, for example 192.168.1.50", ErrBadSettings)
	if len(h) > 253 || strings.ContainsAny(h, "/\\@?#% \t") {
		return bad
	}
	host := h
	if hh, port, err := net.SplitHostPort(h); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad
		}
		host = hh
	}
	if host == "" || strings.Contains(host, ":") {
		return bad
	}
	return nil
}

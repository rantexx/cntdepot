package gatecam

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
// an empty password keeps the saved one; the page never sees it. The saved
// password is kept only for the saved address and user name: changing either
// needs the password again, so nobody can send it to another host.
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

	// saveMu makes each Save, Load and Reconnect whole: read the settings,
	// store them, apply them. Two pages saving at once must not lose either
	// change, nor leave the camera on the older settings.
	saveMu sync.Mutex

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
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
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
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	prev := m.Settings()
	s := prev
	if u.Host != nil {
		s.Host = strings.TrimSpace(*u.Host)
	}
	if u.User != nil {
		s.User = strings.TrimSpace(*u.User)
	}
	newPassword := u.Password != nil && *u.Password != ""
	if newPassword {
		s.Password = *u.Password
	}
	// The saved password belongs to the saved address and user name: keeping
	// it for new ones would let anyone with the app send it to a host of
	// their choice.
	if prev.Password != "" && !newPassword && (s.Host != prev.Host || s.User != prev.User) {
		return prev, fmt.Errorf("%w: enter the camera password again when you change the address or user name", ErrBadSettings)
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
// the automatic switch keeps a working connection; saving again after a wrong
// password or while the camera is offline connects afresh. Callers hold
// saveMu.
func (m *Manager) apply(s camera.Settings) {
	m.mu.Lock()
	prev, fixed, applied := m.current, m.fixed, m.applied
	m.current, m.applied = s, true
	m.mu.Unlock()
	m.watcher.SetAuto(s.Auto)
	state := m.watcher.Status().State
	if applied && prev.Host == s.Host && prev.User == s.User && prev.Password == s.Password &&
		state != StateAuth && state != StateOffline {
		return
	}
	m.connect(s, fixed)
}

// Reconnect connects afresh with the current settings (or the fixed camera),
// for example after a successful setup test following a wrong password: a
// camera stopped by a wrong password is never polled again on its own.
func (m *Manager) Reconnect() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	s, fixed := m.current, m.fixed
	m.mu.Unlock()
	m.connect(s, fixed)
}

// connect builds a new camera, which forgets any password it saw rejected,
// and hands it to the watcher and the live video. The camera it replaces is
// closed, which frees its session on the camera; the fixed camera is never
// replaced by itself, so it is never closed here. Callers hold saveMu.
func (m *Manager) connect(s camera.Settings, fixed camera.Camera) {
	var cam camera.Camera
	switch {
	case fixed != nil:
		cam = fixed
	case s.Configured():
		cam = m.build(s)
	}
	prev := m.watcher.Camera()
	m.watcher.SetCamera(cam)
	src := ""
	if cam != nil {
		src = cam.LiveSource()
	}
	m.live.SetSource(src)
	if prev != nil && prev != cam {
		prev.Close()
	}
}

// Close closes the current camera, which logs its session out. The hub calls
// it once the watcher has stopped.
func (m *Manager) Close() {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if cam := m.watcher.Camera(); cam != nil {
		cam.Close()
	}
}

// Status is the watcher's camera status.
func (m *Manager) Status() Status { return m.watcher.Status() }

// Camera is the connected camera, or nil.
func (m *Manager) Camera() camera.Camera { return m.watcher.Camera() }

// Capture starts a visit by hand.
func (m *Manager) Capture(ctx context.Context) (string, error) { return m.watcher.Capture(ctx) }

// hostPattern is an IPv4 address or a name, optionally with a port: letters,
// digits, dots and hyphens only.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)

// checkHost accepts an IPv4 address or a name, optionally with a port, and
// nothing else: no scheme, path, credentials, brackets or control
// characters. "" means no camera.
func checkHost(h string) error {
	if h == "" {
		return nil // no camera
	}
	bad := fmt.Errorf("%w: enter only the camera's address, for example 192.168.1.50", ErrBadSettings)
	if len(h) > 253 || !hostPattern.MatchString(h) {
		return bad
	}
	host, port, hasPort := strings.Cut(h, ":")
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad
		}
	}
	if host == "" || strings.Trim(host, ".-") != host {
		return bad // no leading or trailing dots or hyphens
	}
	return nil
}

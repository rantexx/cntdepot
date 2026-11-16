package gatecam

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"depot/internal/camera"
	"depot/internal/testkit"
)

type fakeLive struct {
	mu      sync.Mutex
	sources []string
}

func (f *fakeLive) SetSource(src string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sources = append(f.sources, src)
}

func (f *fakeLive) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sources) == 0 {
		return "<none>"
	}
	return f.sources[len(f.sources)-1]
}

func ptr[T any](v T) *T { return &v }

func TestManagerSavesAppliesAndKeepsThePassword(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	live := &fakeLive{}
	var built []camera.Settings
	build := func(s camera.Settings) camera.Camera {
		built = append(built, s)
		return camera.NewReolink(s)
	}
	m := NewManager(st, w, live, build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Camera() != nil || w.Status().State != StateUnconfigured || live.last() != "" {
		t.Fatalf("before setup: camera %v, status %+v, live %q", w.Camera(), w.Status(), live.last())
	}

	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.50"), User: ptr("admin"), Password: ptr("pw-secret")}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0].Password != "pw-secret" || w.Camera() == nil {
		t.Fatalf("built = %+v", built)
	}
	if got := live.last(); got != "rtsp://admin:pw-secret@192.168.1.50:554/h264Preview_01_sub" {
		t.Fatalf("live source = %s", got)
	}

	// Switching automatic photos on keeps the connection.
	if _, err := m.Save(ctx, Update{Auto: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || !w.Status().Auto {
		t.Fatalf("built %d cameras, auto %v; want the same camera with auto on", len(built), w.Status().Auto)
	}

	// An empty password keeps the saved one for the same address and user.
	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.50"), User: ptr("admin"), Password: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if s := m.Settings(); s.Password != "pw-secret" || len(built) != 1 {
		t.Fatalf("settings = %+v, built %d; want the saved password and the working camera kept", s, len(built))
	}

	// A new address needs the password again.
	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.51"), Password: ptr("pw-secret")}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 || built[1].Host != "192.168.1.51" || built[1].Password != "pw-secret" {
		t.Fatalf("built = %+v", built)
	}

	// A new manager over the same database loads the same settings.
	m2 := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, build, testkit.Scope)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s := m2.Settings(); s.Host != "192.168.1.51" || s.User != "admin" || s.Password != "pw-secret" || !s.Auto {
		t.Fatalf("loaded settings = %+v", s)
	}
}

func TestChangingTheAddressOrUserNeedsThePasswordAgain(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	b := &fakeBuilder{}
	m := NewManager(st, w, &fakeLive{}, b.build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ctx, setupUpdate()); err != nil {
		t.Fatal(err)
	}
	for _, u := range []Update{
		{Host: ptr("203.0.113.9")},                       // somebody else's host
		{Host: ptr("203.0.113.9"), Password: ptr("")},    // the page sends an empty password
		{User: ptr("other")},                             // another user on the same camera
		{Host: ptr("203.0.113.9"), User: ptr(setupUser)}, // same user, new host
	} {
		_, err := m.Save(ctx, u)
		if !errors.Is(err, ErrBadSettings) || !strings.Contains(err.Error(), "enter the camera password again") {
			t.Errorf("Save(%s) = %v; want ErrBadSettings asking for the password", describe(u), err)
		}
	}
	if b.count() != 1 {
		t.Fatalf("built %+v; a refused save connected to a camera", b.built)
	}
	if s := m.Settings(); s.Host != setupHost || s.User != setupUser || s.Password != setupPassword {
		t.Fatalf("settings = %+v; want them unchanged", s)
	}
	stored, err := st.Settings(ctx, "camera.")
	if err != nil {
		t.Fatal(err)
	}
	if stored[keyHost] != setupHost || stored[keyUser] != setupUser {
		t.Fatalf("stored = %v; a refused save was written", stored)
	}

	// Still allowed: the automatic switch alone, the same address and user
	// with an empty password (the re-save that reconnects), and a new
	// address with the password typed again.
	for _, u := range []Update{
		{Auto: ptr(true)},
		{Host: ptr(setupHost), User: ptr(setupUser), Password: ptr("")},
		{Host: ptr(" " + setupHost + " "), User: ptr(setupUser)}, // spaces are trimmed: the same address
		{Host: ptr("192.168.1.51"), Password: ptr(setupPassword)},
	} {
		if _, err := m.Save(ctx, u); err != nil {
			t.Errorf("Save(%s) = %v", describe(u), err)
		}
	}
	if s := m.Settings(); s.Host != "192.168.1.51" || s.Password != setupPassword || !s.Auto {
		t.Fatalf("settings = %+v", s)
	}
}

// describe prints an update without its password.
func describe(u Update) string {
	str := func(p *string) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprintf("%q", *p)
	}
	pw := "nil"
	if u.Password != nil {
		pw = fmt.Sprintf("%d chars", len(*u.Password))
	}
	return fmt.Sprintf("host %s, user %s, password %s, auto %v", str(u.Host), str(u.User), pw, u.Auto != nil)
}

func TestManagerRejectsAnythingButAnAddress(t *testing.T) {
	st, _ := testkit.Open(t)
	m := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) }, testkit.Scope)
	for _, host := range []string{
		"http://192.168.1.50", "192.168.1.50/cgi-bin", "admin@192.168.1.50", "192.168.1.50:99999", "cam era", "fe80::1",
		"cam\nera", "cam\x00", "host:+80", "[192.168.1.50]:80", "cam]era", "<x>",
		".cam", "cam.", "-cam", "cam-", ":80", "192.168.1.50:0", "192.168.1.50:", "cam:80:81", "cam:123456",
	} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); !errors.Is(err, ErrBadSettings) {
			t.Errorf("Save(host %q) = %v; want ErrBadSettings", host, err)
		}
	}
	for _, host := range []string{"192.168.1.50", "192.168.1.50:8080", "gate-cam.local", "gate-cam.local:65535", "CAM1:1", ""} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); err != nil {
			t.Errorf("Save(host %q) = %v", host, err)
		}
	}
}

func TestManagerUseFixedIgnoresSavedSettings(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	fake := camera.NewFake(camera.DemoStill())
	m := NewManager(st, w, &fakeLive{}, func(s camera.Settings) camera.Camera { t.Fatal("must not build a camera"); return nil }, testkit.Scope)
	m.UseFixed(fake)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Camera() != camera.Camera(fake) {
		t.Fatal("the fixed camera is not in use")
	}
}

// closeCounter is a fake camera that counts Close calls.
type closeCounter struct {
	*camera.Fake
	mu     sync.Mutex
	closes int
}

func (c *closeCounter) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
}

func (c *closeCounter) closed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// fakeBuilder builds fake cameras; the first one answers with firstErr.
type fakeBuilder struct {
	mu       sync.Mutex
	firstErr error
	built    []camera.Settings
	cams     []*closeCounter
}

func (b *fakeBuilder) build(s camera.Settings) camera.Camera {
	b.mu.Lock()
	defer b.mu.Unlock()
	f := camera.NewFake([]byte("still"))
	if len(b.built) == 0 && b.firstErr != nil {
		f.SetDetectErr(b.firstErr)
	}
	b.built = append(b.built, s)
	c := &closeCounter{Fake: f}
	b.cams = append(b.cams, c)
	return c
}

// closes is how often each built camera was closed, in build order.
func (b *fakeBuilder) closes() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := []int{}
	for _, c := range b.cams {
		n = append(n, c.closed())
	}
	return fmt.Sprint(n)
}

func (b *fakeBuilder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.built)
}

func (b *fakeBuilder) last() camera.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.built[len(b.built)-1]
}

const setupHost, setupUser, setupPassword = "192.168.1.50", "admin", "pw-secret"

func setupUpdate() Update {
	return Update{Host: ptr(setupHost), User: ptr(setupUser), Password: ptr(setupPassword)}
}

func TestResavingTheSameSettingsReconnectsAStoppedCamera(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state string
	}{
		{"wrong password", fmt.Errorf("%w: login failed", camera.ErrAuth), StateAuth},
		{"offline", fmt.Errorf("%w: timeout", camera.ErrUnreachable), StateOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := testkit.Open(t)
			w := New(newFakeGate(), DefaultConfig())
			b := &fakeBuilder{firstErr: tc.err}
			m := NewManager(st, w, &fakeLive{}, b.build, testkit.Scope)
			if err := m.Load(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Save(ctx, setupUpdate()); err != nil {
				t.Fatal(err)
			}
			w.poll(ctx)
			if w.Status().State != tc.state {
				t.Fatalf("status = %+v; want %s", w.Status(), tc.state)
			}
			// The owner fixed the camera (or its password) and saves the same settings again.
			if _, err := m.Save(ctx, setupUpdate()); err != nil {
				t.Fatal(err)
			}
			if b.count() != 2 || w.Status().State != StateConnecting {
				t.Fatalf("built %d cameras, status %+v; want a fresh connection", b.count(), w.Status())
			}
			w.poll(ctx)
			if w.Status().State != StateOnline {
				t.Fatalf("status = %+v; want online", w.Status())
			}
			// Online now: switching automatic photos on keeps the connection.
			if _, err := m.Save(ctx, Update{Auto: ptr(true)}); err != nil {
				t.Fatal(err)
			}
			if b.count() != 2 {
				t.Fatalf("built %d cameras; want the working one kept", b.count())
			}
		})
	}
}

func TestReconnectAfterAWrongPassword(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	b := &fakeBuilder{firstErr: fmt.Errorf("%w: login failed", camera.ErrAuth)}
	live := &fakeLive{}
	m := NewManager(st, w, live, b.build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ctx, setupUpdate()); err != nil {
		t.Fatal(err)
	}
	w.poll(ctx)
	if w.Status().State != StateAuth {
		t.Fatalf("status = %+v; want auth", w.Status())
	}
	m.Reconnect()
	if b.count() != 2 || b.last().Password != setupPassword || w.Status().State != StateConnecting {
		t.Fatalf("built %+v, status %+v; want a new camera from the saved settings", b.built, w.Status())
	}
	if n := len(live.sources); n != 3 { // Load, Save, Reconnect
		t.Fatalf("live sources = %q; want the live video restarted too", live.sources)
	}
	w.poll(ctx)
	if w.Status().State != StateOnline {
		t.Fatalf("status = %+v; want online", w.Status())
	}
}

func TestReconnectUsesTheFixedCamera(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	fixed := camera.NewFake(camera.DemoStill())
	fixed.SetDetectErr(fmt.Errorf("%w: login failed", camera.ErrAuth))
	m := NewManager(st, w, &fakeLive{}, func(camera.Settings) camera.Camera { t.Fatal("must not build a camera"); return nil }, testkit.Scope)
	m.UseFixed(fixed)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	w.poll(ctx)
	fixed.SetDetectErr(nil)
	m.Reconnect()
	w.poll(ctx)
	if w.Camera() != camera.Camera(fixed) || w.Status().State != StateOnline {
		t.Fatalf("camera %v, status %+v; want the fixed camera online", w.Camera(), w.Status())
	}
}

func TestConcurrentSavesKeepBothChanges(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	b := &fakeBuilder{}
	m := NewManager(st, w, &fakeLive{}, b.build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.50"), User: ptr("admin"), Auto: ptr(false)}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, u := range []Update{{Auto: ptr(true)}, {Host: ptr("192.168.1.51")}} {
			wg.Go(func() {
				<-start
				_, err := m.Save(ctx, u)
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if s := m.Settings(); s.Host != "192.168.1.51" || !s.Auto {
			t.Fatalf("round %d: settings = %+v; want both changes", i, s)
		}
		stored, err := st.Settings(ctx, "camera.")
		if err != nil {
			t.Fatal(err)
		}
		if stored[keyHost] != "192.168.1.51" || stored[keyAuto] != "1" {
			t.Fatalf("round %d: stored = %v; want both changes", i, stored)
		}
		if !w.Status().Auto || b.last().Host != "192.168.1.51" {
			t.Fatalf("round %d: watcher auto %v, camera %+v; want both changes applied", i, w.Status().Auto, b.last())
		}
	}
}

func TestReplacedCamerasAreClosed(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	b := &fakeBuilder{firstErr: fmt.Errorf("%w: login failed", camera.ErrAuth)}
	m := NewManager(st, w, &fakeLive{}, b.build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ctx, setupUpdate()); err != nil {
		t.Fatal(err)
	}
	w.poll(ctx) // wrong password
	m.Reconnect()
	if got := b.closes(); got != "[1 0]" {
		t.Fatalf("closes after Reconnect = %s; want the first camera closed once", got)
	}
	w.poll(ctx) // online
	if _, err := m.Save(ctx, Update{Auto: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if got := b.closes(); got != "[1 0]" {
		t.Fatalf("closes after switching auto = %s; want the working camera kept open", got)
	}
	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.51"), Password: ptr(setupPassword)}); err != nil {
		t.Fatal(err)
	}
	if got := b.closes(); got != "[1 1 0]" {
		t.Fatalf("closes after a new address = %s; want the old camera closed", got)
	}
	if _, err := m.Save(ctx, Update{Host: ptr(""), User: ptr(""), Password: ptr("x")}); err != nil {
		t.Fatal(err)
	}
	if got := b.closes(); got != "[1 1 1]" || w.Camera() != nil {
		t.Fatalf("closes after removing the camera = %s, camera %v; want the last one closed", got, w.Camera())
	}
	m.Close() // no camera: nothing to close
}

func TestCloseClosesTheCurrentCamera(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	b := &fakeBuilder{}
	m := NewManager(st, w, &fakeLive{}, b.build, testkit.Scope)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(ctx, setupUpdate()); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if got := b.closes(); got != "[1]" {
		t.Fatalf("closes = %s; want the current camera closed", got)
	}
}

func TestTheFixedCameraIsClosedOnlyByClose(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	fixed := &closeCounter{Fake: camera.NewFake(camera.DemoStill())}
	m := NewManager(st, w, &fakeLive{}, func(camera.Settings) camera.Camera { t.Fatal("must not build a camera"); return nil }, testkit.Scope)
	m.UseFixed(fixed)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	m.Reconnect()
	m.Reconnect()
	if _, err := m.Save(ctx, setupUpdate()); err != nil {
		t.Fatal(err)
	}
	if n := fixed.closed(); n != 0 {
		t.Fatalf("the fixed camera was closed %d times while in use", n)
	}
	m.Close()
	if n := fixed.closed(); n != 1 {
		t.Fatalf("closes = %d; want 1 on shutdown", n)
	}

	// The real fake camera's Close does nothing, however often it is called.
	plain := camera.NewFake(camera.DemoStill())
	m.UseFixed(plain)
	m.Reconnect()
	m.Close()
	m.Close()
	if _, err := plain.Detect(ctx); err != nil {
		t.Fatalf("the fake camera after Close: %v", err)
	}
}

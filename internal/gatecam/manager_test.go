package gatecam

import (
	"errors"
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
	m := NewManager(st, w, live, build)
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

	// An empty password keeps the saved one.
	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.51"), Password: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 || built[1].Host != "192.168.1.51" || built[1].Password != "pw-secret" {
		t.Fatalf("built = %+v", built)
	}

	// A new manager over the same database loads the same settings.
	m2 := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, build)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s := m2.Settings(); s.Host != "192.168.1.51" || s.User != "admin" || s.Password != "pw-secret" || !s.Auto {
		t.Fatalf("loaded settings = %+v", s)
	}
}

func TestManagerRejectsAnythingButAnAddress(t *testing.T) {
	st, _ := testkit.Open(t)
	m := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) })
	for _, host := range []string{"http://192.168.1.50", "192.168.1.50/cgi-bin", "admin@192.168.1.50", "192.168.1.50:99999", "cam era", "fe80::1"} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); !errors.Is(err, ErrBadSettings) {
			t.Errorf("Save(host %q) = %v; want ErrBadSettings", host, err)
		}
	}
	for _, host := range []string{"192.168.1.50", "192.168.1.50:8080", "gate-cam.local", ""} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); err != nil {
			t.Errorf("Save(host %q) = %v", host, err)
		}
	}
}

func TestManagerUseFixedIgnoresSavedSettings(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	fake := camera.NewFake(camera.DemoStill())
	m := NewManager(st, w, &fakeLive{}, func(s camera.Settings) camera.Camera { t.Fatal("must not build a camera"); return nil })
	m.UseFixed(fake)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Camera() != camera.Camera(fake) {
		t.Fatal("the fixed camera is not in use")
	}
}

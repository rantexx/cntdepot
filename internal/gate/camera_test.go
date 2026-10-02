package gate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"depot/internal/recognition"
	"depot/internal/store"
)

// still is a fake camera still: the JPEG signature satisfies content
// sniffing, and fake engines can be scripted by the exact bytes.
func still(name string) []byte { return []byte("\xff\xd8\xff\xe0" + name) }

// startWithHook starts the service with a read hook that reports every
// finished read on the returned channel.
func startWithHook(t *testing.T, h *harness) chan recognition.Frame {
	t.Helper()
	got := make(chan recognition.Frame, 32)
	h.svc.SetReadHook(func(moveID, photoID string, f recognition.Frame) { got <- f })
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // runs before the harness cleanup waits for the workers
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	return got
}

func nextRead(t *testing.T, ch chan recognition.Frame) recognition.Frame {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no read finished within 5 s")
		return recognition.Frame{}
	}
}

func TestCameraFramesAreReadOnlyWhenChosen(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)

	m, err := h.svc.NewCameraMove(ctx, "camera")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != store.SourceCamera || m.Status != store.StatusCapturing {
		t.Fatalf("new camera move = %+v", m)
	}
	p1, err := h.svc.AddFrame(ctx, m.ID, still("f1"), 3840, 2160)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AddFrame(ctx, m.ID, still("f2"), 3840, 2160); err != nil {
		t.Fatal(err)
	}
	if !p1.Deferred || p1.AISHA256 != p1.SHA256 || p1.Width != 3840 {
		t.Fatalf("frame photo = %+v; want deferred, original as engine copy", p1)
	}
	if fake.Calls() != 0 {
		t.Fatalf("engine calls = %d before any photo was chosen", fake.Calls())
	}

	if err := h.svc.ReadFrame(ctx, p1.ID); err != nil {
		t.Fatal(err)
	}
	f := nextRead(t, reads)
	if len(f.Candidates) == 0 || f.Candidates[0].Number != "CSQU3054383" || !f.Candidates[0].CheckDigitValid {
		t.Fatalf("hook frame = %+v", f)
	}
	// The visit is still open, so the move waits for more photos.
	waitStatus(t, h.store, m.ID, store.StatusCapturing)

	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.Number != "CSQU3054383" || got.Decision != string(recognition.DecisionReview) {
		t.Fatalf("settled move = %+v; want the number, decided by a person", got)
	}
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1 (the deferred photo is never read)", fake.Calls())
	}
}

func TestCameraMovesNeverAutoAccept(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.99)}
	h := newHarness(t, fake, Config{AutoAcceptEnabled: true}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	for _, name := range []string{"a", "b"} {
		p, err := h.svc.AddFrame(ctx, m.ID, still(name), 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		nextRead(t, reads)
	}
	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if !strings.Contains(got.Reason, "person") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestCameraMoveWithoutAnyNumberIsNoContainer(t *testing.T) {
	fake := &recognition.Fake{Reading: recognition.Reading{Visible: "none"}}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("car"), 0, 0)
	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)
	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNoContainer)
	if !strings.Contains(got.Reason, "no container") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestCameraMoveWithAFailedReadNeedsReview(t *testing.T) {
	fake := &recognition.Fake{Err: recognition.ErrUnavailable}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("x"), 0, 0)
	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if f := nextRead(t, reads); f.Error == "" {
		t.Fatalf("hook frame = %+v; want the engine error", f)
	}
	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview) // an outage is not proof of an empty lane
}

// detailRecorder is an engine that records the Detail of every image.
type detailRecorder struct {
	mu      sync.Mutex
	details []string
}

func (d *detailRecorder) Name() string  { return "rec" }
func (d *detailRecorder) Model() string { return "rec-1" }
func (d *detailRecorder) Read(ctx context.Context, img recognition.Image) (recognition.Attempt, error) {
	d.mu.Lock()
	d.details = append(d.details, img.Detail)
	d.mu.Unlock()
	return recognition.Attempt{Engine: "rec", Model: "rec-1", Reading: reading("CSQU3054383", 0.9)}, nil
}

func (d *detailRecorder) got() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.details...)
}

func TestCameraStillsAreReadInUltraHighDetail(t *testing.T) {
	rec := &detailRecorder{}
	h := newHarness(t, rec, Config{}, false)
	reads := startWithHook(t, h)

	phone, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, phone.ID, "rear", "phone")
	nextRead(t, reads)

	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("cam"), 0, 0)
	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)

	if _, err := h.svc.ReadStill(ctx, still("page")); err != nil {
		t.Fatal(err)
	}
	want := []string{"", recognition.DetailUltraHigh, recognition.DetailUltraHigh}
	if got := rec.got(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("details = %q; want %q", got, want)
	}
	if _, err := h.svc.ReadStill(ctx, []byte("not a jpeg")); !errors.Is(err, ErrBadPhoto) {
		t.Fatalf("ReadStill(non-JPEG) = %v; want ErrBadPhoto", err)
	}
}

func TestAddFrameRejectsBadInputAndClosedMoves(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	if _, err := h.svc.AddFrame(ctx, m.ID, []byte("png?"), 0, 0); !errors.Is(err, ErrBadPhoto) {
		t.Fatalf("AddFrame(non-JPEG) = %v; want ErrBadPhoto", err)
	}
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "refuse", By: "clerk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AddFrame(ctx, m.ID, still("late"), 0, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("AddFrame(resolved move) = %v; want ErrClosed", err)
	}
}

func TestStartSettlesCameraVisitsCutShortByARestart(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	// Before the restart: move A had one photo read and one deferred; move B
	// had one photo chosen but not read yet.
	a, _ := h.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	sha, _ := h.blobs.Put(still("a1"))
	pa, _ := h.store.AddPhoto(ctx, store.Photo{MoveID: a.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1}, "camera")
	raw, _ := json.Marshal(reading("CSQU3054383", 0.9))
	if _, err := h.store.AddAttempt(ctx, store.Attempt{MoveID: a.ID, PhotoID: pa.ID, Engine: "fake", Model: "fake-1", Reading: raw, Raw: string(raw)}); err != nil {
		t.Fatal(err)
	}
	sha2, _ := h.blobs.Put(still("a2"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: a.ID, Role: "rear", SHA256: sha2, AISHA256: sha2, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true}, "camera"); err != nil {
		t.Fatal(err)
	}
	b, _ := h.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	sha3, _ := h.blobs.Put(still("b1"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: b.ID, Role: "rear", SHA256: sha3, AISHA256: sha3, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1}, "camera"); err != nil {
		t.Fatal(err)
	}

	startWithHook(t, h)
	waitStatus(t, h.store, a.ID, store.StatusNeedsReview)
	waitStatus(t, h.store, b.ID, store.StatusNeedsReview)
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1 (B's chosen photo; never A's deferred one)", fake.Calls())
	}
}

func TestRereadSkipsDeferredPhotos(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("r1"), 0, 0)
	if _, err := h.svc.AddFrame(ctx, m.ID, still("r2"), 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)
	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reread(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 2 {
		t.Fatalf("engine calls = %d; want 2 (the chosen photo twice, never the deferred one)", fake.Calls())
	}
}

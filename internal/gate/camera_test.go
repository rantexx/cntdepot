package gate

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"depot/internal/recognition"
	"depot/internal/store"

	"github.com/jackc/pgx/v5"
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
	pa, _ := h.store.AddPhoto(ctx, store.Photo{MoveID: a.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "camera")
	raw, _ := json.Marshal(reading("CSQU3054383", 0.9))
	if _, err := h.store.AddAttempt(ctx, store.Attempt{MoveID: a.ID, PhotoID: pa.ID, Engine: "fake", Model: "fake-1", Reading: raw, Raw: string(raw)}); err != nil {
		t.Fatal(err)
	}
	sha2, _ := h.blobs.Put(still("a2"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: a.ID, Role: "rear", SHA256: sha2, AISHA256: sha2, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true, ReadEnabled: true}, "camera"); err != nil {
		t.Fatal(err)
	}
	b, _ := h.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	sha3, _ := h.blobs.Put(still("b1"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: b.ID, Role: "rear", SHA256: sha3, AISHA256: sha3, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "camera"); err != nil {
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

func TestCameraMoveWithoutAnyReadIsNotNoContainer(t *testing.T) {
	fake := &recognition.Fake{Reading: recognition.Reading{Visible: "none"}}
	h := newHarness(t, fake, Config{}, false)
	startWithHook(t, h)
	// Every still failed: no photo at all.
	empty, _ := h.svc.NewCameraMove(ctx, "camera")
	// Stills were taken, but none was ever chosen for reading.
	unread, _ := h.svc.NewCameraMove(ctx, "camera")
	if _, err := h.svc.AddFrame(ctx, unread.ID, still("u1"), 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{empty.ID, unread.ID} {
		if err := h.svc.SettleCamera(ctx, id); err != nil {
			t.Fatal(err)
		}
		got := waitStatus(t, h.store, id, store.StatusNeedsReview)
		if got.Reason != "no photo was read" {
			t.Fatalf("reason = %q; want no photo was read", got.Reason)
		}
	}
	if fake.Calls() != 0 {
		t.Fatalf("engine calls = %d; want 0", fake.Calls())
	}
}

func TestRestartBeforeTheFirstReadNeedsReview(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: recognition.Reading{Visible: "none"}}, Config{}, false)
	// Before the restart the visit stored a still but never chose one.
	m, _ := h.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	sha, _ := h.blobs.Put(still("d1"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true, ReadEnabled: true}, "camera"); err != nil {
		t.Fatal(err)
	}
	startWithHook(t, h)
	if got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview); got.Reason != "no photo was read" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestCameraMoveWithAContainerButNoLegibleNumberNeedsReview(t *testing.T) {
	for name, r := range map[string]recognition.Reading{
		"seen":      {Visible: "partial"},
		"type code": {Visible: "none", TypeCode: "45G1"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, &recognition.Fake{Reading: r}, Config{}, false)
			reads := startWithHook(t, h)
			m, _ := h.svc.NewCameraMove(ctx, "camera")
			p, _ := h.svc.AddFrame(ctx, m.ID, still("box"), 0, 0)
			if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			nextRead(t, reads)
			if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
				t.Fatal(err)
			}
			waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
		})
	}
}

func TestOpenCameraVisitNeverRecordsAutoAccept(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.99)}
	h := newHarness(t, fake, Config{AutoAcceptEnabled: true}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("open"), 0, 0)
	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)
	got := waitStatus(t, h.store, m.ID, store.StatusCapturing) // the visit is still open
	if got.Decision == string(recognition.DecisionAutoAccept) || !strings.Contains(got.Reason, "person") {
		t.Fatalf("open visit: decision %q, reason %q", got.Decision, got.Reason)
	}
	events, err := h.store.Events(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "move.read" && strings.Contains(string(e.Payload), string(recognition.DecisionAutoAccept)) {
			t.Fatalf("move.read event says auto_accept: %s", e.Payload)
		}
	}
}

func TestCameraConfidenceIsOneStillsConfidence(t *testing.T) {
	fake := &recognition.Fake{ByImage: map[string]recognition.Reading{
		string(still("c1")): reading("CSQU3054383", 0.95),
		string(still("c2")): reading("CSQU3054383", 0.90),
	}}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	for _, name := range []string{"c1", "c2"} {
		p, _ := h.svc.AddFrame(ctx, m.ID, still(name), 0, 0)
		if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
		nextRead(t, reads)
	}
	if err := h.svc.SettleCamera(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if math.Abs(got.Confidence-0.95) > 1e-6 { // the column is REAL (float4)
		t.Fatalf("confidence = %v; want 0.95, the best single still (no bonus for stills of one side)", got.Confidence)
	}
	var res recognition.Result
	if err := json.Unmarshal(got.Result, &res); err != nil || res.Confidence != 0.95 {
		t.Fatalf("stored result confidence = %v, %v", res.Confidence, err)
	}
}

func TestCameraPhotosKeepUltraHighDetailOnRereadAndRestart(t *testing.T) {
	rec := &detailRecorder{}
	h := newHarness(t, rec, Config{Workers: 1}, false)
	// Before the restart: a camera photo and a phone photo were chosen but not read.
	cam, _ := h.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	sha, _ := h.blobs.Put(still("restart"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: cam.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "camera"); err != nil {
		t.Fatal(err)
	}
	phone, _ := h.store.CreateMove(ctx, "1", false, "clerk")
	sha2, _ := h.blobs.Put([]byte("phone"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: phone.ID, Role: "rear", SHA256: sha2, AISHA256: sha2, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk"); err != nil {
		t.Fatal(err)
	}
	reads := startWithHook(t, h)
	nextRead(t, reads)
	nextRead(t, reads)
	waitStatus(t, h.store, cam.ID, store.StatusNeedsReview)
	if err := h.svc.Reread(ctx, cam.ID); err != nil {
		t.Fatal(err)
	}
	nextRead(t, reads)
	got := rec.got()
	ultra := 0
	for _, d := range got {
		if d == recognition.DetailUltraHigh {
			ultra++
		}
	}
	if len(got) != 3 || ultra != 2 {
		t.Fatalf("details = %q; want the camera photo in ultra-high twice (restart, re-read) and the phone photo once by default", got)
	}
}

func TestReadFrameQueuesThePhotoEvenWhenTheStatusCannotBeSet(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	reads := startWithHook(t, h)
	m, _ := h.svc.NewCameraMove(ctx, "camera")
	p, _ := h.svc.AddFrame(ctx, m.ID, still("s1"), 0, 0)

	// Make every "reading" status write fail, as a full disk or a locked
	// database would, while choosing the photo still works.
	conn, err := pgx.Connect(context.Background(), h.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(context.Background(), `
		CREATE FUNCTION fail_reading() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected failure'; END $$;
		CREATE TRIGGER fail_reading BEFORE UPDATE OF status ON gate_moves
		FOR EACH ROW WHEN (NEW.status = 'reading') EXECUTE FUNCTION fail_reading()`)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetStatus(ctx, m.ID, store.StatusReading); err == nil {
		t.Fatal("the injected failure does not work")
	}

	if err := h.svc.ReadFrame(ctx, p.ID); err != nil {
		t.Fatalf("ReadFrame = %v; want the photo queued anyway", err)
	}
	if f := nextRead(t, reads); len(f.Candidates) == 0 {
		t.Fatalf("hook frame = %+v", f)
	}
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1", fake.Calls())
	}
}

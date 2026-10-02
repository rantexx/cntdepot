package gatecam

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/recognition"
	"depot/internal/store"
)

var ctx = context.Background()

// fakeGate records what the watcher asks of the gate service.
type fakeGate struct {
	mu      sync.Mutex
	moves   []string
	frames  map[string][]string // move ID → photo IDs
	reads   []string
	settled []string
	photoN  int
	addErr  error // AddFrame fails with it; gate.ErrClosed as for a move a person already resolved
}

func newFakeGate() *fakeGate { return &fakeGate{frames: map[string][]string{}} }

func (g *fakeGate) NewCameraMove(ctx context.Context, actor string) (store.Move, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := fmt.Sprintf("m%d", len(g.moves)+1)
	g.moves = append(g.moves, id)
	return store.Move{ID: id, Source: store.SourceCamera}, nil
}

func (g *fakeGate) AddFrame(ctx context.Context, moveID string, jpeg []byte, w, h int) (store.Photo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.addErr != nil {
		return store.Photo{}, g.addErr
	}
	g.photoN++
	id := fmt.Sprintf("p%d", g.photoN)
	g.frames[moveID] = append(g.frames[moveID], id)
	return store.Photo{ID: id, MoveID: moveID, Deferred: true}, nil
}

func (g *fakeGate) ReadFrame(ctx context.Context, photoID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads = append(g.reads, photoID)
	return nil
}

func (g *fakeGate) SettleCamera(ctx context.Context, moveID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settled = append(g.settled, moveID)
	return nil
}

func (g *fakeGate) snapshot() (moves, reads, settled []string, frames map[string]int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	frames = map[string]int{}
	for k, v := range g.frames {
		frames[k] = len(v)
	}
	return append([]string(nil), g.moves...), append([]string(nil), g.reads...), append([]string(nil), g.settled...), frames
}

var t0 = time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

var (
	validRead = recognition.Frame{Role: "rear", Candidates: []recognition.Candidate{{Number: "CSQU3054383", Confidence: 0.9, CheckDigitValid: true}}}
	emptyRead = recognition.Frame{Role: "rear"}
)

func newTestWatcher(t *testing.T, cfg Config) (*Watcher, *fakeGate, *camera.Fake) {
	t.Helper()
	g := newFakeGate()
	cam := camera.NewFake([]byte("still"))
	w := New(g, cfg)
	w.SetCamera(cam)
	w.SetAuto(true)
	return w, g, cam
}

func TestVehicleStartsAVisitAndQuietEndsIt(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	for _, s := range []float64{0, 0.5, 1, 1.5, 2} {
		w.step(ctx, at(s), true, cam)
	}
	w.step(ctx, at(2.5), false, cam)
	w.step(ctx, at(3), false, cam) // still within 3 s of the last sighting: keeps shooting
	moves, reads, _, frames := g.snapshot()
	if len(moves) != 1 || frames["m1"] != 4 {
		t.Fatalf("moves %v frames %v; want 1 move with 4 stills", moves, frames)
	}
	if strings.Join(reads, ",") != "p1,p2" {
		t.Fatalf("reads = %v; want p1,p2 (two in flight)", reads)
	}
	w.step(ctx, at(5), false, cam) // 3 s since the vehicle was last seen at 2 s
	if w.visit != nil {
		t.Fatal("visit still running after 3 s without a vehicle")
	}
	w.step(ctx, at(5.5), true, cam) // the next truck
	if moves, _, _, _ := g.snapshot(); len(moves) != 2 {
		t.Fatalf("moves = %v; want a second visit right away", moves)
	}
}

func TestPhotoLimitEndsTheVisitAndWaitsForTheVehicleToLeave(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	for s := 0; s <= 14; s++ {
		w.step(ctx, at(float64(s)), true, cam)
	}
	if w.visit != nil {
		t.Fatal("visit still running after 15 stills")
	}
	for s := 15; s <= 20; s++ { // the same truck is still standing there
		w.step(ctx, at(float64(s)), true, cam)
	}
	for _, s := range []float64{21, 22, 23.5} {
		w.step(ctx, at(s), false, cam)
	}
	w.step(ctx, at(23.6), true, cam) // gone only 2.6 s: not yet
	if moves, _, _, _ := g.snapshot(); len(moves) != 1 {
		t.Fatalf("moves = %v; want no new visit for the same truck", moves)
	}
	for _, s := range []float64{24, 25, 27} {
		w.step(ctx, at(s), false, cam)
	}
	w.step(ctx, at(27.5), true, cam)
	moves, _, _, frames := g.snapshot()
	if len(moves) != 2 || frames["m1"] != 15 {
		t.Fatalf("moves %v frames %v", moves, frames)
	}
}

func TestVisitIsCappedEvenWhenNoStillWorks(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	cam.SetSnapErr(camera.ErrUnreachable)
	for s := 0; s < 30; s++ {
		w.step(ctx, at(float64(s)), true, cam)
	}
	if w.visit == nil {
		t.Fatal("visit ended before the 30 s cap")
	}
	w.step(ctx, at(30), true, cam)
	if w.visit != nil {
		t.Fatal("visit still running at the 30 s cap")
	}
	if _, _, settled, _ := g.snapshot(); strings.Join(settled, ",") != "m1" {
		t.Fatalf("settled = %v; want the empty visit settled", settled)
	}
}

func TestTwoAgreeingReadsStopReading(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	w.step(ctx, at(1), true, cam)
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p1", frame: validRead})
	w.step(ctx, at(2), true, cam) // p3 is taken and read: one read slot was free
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p2", frame: validRead})
	if _, _, settled, _ := g.snapshot(); len(settled) != 0 {
		t.Fatalf("settled while p3 is still being read: %v", settled)
	}
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p3", frame: emptyRead})
	w.step(ctx, at(3), true, cam) // more stills for training, but no more reads
	_, reads, settled, frames := g.snapshot()
	if strings.Join(reads, ",") != "p1,p2,p3" || strings.Join(settled, ",") != "m1" || frames["m1"] != 4 {
		t.Fatalf("reads %v settled %v frames %v", reads, settled, frames)
	}
}

func TestReadsSpreadEvenlyAfterTheVisitAndStopAtEight(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxPhotos = 10
	w, g, cam := newTestWatcher(t, cfg)
	for s := 0; s <= 9; s++ { // ten stills; p1 and p2 are being read the whole time
		w.step(ctx, at(float64(s)), true, cam)
	}
	if w.visit != nil {
		t.Fatal("visit should have ended at 10 stills")
	}
	for _, p := range []string{"p1", "p2", "p10", "p6", "p4", "p8", "p3", "p5"} {
		w.handleRead(ctx, readResult{moveID: "m1", photoID: p, frame: emptyRead})
	}
	_, reads, settled, _ := g.snapshot()
	if got := strings.Join(reads, ","); got != "p1,p2,p10,p6,p4,p8,p3,p5" {
		t.Fatalf("reads = %s; want the newest two, then spread evenly, eight in all", got)
	}
	if strings.Join(settled, ",") != "m1" {
		t.Fatalf("settled = %v", settled)
	}
}

func TestEveryStillIsReadWhenThereAreFewerThanEight(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	w.step(ctx, at(1), true, cam)
	w.step(ctx, at(2), true, cam)
	w.step(ctx, at(5), false, cam) // ends: 3 stills, p1 and p2 in flight
	for _, p := range []string{"p1", "p2", "p3"} {
		w.handleRead(ctx, readResult{moveID: "m1", photoID: p, frame: emptyRead})
	}
	_, reads, settled, _ := g.snapshot()
	if strings.Join(reads, ",") != "p1,p2,p3" || strings.Join(settled, ",") != "m1" {
		t.Fatalf("reads %v settled %v", reads, settled)
	}
}

func TestManualCaptureTakesTenStills(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.SetAuto(false)
	id, err := w.startManual(ctx, at(0))
	if err != nil || id != "m1" {
		t.Fatalf("startManual = %q, %v", id, err)
	}
	if _, err := w.startManual(ctx, at(0.5)); !errors.Is(err, ErrBusy) {
		t.Fatalf("second startManual = %v; want ErrBusy", err)
	}
	for s := 1; s <= 8; s++ {
		w.step(ctx, at(float64(s)), false, cam) // no vehicle: a manual visit keeps going
	}
	if w.visit == nil {
		t.Fatal("manual visit ended early")
	}
	w.step(ctx, at(9), false, cam)
	if _, _, _, frames := g.snapshot(); frames["m1"] != 10 || w.visit != nil {
		t.Fatalf("frames = %v, visit running %v; want 10 stills and done", frames, w.visit != nil)
	}
}

func TestNothingStartsWithAutomaticPhotosOffOrWithoutACamera(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.SetAuto(false)
	w.step(ctx, at(0), true, cam)
	if moves, _, _, _ := g.snapshot(); len(moves) != 0 {
		t.Fatalf("moves = %v with automatic photos off", moves)
	}
	none := New(newFakeGate(), DefaultConfig())
	if _, err := none.startManual(ctx, at(0)); !errors.Is(err, ErrNoCamera) {
		t.Fatalf("startManual without camera = %v; want ErrNoCamera", err)
	}
}

func TestAResolvedMoveEndsTheVisit(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	g.mu.Lock()
	g.addErr = fmt.Errorf("store still: %w", gate.ErrClosed)
	g.mu.Unlock()
	w.step(ctx, at(1), true, cam)
	if w.visit != nil {
		t.Fatal("visit still running after its move was closed")
	}
}

func TestAStillThatCannotBeStoredIsSkipped(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	g.mu.Lock()
	g.addErr = errors.New("disk full")
	g.mu.Unlock()
	w.step(ctx, at(1), true, cam)
	if w.visit == nil {
		t.Fatal("a store failure ended the visit")
	}
	g.mu.Lock()
	g.addErr = nil
	g.mu.Unlock()
	w.step(ctx, at(2), true, cam)
	if _, _, _, frames := g.snapshot(); frames["m1"] != 2 || w.visit == nil {
		t.Fatalf("frames = %v; want the visit to carry on with 2 stills", frames)
	}
}

// clockAt makes the watcher's clock read at(s).
func clockAt(w *Watcher, s *float64) { w.now = func() time.Time { return at(*s) } }

func TestDetectOutageMidVisitKeepsOneMove(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	var s float64
	clockAt(w, &s)
	cam.SetVehicle(true)
	for s = 0; s <= 1; s += 0.5 {
		w.poll(ctx)
	}
	if w.visit == nil {
		t.Fatal("no visit started")
	}
	cam.SetDetectErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	for s = 1.5; s <= 8; s += 0.5 { // the truck stands still while the camera does not answer
		w.poll(ctx)
	}
	if w.Status().State != StateOffline {
		t.Fatalf("status = %+v; want offline", w.Status())
	}
	cam.SetDetectErr(nil)
	for s = 8.5; s <= 10; s += 0.5 {
		w.poll(ctx)
	}
	cam.SetVehicle(false)
	for s = 10.5; s <= 20; s += 0.5 {
		w.poll(ctx)
	}
	moves, _, _, frames := g.snapshot()
	if len(moves) != 1 || w.visit != nil {
		t.Fatalf("moves = %v, visit running %v; want one move for the one truck", moves, w.visit != nil)
	}
	if frames["m1"] < 10 {
		t.Fatalf("frames = %v; want stills taken through the outage", frames)
	}
}

func TestDetectOutageWithoutAVisitDoesNotRearm(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	var s float64
	clockAt(w, &s)
	cam.SetVehicle(true)
	for s = 0; s <= 30; s += 0.5 { // a truck parked long after its visit ended
		w.poll(ctx)
	}
	if w.visit != nil {
		t.Fatal("visit still running")
	}
	cam.SetDetectErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	for s = 30.5; s <= 40; s += 0.5 {
		w.poll(ctx)
	}
	cam.SetDetectErr(nil) // back, and the same truck is still there
	for s = 40.5; s <= 42; s += 0.5 {
		w.poll(ctx)
	}
	if moves, _, _, _ := g.snapshot(); len(moves) != 1 {
		t.Fatalf("moves = %v; an outage must not count as the truck leaving", moves)
	}
}

func TestPollWakesUpForTheNextStill(t *testing.T) {
	w, _, cam := newTestWatcher(t, DefaultConfig())
	s := 0.0
	clockAt(w, &s)
	cam.SetVehicle(true)
	for _, step := range []struct {
		s    float64
		want time.Duration
	}{
		{0, 500 * time.Millisecond},    // the first still; the next is due at 1 s
		{0.75, 250 * time.Millisecond}, // wake up for it, not 500 ms later
		{1, 500 * time.Millisecond},    // the second still
		{1.75, 250 * time.Millisecond},
	} {
		s = step.s
		if d := w.poll(ctx); d != step.want {
			t.Fatalf("poll at %v s waits %v; want %v", s, d, step.want)
		}
	}
	cam.SetDetectErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	s = 1.875
	if d := w.poll(ctx); d != 125*time.Millisecond {
		t.Fatalf("wait during an outage mid-visit = %v; want 125ms, for the next still", d)
	}
	s = 2
	w.poll(ctx)
	if n := len(w.visit.photos); n != 3 {
		t.Fatalf("stills = %d; want 3, one a second", n)
	}
}

func TestInfoIsAskedOnlyAfterTheCameraAnswers(t *testing.T) {
	w, _, cam := newTestWatcher(t, DefaultConfig())
	cam.SetDetectErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	counting := &infoCounter{Fake: cam}
	w.SetCamera(counting)
	if d := w.poll(ctx); d != w.cfg.Retry || counting.infos != 0 {
		t.Fatalf("offline poll = %v with %d Info calls; want Retry and none", d, counting.infos)
	}
	cam.SetDetectErr(nil)
	w.poll(ctx)
	w.poll(ctx)
	if counting.infos != 1 || w.Status().Info.Model != "Fake camera" {
		t.Fatalf("Info calls = %d, status %+v; want one, once online", counting.infos, w.Status())
	}
}

type infoCounter struct {
	*camera.Fake
	infos int
}

func (c *infoCounter) Info(ctx context.Context) (camera.Info, error) {
	c.infos++
	return c.Fake.Info(ctx)
}

func TestReadsTheVisitDidNotAskForAreIgnored(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	w.step(ctx, at(1), true, cam) // p1 and p2 in flight
	// A person's re-read of the move and a stray read: neither is the visit's.
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p7", frame: validRead})
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p8", frame: validRead})
	w.step(ctx, at(2), true, cam)
	if w.visit == nil || w.visit.stopped || len(w.visit.inFlight) != 2 {
		t.Fatalf("visit = %+v; untracked reads were counted", w.visit)
	}
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p1", frame: validRead})
	w.handleRead(ctx, readResult{moveID: "m1", photoID: "p1", frame: validRead}) // the same photo twice counts once
	if w.visit.stopped {
		t.Fatal("one photo read twice counted as two agreeing photos")
	}
	if _, reads, _, _ := g.snapshot(); strings.Join(reads, ",") != "p1,p2,p3" {
		t.Fatalf("reads = %v; want p3 sent once p1 finished", reads)
	}
}

func TestSetCameraKeepsTheRunningVisit(t *testing.T) {
	w, _, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	w.SetCamera(camera.NewFake([]byte("still")))
	if got := w.Status().VisitMove; got != "m1" {
		t.Fatalf("visit move after SetCamera = %q; want m1", got)
	}
}

// swappingCamera replaces itself in the watcher while answering Detect.
type swappingCamera struct {
	*camera.Fake
	swap func()
}

func (c *swappingCamera) Detect(ctx context.Context) (camera.Detection, error) {
	d, err := c.Fake.Detect(ctx)
	c.swap()
	return d, err
}

func TestALateAnswerFromAReplacedCameraIsIgnored(t *testing.T) {
	w, g, _ := newTestWatcher(t, DefaultConfig())
	old := camera.NewFake([]byte("old"))
	old.SetVehicle(true)
	w.SetCamera(&swappingCamera{Fake: old, swap: func() { w.SetCamera(camera.NewFake([]byte("new"))) }})
	w.poll(ctx)
	if moves, _, _, _ := g.snapshot(); len(moves) != 0 || w.visit != nil {
		t.Fatalf("moves = %v; the replaced camera's answer started a visit", moves)
	}
	if w.Status().State != StateConnecting {
		t.Fatalf("status = %+v; want the new camera still connecting", w.Status())
	}
}

func TestOnReadNeverBlocks(t *testing.T) {
	w, g, cam := newTestWatcher(t, DefaultConfig())
	w.step(ctx, at(0), true, cam)
	w.step(ctx, at(1), true, cam) // p1 and p2 in flight
	w.SetCamera(nil)              // so Run's polls take no stills
	s := 1.5
	clockAt(w, &s)

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := range 1000 { // far more than any buffer, with Run not started
			w.OnRead("other", fmt.Sprintf("x%d", i), emptyRead)
		}
		w.OnRead("m1", "p1", validRead)
		w.OnRead("m1", "p2", validRead)
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRead blocked while Run was not running")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(runCtx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, settled, _ := g.snapshot(); strings.Join(settled, ",") == "m1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not handle the queued reads")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for range 1000 {
			w.OnRead("m1", "p1", validRead)
		}
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRead blocked after Run stopped")
	}
}

func TestPollTracksTheCameraState(t *testing.T) {
	w, _, cam := newTestWatcher(t, DefaultConfig())
	if d := w.poll(ctx); d != w.cfg.Poll || w.Status().State != StateOnline || w.Status().Info.Model != "Fake camera" {
		t.Fatalf("poll = %v, status %+v", d, w.Status())
	}
	cam.SetDetectErr(fmt.Errorf("%w: timeout", camera.ErrUnreachable))
	if d := w.poll(ctx); d != w.cfg.Retry || w.Status().State != StateOffline || w.Status().Error == "" {
		t.Fatalf("offline poll = %v, status %+v", d, w.Status())
	}
	cam.SetDetectErr(fmt.Errorf("%w: login failed", camera.ErrAuth))
	w.poll(ctx)
	if w.Status().State != StateAuth {
		t.Fatalf("status = %+v; want auth", w.Status())
	}
	n := cam.Detects()
	w.poll(ctx)
	if cam.Detects() != n {
		t.Fatal("polling continued after a wrong password")
	}
	fresh := camera.NewFake([]byte("still"))
	w.SetCamera(fresh)
	if w.Status().State != StateConnecting {
		t.Fatalf("status after SetCamera = %+v", w.Status())
	}
	w.poll(ctx)
	if w.Status().State != StateOnline {
		t.Fatalf("status = %+v; want online with the new camera", w.Status())
	}
}

func TestCaptureGoesThroughRun(t *testing.T) {
	w, _, _ := newTestWatcher(t, DefaultConfig())
	w.SetAuto(false)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(runCtx); close(done) }()
	id, err := w.Capture(ctx)
	if err != nil || id != "m1" {
		t.Fatalf("Capture = %q, %v", id, err)
	}
	cancel()
	<-done
	if _, err := w.Capture(ctx); !errors.Is(err, ErrStopped) {
		t.Fatalf("Capture after Run ended = %v; want ErrStopped", err)
	}
}

func TestLetterHeight(t *testing.T) {
	for _, tc := range []struct {
		box  *recognition.Box
		w, h int
		want int
	}{
		{&recognition.Box{X0: 0.3, Y0: 0.2, X1: 0.62, Y1: 0.3}, 1280, 720, 72},  // one line
		{&recognition.Box{X0: 0.4, Y0: 0.4, X1: 0.5, Y1: 0.42}, 3840, 2160, 43}, // small and far
		{&recognition.Box{X0: 0.5, Y0: 0.1, X1: 0.52, Y1: 0.6}, 3840, 2160, 98}, // written top to bottom
		{nil, 3840, 2160, 0},
	} {
		if got := LetterHeight(tc.box, tc.w, tc.h); got != tc.want {
			t.Errorf("LetterHeight(%+v) = %d; want %d", tc.box, got, tc.want)
		}
	}
	for px, want := range map[int]string{0: "", 12: VerdictTooSmall, 20: VerdictBorderline, 29: VerdictBorderline, 30: VerdictGood, 72: VerdictGood} {
		if got := Verdict(px); got != want {
			t.Errorf("Verdict(%d) = %q; want %q", px, got, want)
		}
	}
}

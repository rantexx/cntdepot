// Package gatecam turns a fixed camera into gate-ins. The camera's vehicle
// detection starts a visit, the visit takes a still every second, and the
// stills are read until two agree on a container number (camera spec §3).
package gatecam

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log"
	"sync"
	"time"

	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/recognition"
	"depot/internal/store"
)

// Gate is what the watcher needs from the gate service.
type Gate interface {
	NewCameraMove(ctx context.Context, actor string) (store.Move, error)
	AddFrame(ctx context.Context, moveID string, jpeg []byte, width, height int) (store.Photo, error)
	ReadFrame(ctx context.Context, photoID string) error
	SettleCamera(ctx context.Context, moveID string) error
}

// Config holds the visit timings.
type Config struct {
	Poll         time.Duration // how often to ask "vehicle now?"
	Retry        time.Duration // how often while the camera is offline
	Interval     time.Duration // between stills during a visit
	MaxPhotos    int           // stills per visit
	Quiet        time.Duration // the vehicle gone this long ends a visit
	MaxVisit     time.Duration // no visit runs longer
	MaxReads     int           // engine reads per visit
	Parallel     int           // engine reads in flight per visit
	ManualPhotos int           // stills of a "take photos now" visit
	Timeout      time.Duration // per camera call
}

// DefaultConfig holds the camera spec's defaults.
func DefaultConfig() Config {
	return Config{
		Poll: 500 * time.Millisecond, Retry: 5 * time.Second, Interval: time.Second, MaxPhotos: 15,
		Quiet: 3 * time.Second, MaxVisit: 30 * time.Second, MaxReads: 8, Parallel: 2, ManualPhotos: 10,
		Timeout: 5 * time.Second,
	}
}

// Camera states shown on the Camera page.
const (
	StateUnconfigured = "unconfigured"
	StateConnecting   = "connecting"
	StateOnline       = "online"
	StateOffline      = "offline"
	StateAuth         = "auth" // wrong user name or password: polling stops until the settings change
)

// Status is the watcher's view of the camera.
type Status struct {
	State     string      `json:"state"`
	Since     time.Time   `json:"since"`
	Error     string      `json:"error,omitempty"`
	Vehicle   bool        `json:"vehicle"`
	Detection string      `json:"detection"`
	Info      camera.Info `json:"info"`
	Auto      bool        `json:"auto"`
	VisitMove string      `json:"visit_move,omitempty"`
}

var (
	ErrNoCamera     = errors.New("no camera is set up")
	ErrNotConnected = errors.New("the camera is not connected")
	ErrBusy         = errors.New("a visit is already running")
	ErrStopped      = errors.New("the camera watcher has stopped")
)

type readResult struct {
	moveID, photoID string
	frame           recognition.Frame
}

type captureReply struct {
	moveID string
	err    error
}

// Watcher runs one camera: it polls detection, runs visits and keeps the
// status. Run owns the visit state. The exported methods are safe to call
// from any goroutine.
type Watcher struct {
	gate Gate
	cfg  Config
	now  func() time.Time

	mu      sync.Mutex
	cam     camera.Camera
	auto    bool
	status  Status
	changed bool // camera replaced since the last poll

	readMu   sync.Mutex
	queued   []readResult  // finished reads waiting for Run
	notify   chan struct{} // capacity 1: tells Run that queued is not empty
	captures chan chan captureReply
	done     chan struct{}

	// Owned by Run (and by tests that call step and handleRead directly).
	visit    *visit            // the visit taking stills, if any
	settling map[string]*visit // ended visits still waiting for reads
	armed    bool              // a new visit may start
	offSince time.Time         // the vehicle has been absent since
	infoOK   bool
}

// visit is one vehicle pass.
type visit struct {
	moveID   string
	manual   bool
	start    time.Time
	lastSeen time.Time
	nextShot time.Time
	photos   []string        // photo IDs in capture order
	chosen   map[int]bool    // indexes of photos sent to the engine
	inFlight map[string]bool // photos this visit sent to the engine and still waits for
	reads    int
	ended    bool // no more stills will be taken
	stopped  bool // two photos agreed: no more reads
	settled  bool
	votes    map[string]int // a photo's lead valid number → photos that read it
}

// New builds a watcher without a camera; see SetCamera.
func New(g Gate, cfg Config) *Watcher {
	return &Watcher{
		gate: g, cfg: cfg, now: time.Now, status: Status{State: StateUnconfigured},
		notify: make(chan struct{}, 1), captures: make(chan chan captureReply), done: make(chan struct{}),
		settling: map[string]*visit{}, armed: true,
	}
}

// SetCamera replaces the camera. nil means none is set up. A running visit
// carries on, so its move stays in the status.
func (w *Watcher) SetCamera(c camera.Camera) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cam, w.changed = c, true
	state := StateConnecting
	if c == nil {
		state = StateUnconfigured
	}
	w.status = Status{State: state, Since: w.now(), Auto: w.auto, VisitMove: w.status.VisitMove}
}

// SetAuto switches vehicle-triggered visits on or off.
func (w *Watcher) SetAuto(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.auto, w.status.Auto = on, on
}

// Status returns the current camera status.
func (w *Watcher) Status() Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Camera returns the current camera, or nil.
func (w *Watcher) Camera() camera.Camera {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cam
}

// OnRead is the gate service's read hook. The gate's engine workers call it,
// so it only hands the result over to Run, and it never blocks: Run may itself
// be waiting for a worker to take a job.
func (w *Watcher) OnRead(moveID, photoID string, f recognition.Frame) {
	select {
	case <-w.done:
		return // Run has stopped: nobody will count the read
	default:
	}
	w.readMu.Lock()
	w.queued = append(w.queued, readResult{moveID: moveID, photoID: photoID, frame: f})
	w.readMu.Unlock()
	select {
	case w.notify <- struct{}{}:
	default: // Run has been told already
	}
}

// takeReads returns the reads OnRead queued since the last call.
func (w *Watcher) takeReads() []readResult {
	w.readMu.Lock()
	defer w.readMu.Unlock()
	q := w.queued
	w.queued = nil
	return q
}

// Capture starts a visit by hand and returns its move ID.
func (w *Watcher) Capture(ctx context.Context) (string, error) {
	reply := make(chan captureReply, 1)
	select {
	case w.captures <- reply:
	case <-w.done:
		return "", ErrStopped
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case r := <-reply:
		return r.moveID, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Run polls the camera and runs visits until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	defer close(w.done)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.notify:
			for _, r := range w.takeReads() {
				w.handleRead(ctx, r)
			}
		case reply := <-w.captures:
			id, err := w.startManual(ctx, w.now())
			reply <- captureReply{moveID: id, err: err}
		case <-timer.C:
			timer.Reset(w.poll(ctx))
		}
	}
}

// poll asks the camera whether a vehicle is there, moves the visit on, and
// returns how long to wait before asking again.
func (w *Watcher) poll(ctx context.Context) time.Duration {
	w.mu.Lock()
	cam, changed, stopped := w.cam, w.changed, w.status.State == StateAuth
	w.changed = false
	w.mu.Unlock()
	if changed {
		w.infoOK = false
	}
	if cam == nil || stopped {
		w.step(ctx, w.now(), false, cam)
		return w.cfg.Poll // no still can be taken, so there is no reason to wake sooner
	}
	cctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	det, err := cam.Detect(cctx)
	cancel()
	if w.Camera() != cam {
		return w.cfg.Poll // a late answer from a camera replaced meanwhile
	}
	now := w.now()
	if err != nil {
		auth := errors.Is(err, camera.ErrAuth)
		state := StateOffline
		if auth {
			state = StateAuth
		}
		w.update(cam, func(s *Status) {
			if s.State != state {
				s.Since = now
			}
			s.State, s.Error, s.Vehicle = state, err.Error(), false
		})
		if w.visit != nil {
			// Unknown is not "gone": a camera that stops answering mid-visit
			// must not split one truck into two moves, so the visit carries
			// on, still capped at MaxVisit. A wrong password ends it as usual.
			w.step(ctx, now, !auth, cam)
			if w.visit != nil {
				return w.wait()
			}
		}
		// Without a visit an outage says nothing about the lane: it neither
		// starts a visit nor counts towards re-arming.
		if auth {
			return w.cfg.Poll
		}
		return w.cfg.Retry
	}
	w.update(cam, func(s *Status) {
		if s.State != StateOnline {
			s.Since = now
		}
		s.State, s.Error, s.Vehicle, s.Detection = StateOnline, "", det.Present, det.Kind
	})
	w.step(ctx, now, det.Present, cam)
	if !w.infoOK {
		// Asked only once the camera answers, so an offline camera is retried
		// every Retry rather than every Retry plus two timeouts.
		cctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
		info, err := cam.Info(cctx)
		cancel()
		if err == nil {
			w.infoOK = true
			w.update(cam, func(s *Status) {
				s.Info = info
				if info.Detection != "" {
					s.Detection = info.Detection
				}
			})
		}
	}
	return w.wait()
}

// wait is how long to wait before the next poll: Poll, or less while a visit
// runs, so that its next still (stills are only taken by poll) is on time.
func (w *Watcher) wait() time.Duration {
	v := w.visit
	if v == nil {
		return w.cfg.Poll
	}
	return min(w.cfg.Poll, max(0, v.nextShot.Sub(w.now())))
}

// update changes the status, unless cam has been replaced since the poll
// began (a late answer from the old camera).
func (w *Watcher) update(cam camera.Camera, fn func(*Status)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cam == cam {
		fn(&w.status)
	}
}

func (w *Watcher) setVisitMove(id string) {
	w.mu.Lock()
	w.status.VisitMove = id
	w.mu.Unlock()
}

// step moves the watcher on to now, given whether a vehicle is present.
func (w *Watcher) step(ctx context.Context, now time.Time, vehicle bool, cam camera.Camera) {
	v := w.visit
	if v == nil {
		w.track(now, vehicle)
		w.mu.Lock()
		auto := w.auto
		w.mu.Unlock()
		if vehicle && auto && w.armed && cam != nil {
			_, _ = w.startVisit(ctx, now, false, cam)
		}
		return
	}
	if vehicle {
		v.lastSeen = now
	}
	if now.Sub(v.start) >= w.cfg.MaxVisit || (!v.manual && now.Sub(v.lastSeen) >= w.cfg.Quiet) {
		w.endVisit(ctx, now, vehicle)
		return
	}
	limit := w.cfg.MaxPhotos
	if v.manual {
		limit = w.cfg.ManualPhotos
	}
	if cam != nil && !now.Before(v.nextShot) && len(v.photos) < limit {
		w.shoot(ctx, now, cam)
		if w.visit != v {
			return // the move was closed while shooting
		}
	}
	if len(v.photos) >= limit {
		w.endVisit(ctx, now, vehicle)
		return
	}
	w.pump(ctx, v)
}

// track re-arms the watcher once the vehicle has been gone for Quiet, so the
// same parked truck never starts a second visit.
func (w *Watcher) track(now time.Time, vehicle bool) {
	if vehicle {
		w.offSince = time.Time{}
		return
	}
	if w.offSince.IsZero() {
		w.offSince = now
	}
	if now.Sub(w.offSince) >= w.cfg.Quiet {
		w.armed = true
	}
}

// startManual starts a "take photos now" visit. The camera must be online: an
// offline camera would leave an empty move, and one rejecting the password
// must not be asked again.
func (w *Watcher) startManual(ctx context.Context, now time.Time) (string, error) {
	w.mu.Lock()
	cam, state := w.cam, w.status.State
	w.mu.Unlock()
	if cam == nil {
		return "", ErrNoCamera
	}
	if state != StateOnline {
		return "", ErrNotConnected
	}
	if w.visit != nil {
		return "", ErrBusy
	}
	return w.startVisit(ctx, now, true, cam)
}

func (w *Watcher) startVisit(ctx context.Context, now time.Time, manual bool, cam camera.Camera) (string, error) {
	m, err := w.gate.NewCameraMove(ctx, "camera")
	if err != nil {
		log.Printf("gatecam: start visit: %v", err)
		return "", err
	}
	v := &visit{moveID: m.ID, manual: manual, start: now, lastSeen: now, nextShot: now,
		chosen: map[int]bool{}, inFlight: map[string]bool{}, votes: map[string]int{}}
	w.visit, w.armed, w.offSince = v, false, time.Time{}
	w.setVisitMove(m.ID)
	w.shoot(ctx, now, cam)
	w.pump(ctx, v)
	return m.ID, nil
}

// shoot takes one still and stores it with the visit's move. A still that
// could not be taken or stored is skipped. Only a move that is closed (a
// person already resolved it) or gone ends the visit.
func (w *Watcher) shoot(ctx context.Context, now time.Time, cam camera.Camera) {
	v := w.visit
	v.nextShot = now.Add(w.cfg.Interval)
	cctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	data, err := cam.Snapshot(cctx)
	cancel()
	if err != nil {
		log.Printf("gatecam: still for move %s: %v", v.moveID, err)
		return
	}
	width, height := jpegSize(data)
	p, err := w.gate.AddFrame(ctx, v.moveID, data, width, height)
	if errors.Is(err, gate.ErrClosed) || errors.Is(err, gate.ErrNotFound) {
		log.Printf("gatecam: store still for move %s: %v; visit ended", v.moveID, err)
		w.endVisit(ctx, now, true)
		return
	}
	if err != nil {
		log.Printf("gatecam: store still for move %s: %v", v.moveID, err)
		return
	}
	v.photos = append(v.photos, p.ID)
}

// endVisit stops taking stills. Reads may continue, and the visit settles
// once they are done.
func (w *Watcher) endVisit(ctx context.Context, now time.Time, vehicle bool) {
	v := w.visit
	w.visit = nil
	v.ended = true
	w.armed, w.offSince = false, time.Time{}
	if !vehicle {
		w.offSince = v.lastSeen
	}
	w.track(now, vehicle)
	w.setVisitMove("")
	w.settling[v.moveID] = v
	w.pump(ctx, v)
}

// handleRead counts a finished read for its visit, stops reading once two
// photos agree, and chooses the next photo. Only reads the visit asked for
// count.
func (w *Watcher) handleRead(ctx context.Context, r readResult) {
	v := w.visit
	if v == nil || v.moveID != r.moveID {
		v = w.settling[r.moveID]
	}
	if v == nil || !v.inFlight[r.photoID] {
		return // a phone photo, a person's re-read, or a read from before a restart
	}
	delete(v.inFlight, r.photoID)
	if n := leadNumber(r.frame); n != "" {
		v.votes[n]++
		if v.votes[n] >= 2 {
			v.stopped = true
		}
	}
	w.pump(ctx, v)
}

// pump sends photos to the engine until the limits are reached, and settles
// the visit when nothing more will be read.
func (w *Watcher) pump(ctx context.Context, v *visit) {
	for !v.stopped && len(v.inFlight) < w.cfg.Parallel && v.reads < w.cfg.MaxReads {
		i := v.pick()
		if i < 0 {
			break
		}
		v.chosen[i] = true
		if err := w.gate.ReadFrame(ctx, v.photos[i]); err != nil {
			log.Printf("gatecam: read photo %s: %v", v.photos[i], err)
			v.stopped = true // the move was closed, or the store failed
			break
		}
		v.inFlight[v.photos[i]] = true
		v.reads++
	}
	finished := v.stopped || v.reads >= w.cfg.MaxReads || (v.ended && v.pick() < 0)
	if finished && len(v.inFlight) == 0 && !v.settled {
		v.settled = true
		if err := w.gate.SettleCamera(ctx, v.moveID); err != nil {
			log.Printf("gatecam: settle move %s: %v", v.moveID, err)
		}
	}
	if v.settled && v.ended {
		delete(w.settling, v.moveID)
	}
}

// pick returns the index of the next photo to read, or -1. While the visit
// runs it is the newest unread photo, because the truck is nearest to where it
// stops. Afterwards it is the unread photo farthest from those already
// chosen, which spreads reads evenly over the visit.
func (v *visit) pick() int {
	if !v.ended {
		for i := len(v.photos) - 1; i >= 0; i-- {
			if !v.chosen[i] {
				return i
			}
		}
		return -1
	}
	best, bestDist := -1, -1
	for i := range v.photos {
		if v.chosen[i] {
			continue
		}
		d := min(i+1, len(v.photos)-i) // nothing chosen yet: prefer the middle
		if len(v.chosen) > 0 {
			d = len(v.photos)
			for c := range v.chosen {
				d = min(d, abs(i-c))
			}
		}
		if d > bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// leadNumber is a photo's first direct, check-digit-valid candidate (the same
// rule as Reconcile's lead read).
func leadNumber(f recognition.Frame) string {
	for _, c := range f.Candidates {
		if c.RepairedFrom == "" && c.CheckDigitValid {
			return c.Number
		}
	}
	return ""
}

func jpegSize(data []byte) (int, int) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

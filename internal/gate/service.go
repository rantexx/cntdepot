// Package gate runs gate-ins: photos come in, engines read them in the
// background, and a person confirms, corrects or refuses the result.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"

	"depot/internal/blob"
	"depot/internal/iso6346"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/tenancy"
)

var (
	ErrNotFound       = errors.New("move not found")
	ErrClosed         = errors.New("move already resolved")
	ErrBadAction      = errors.New("unknown action: use accept, correct or refuse")
	ErrNumberRequired = errors.New("a container number is required")
	ErrBadPhoto       = errors.New("photo missing or not a JPEG, PNG or WebP image")
)

// ModuleOCR is the company module that lets AI read gate photos.
const ModuleOCR = "ocr"

// Config tunes the service.
type Config struct {
	// AutoAcceptEnabled lets confident, check-digit-valid reads skip the
	// person. Off by default: at the owner's depot every read is confirmed.
	AutoAcceptEnabled   bool
	AutoAcceptThreshold float64 // default 0.85
	Workers             int     // parallel engine calls, default 2
}

// Service orchestrates gate-ins.
type Service struct {
	store  *store.Store
	blobs  *blob.Store
	rec    recognition.Recognizer
	cfg    Config
	notify func(companyID, moveID string)
	jobs   chan job
	ctx    context.Context
	mu     sync.Mutex // serializes recompute and resolve
	addMu  sync.Mutex // serializes storing photos, so the duplicate check is exact
	wg     sync.WaitGroup

	hook    ReadHook        // told about every finished read; may be nil
	visitMu sync.Mutex      // guards visits
	visits  map[string]bool // camera moves whose visit may still choose photos
}

type job struct {
	scope                                tenancy.Scope // the company and depot the job was created under
	moveID, photoID, role, aiSHA, aiType string
	detail                               string // recognition.Image.Detail: ultra-high for camera stills
}

// detailFor is the engine detail for photos of a move from source: camera
// stills are wide 4K pictures in which the marks are small.
func detailFor(source string) string {
	if source == store.SourceCamera {
		return recognition.DetailUltraHigh
	}
	return ""
}

// New builds the service. notify is called whenever a move changes (live
// screen updates), with the move's company; it may be nil.
func New(st *store.Store, bl *blob.Store, rec recognition.Recognizer, cfg Config, notify func(companyID, moveID string)) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.AutoAcceptThreshold <= 0 {
		cfg.AutoAcceptThreshold = 0.85
	}
	if notify == nil {
		notify = func(string, string) {}
	}
	return &Service{store: st, blobs: bl, rec: rec, cfg: cfg, notify: notify, jobs: make(chan job, 256), ctx: context.Background(), visits: map[string]bool{}}
}

// Engine names the engine and model in use.
func (s *Service) Engine() (name, model string) { return s.rec.Name(), s.rec.Model() }

// Start launches the workers and re-queues photos left unread by a previous
// run. Workers stop when ctx is cancelled; call Wait afterwards.
func (s *Service) Start(ctx context.Context) error {
	s.ctx = ctx
	for i := 0; i < s.cfg.Workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	companies, err := s.store.CompanyIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range companies {
		// Reads span the company's depots; each job keeps its photo's depot.
		// The gate never stops: one company's failure must not keep the others from recovering.
		if err := s.recoverCompany(tenancy.With(ctx, tenancy.Scope{CompanyID: id}), id); err != nil {
			log.Printf("gate: recover company %s: %v", id, err)
		}
	}
	return nil
}

// recoverCompany re-queues the company's unread photos and settles its
// moves left open by a previous run. ctx carries the company's scope.
func (s *Service) recoverCompany(ctx context.Context, companyID string) error {
	pending, err := s.store.PhotosWithoutAttempts(ctx)
	if err != nil {
		return err
	}
	unread := map[string]bool{}
	details := map[string]string{} // move ID -> engine detail, one lookup per move
	for _, p := range pending {
		detail, ok := details[p.MoveID]
		if !ok {
			m, err := s.store.GetMove(ctx, p.MoveID)
			if err != nil {
				return err
			}
			detail = detailFor(m.Source)
			details[p.MoveID] = detail
		}
		unread[p.MoveID] = true
		s.enqueue(job{scope: tenancy.Scope{CompanyID: companyID, DepotID: p.DepotID}, moveID: p.MoveID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType, detail: detail})
	}
	// A re-read cut short by a shutdown leaves its move saying "reading" with
	// every photo already read and no job left to finish it. Camera visits do
	// not survive a restart either. Moves with unread photos were just
	// re-queued above and settle as those jobs complete.
	if err := s.settleOpen(ctx, store.ListFilter{Status: store.StatusReading}, unread); err != nil {
		return err
	}
	return s.settleOpen(ctx, store.ListFilter{Status: store.StatusCapturing, Source: store.SourceCamera}, unread)
}

// settleOpen recomputes every open move matching f from its existing
// attempts, except those in skip.
func (s *Service) settleOpen(ctx context.Context, f store.ListFilter, skip map[string]bool) error {
	const page = 200
	f.Limit = page
	for {
		moves, err := s.store.ListMoves(ctx, f)
		if err != nil {
			return err
		}
		for _, m := range moves {
			if skip[m.ID] {
				continue
			}
			if err := s.recompute(ctx, m.ID); err != nil {
				log.Printf("gate: recompute move %s: %v", m.ID, err)
				continue
			}
			s.notifyMove(ctx, m.ID)
		}
		if len(moves) < page {
			return nil
		}
		f.Before = moves[len(moves)-1].ID
	}
}

// Wait blocks until the workers have stopped.
func (s *Service) Wait() { s.wg.Wait() }

// NewMove starts a gate-in.
func (s *Service) NewMove(ctx context.Context, lane string, isTest bool, actor string) (store.Move, error) {
	return s.store.CreateMove(ctx, lane, isTest, actor)
}

// PhotoInput is one uploaded photo: the original as captured, and a
// downscaled, correctly oriented copy for engines.
type PhotoInput struct {
	MoveID, Role, Actor string
	Original            []byte
	OriginalType        string
	AI                  []byte
	AIType              string
	Width, Height       int
}

// AddPhoto stores a photo and queues it for reading. Adding the same original
// again (a phone retrying an upload whose answer it never saw) returns the
// photo already stored and queues nothing.
func (s *Service) AddPhoto(ctx context.Context, in PhotoInput) (store.Photo, error) {
	if len(in.Original) == 0 || len(in.AI) == 0 || !engineReadable(in.AIType) {
		return store.Photo{}, ErrBadPhoto
	}
	// The input is good. A phone that disconnects now must not leave a stored
	// photo that was never queued, so the writes ignore the request's lifetime.
	ctx = context.WithoutCancel(ctx)
	p, added, err := s.storePhoto(ctx, in)
	if err != nil || !added {
		return p, err
	}
	s.notifyMove(ctx, p.MoveID)
	if p.ReadEnabled {
		s.enqueue(job{scope: scopeOf(ctx), moveID: p.MoveID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType}) // a phone photo: default detail
	}
	return p, nil
}

// storePhoto saves the photo and marks its move as reading. added is false
// when the move already holds this original.
func (s *Service) storePhoto(ctx context.Context, in PhotoInput) (p store.Photo, added bool, err error) {
	s.addMu.Lock() // so two concurrent uploads of the same photo cannot both insert it
	defer s.addMu.Unlock()
	m, err := s.openMove(ctx, in.MoveID)
	if err != nil {
		return store.Photo{}, false, err
	}
	origSHA, err := s.blobs.Put(in.Original)
	if err != nil {
		return store.Photo{}, false, err
	}
	for _, existing := range m.Photos {
		if existing.SHA256 == origSHA {
			return existing, false, nil
		}
	}
	aiSHA, err := s.blobs.Put(in.AI)
	if err != nil {
		return store.Photo{}, false, err
	}
	role := strings.TrimSpace(in.Role)
	if role == "" {
		role = "other"
	}
	readEnabled := s.ocrOn(ctx)
	p, err = s.store.AddPhoto(ctx, store.Photo{
		MoveID: m.ID, Role: role, SHA256: origSHA, AISHA256: aiSHA, MediaType: in.OriginalType,
		AIMediaType: in.AIType, Bytes: int64(len(in.Original)), Width: in.Width, Height: in.Height,
		ReadEnabled: readEnabled,
	}, in.Actor)
	if err != nil {
		return store.Photo{}, false, err
	}
	if !readEnabled {
		return p, true, nil // stored for the clerk; nothing will read it
	}
	if err := s.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		return store.Photo{}, false, err
	}
	return p, true, nil
}

// Reread queues every chosen photo of an open move for another engine run,
// e.g. after an engine outage. Deferred camera photos stay unread.
func (s *Service) Reread(ctx context.Context, moveID string) error {
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return err
	}
	var photos []store.Photo
	for _, p := range m.Photos {
		if !p.Deferred && p.ReadEnabled {
			photos = append(photos, p)
		}
	}
	if len(photos) == 0 {
		return nil
	}
	if err := s.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		return err
	}
	s.notifyMove(ctx, m.ID)
	detail := detailFor(m.Source)
	for _, p := range photos {
		s.enqueue(job{scope: scopeOf(ctx), moveID: m.ID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType, detail: detail})
	}
	return nil
}

// ocrOn reports whether ctx's company has the ocr module on. When that cannot
// be told, nothing is read: the clerk types the number.
func (s *Service) ocrOn(ctx context.Context) bool {
	mods, err := s.store.CompanyModules(ctx)
	if err != nil {
		log.Printf("gate: company modules: %v", err)
		return false
	}
	return slices.Contains(mods, ModuleOCR)
}

// ManualEntry records a container number typed by a person. A number that
// passes its check digit is accepted and verified; any other is stored and
// the move goes to review.
func (s *Service) ManualEntry(ctx context.Context, moveID, number, by string) (store.Move, error) {
	n := iso6346.Normalize(number)
	if n == "" {
		return store.Move{}, ErrNumberRequired
	}
	if iso6346.IsValid(n) {
		return s.Resolve(ctx, moveID, Resolution{Action: "correct", Number: n, By: by})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.openMove(ctx, moveID); err != nil {
		return store.Move{}, err
	}
	err := s.store.SetResult(ctx, moveID, store.ReadOutcome{
		Status: store.StatusNeedsReview, Number: n, Decision: string(recognition.DecisionReview),
		Reason: "typed number fails its check digit",
	})
	if err != nil {
		return store.Move{}, err
	}
	s.notifyMove(ctx, moveID)
	return s.store.GetMove(ctx, moveID)
}

// Resolution is a person's decision.
type Resolution struct {
	Action string `json:"action"` // accept | correct | refuse
	Number string `json:"number"` // for correct
	By     string `json:"by"`
	Note   string `json:"note"`
}

// Resolve records a person's decision about an open move.
func (s *Service) Resolve(ctx context.Context, moveID string, r Resolution) (store.Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return m, err
	}
	by := strings.TrimSpace(r.By)
	if by == "" {
		by = "operator"
	}
	u := store.ResolveUpdate{By: by, Note: strings.TrimSpace(r.Note)}
	switch r.Action {
	case "accept":
		if m.Number == "" {
			return m, ErrNumberRequired
		}
		u.Status, u.Number, u.Verified = store.StatusAccepted, m.Number, iso6346.IsValid(m.Number)
	case "correct":
		n := iso6346.Normalize(r.Number)
		if n == "" {
			return m, ErrNumberRequired
		}
		u.Status, u.Number, u.Verified = store.StatusAccepted, n, iso6346.IsValid(n)
		if m.Number != "" && m.Number != n {
			u.CorrectedFrom = m.Number
		}
	case "refuse":
		u.Status, u.Number = store.StatusRefused, m.Number
	default:
		return m, ErrBadAction
	}
	out, err := s.store.Resolve(ctx, moveID, u)
	if err == nil {
		s.notifyMove(ctx, moveID)
	}
	return out, err
}

// TestReadResult is the outcome of a one-off engine test.
type TestReadResult struct {
	Attempt recognition.Attempt `json:"attempt"`
	Frame   recognition.Frame   `json:"frame"`
	Error   string              `json:"error,omitempty"`
}

// TestRead runs the engine on one photo without storing anything. Engine
// failures are reported in the result, not as an error.
func (s *Service) TestRead(ctx context.Context, ai []byte, aiType string) (TestReadResult, error) {
	if len(ai) == 0 || !engineReadable(aiType) {
		return TestReadResult{}, ErrBadPhoto
	}
	return s.testRead(ctx, recognition.Image{Data: ai, MediaType: aiType})
}

func (s *Service) testRead(ctx context.Context, img recognition.Image) (TestReadResult, error) {
	att, err := s.rec.Read(ctx, img)
	res := TestReadResult{Attempt: att}
	if err != nil {
		res.Error = err.Error()
		res.Frame = recognition.ErrorFrame("test", err.Error())
		return res, nil
	}
	res.Frame = recognition.BuildFrame("test", att.Reading)
	return res, nil
}

func (s *Service) openMove(ctx context.Context, id string) (store.Move, error) {
	m, err := s.store.GetMove(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	if m.ResolvedBy != "" {
		return m, ErrClosed
	}
	return m, nil
}

// scopeOf is ctx's scope; the zero scope when there is none, so the job's
// store calls fail with tenancy.ErrNoScope instead of reading another company.
func scopeOf(ctx context.Context) tenancy.Scope {
	sc, _ := tenancy.From(ctx)
	return sc
}

// notifyMove tells the live screens that a move of ctx's company changed.
func (s *Service) notifyMove(ctx context.Context, moveID string) {
	s.notify(scopeOf(ctx).CompanyID, moveID)
}

func (s *Service) enqueue(j job) {
	select {
	case s.jobs <- j:
	case <-s.ctx.Done():
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.jobs:
			s.process(j)
		}
	}
}

func (s *Service) process(j job) {
	ctx := tenancy.With(s.ctx, j.scope)
	if !s.ocrOn(ctx) {
		// Switched off while queued: leave the move open for the clerk. The
		// hook still hears of it, so a camera visit does not wait for ever.
		if s.hook != nil && ctx.Err() == nil {
			s.hook(j.moveID, j.photoID, recognition.ErrorFrame(j.role, "ocr module is off"))
		}
		return
	}
	img := recognition.Image{MediaType: j.aiType, Detail: j.detail}
	called := false
	data, err := s.blobs.Get(j.aiSHA)
	att := recognition.Attempt{Engine: s.rec.Name(), Model: s.rec.Model(), PromptVersion: recognition.PromptVersion}
	if err == nil {
		img.Data = data
		att, err = s.rec.Read(ctx, img)
		called = true
	}
	if ctx.Err() != nil {
		return // shutting down: the photo stays unread and Start re-queues it
	}
	if called {
		if uerr := s.store.AddUsage(ctx, "ocr_read", 1); uerr != nil {
			log.Printf("gate: count read of photo %s: %v", j.photoID, uerr)
		}
	}
	frame := recognition.BuildFrame(j.role, att.Reading)
	reading, _ := json.Marshal(att.Reading)
	row := store.Attempt{
		MoveID: j.moveID, PhotoID: j.photoID, Engine: att.Engine, Model: att.Model, PromptVersion: att.PromptVersion,
		Reading: reading, Raw: att.Raw, InputTokens: att.Usage.InputTokens, OutputTokens: att.Usage.OutputTokens,
		LatencyMS: att.LatencyMS,
	}
	if err != nil {
		row.Error = err.Error()
		frame = recognition.ErrorFrame(j.role, err.Error())
	}
	// The hook hears about every read, stored or not, so a camera visit
	// never waits forever for an answer.
	defer func() {
		if s.hook != nil {
			s.hook(j.moveID, j.photoID, frame)
		}
	}()
	if _, err := s.store.AddAttempt(ctx, row); err != nil {
		log.Printf("gate: store attempt for photo %s: %v", j.photoID, err)
		return
	}
	if err := s.recompute(ctx, j.moveID); err != nil {
		log.Printf("gate: recompute move %s: %v", j.moveID, err)
	}
	s.notifyMove(ctx, j.moveID)
}

// recompute rebuilds a move's result from the latest attempt of each photo.
func (s *Service) recompute(ctx context.Context, moveID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.store.GetMove(ctx, moveID)
	if err != nil {
		return err
	}
	if m.ResolvedBy != "" {
		return nil
	}
	attempts, err := s.store.ListAttempts(ctx, moveID)
	if err != nil {
		return err
	}
	latest := map[string]store.Attempt{}
	for _, a := range attempts { // oldest first, so later attempts win
		latest[a.PhotoID] = a
	}
	camera := m.Source == store.SourceCamera
	var frames []recognition.Frame
	pending := 0
	for _, p := range m.Photos {
		if p.Deferred {
			continue // stored for training, never chosen for reading
		}
		// Every camera still is its own view, so stills that agree count as
		// agreement. Camera moves are never auto-accepted anyway.
		role := p.Role
		if camera {
			role = p.ID
		}
		a, ok := latest[p.ID]
		switch {
		case !ok:
			pending++
		case a.Error != "":
			frames = append(frames, recognition.ErrorFrame(role, a.Error))
		default:
			var r recognition.Reading
			if err := json.Unmarshal(a.Reading, &r); err != nil {
				frames = append(frames, recognition.ErrorFrame(role, err.Error()))
				continue
			}
			frames = append(frames, recognition.BuildFrame(role, r))
		}
	}
	res := recognition.Reconcile(frames, s.cfg.AutoAcceptThreshold)
	if camera {
		// A person confirms every camera read in this kit (camera spec §2.2),
		// so neither the stored decision nor the move.read event may say
		// auto_accept, even while the visit is still open.
		if res.Decision == recognition.DecisionAutoAccept {
			res.Decision, res.Reason = recognition.DecisionReview, "camera reads are confirmed by a person"
		}
		capCameraConfidence(&res, frames)
	}
	status := store.StatusNeedsReview
	switch {
	case pending > 0:
		status = store.StatusReading
	case camera && s.visitOpen(m.ID):
		status = store.StatusCapturing // the visit may still choose photos to read
	case camera && noContainer(frames):
		status, res.Reason = store.StatusNoContainer, "no container number in any photo"
	case camera && len(frames) == 0:
		res.Reason = "no photo was read" // e.g. every still failed, or a restart came before the first read
	case res.Decision == recognition.DecisionAutoAccept && s.cfg.AutoAcceptEnabled:
		status = store.StatusAccepted
	}
	resultJSON, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return s.store.SetResult(ctx, moveID, store.ReadOutcome{
		Status: status, Number: res.Number, TypeCode: res.TypeCode, Color: res.Color, Confidence: res.Confidence,
		CheckDigitValid: res.CheckDigitValid, Decision: string(res.Decision), Reason: res.Reason, ResultJSON: resultJSON,
	})
}

func engineReadable(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}

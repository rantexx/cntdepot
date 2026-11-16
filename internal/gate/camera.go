package gate

import (
	"context"
	"log"
	"net/http"

	"depot/internal/recognition"
	"depot/internal/store"
)

// ReadHook is told about every finished engine read. The camera watcher uses
// it to stop reading a visit's photos once two of them agree.
type ReadHook func(moveID, photoID string, f recognition.Frame)

// SetReadHook installs h. Call it before Start.
func (s *Service) SetReadHook(h ReadHook) { s.hook = h }

// NewCameraMove starts a gate-in whose photos come from a fixed camera. The
// move stays open for reads until SettleCamera.
func (s *Service) NewCameraMove(ctx context.Context, actor string) (store.Move, error) {
	m, err := s.store.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, actor)
	if err != nil {
		return m, err
	}
	s.visitMu.Lock()
	s.visits[m.ID] = true
	s.visitMu.Unlock()
	s.notifyMove(ctx, m.ID)
	return m, nil
}

// AddFrame stores one camera still as a deferred photo. The still is both
// the original and the engine copy, because shrinking it would make distant
// container marks unreadable.
func (s *Service) AddFrame(ctx context.Context, moveID string, jpeg []byte, width, height int) (store.Photo, error) {
	if len(jpeg) == 0 || http.DetectContentType(jpeg) != "image/jpeg" {
		return store.Photo{}, ErrBadPhoto
	}
	ctx = context.WithoutCancel(ctx)
	s.addMu.Lock()
	defer s.addMu.Unlock()
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return store.Photo{}, err
	}
	sha, err := s.blobs.Put(jpeg)
	if err != nil {
		return store.Photo{}, err
	}
	p, err := s.store.AddPhoto(ctx, store.Photo{
		MoveID: m.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg",
		Bytes: int64(len(jpeg)), Width: width, Height: height, Deferred: true, ReadEnabled: true,
	}, "camera")
	if err != nil {
		return store.Photo{}, err
	}
	s.notifyMove(ctx, m.ID)
	return p, nil
}

// ReadFrame chooses a deferred camera photo for reading and queues it.
func (s *Service) ReadFrame(ctx context.Context, photoID string) error {
	ctx = context.WithoutCancel(ctx)
	p, err := s.store.GetPhoto(ctx, photoID)
	if err != nil {
		return err
	}
	if _, err := s.openMove(ctx, p.MoveID); err != nil {
		return err
	}
	if err := s.store.MarkForReading(ctx, p.ID, "camera"); err != nil {
		return err
	}
	// The photo is chosen now, so it must be read whatever happens next: a
	// chosen photo without a job would hold the move at "reading" until a
	// restart. The status is only what the screens show meanwhile.
	if err := s.store.SetStatus(ctx, p.MoveID, store.StatusReading); err != nil {
		log.Printf("gate: mark move %s reading: %v", p.MoveID, err)
	}
	s.notifyMove(ctx, p.MoveID)
	s.enqueue(job{scope: scopeOf(ctx), moveID: p.MoveID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType,
		detail: recognition.DetailUltraHigh})
	return nil
}

// SettleCamera says no more reads will be requested for a camera move, so
// its result can settle: needs review, or no container when no read found a
// number. Photos may still be added afterwards, and they stay deferred.
func (s *Service) SettleCamera(ctx context.Context, moveID string) error {
	s.visitMu.Lock()
	delete(s.visits, moveID)
	s.visitMu.Unlock()
	if err := s.recompute(context.WithoutCancel(ctx), moveID); err != nil {
		return err
	}
	s.notifyMove(ctx, moveID)
	return nil
}

// ReadStill reads one camera still without storing anything (the Camera
// page's "read this frame").
func (s *Service) ReadStill(ctx context.Context, jpeg []byte) (TestReadResult, error) {
	if len(jpeg) == 0 || http.DetectContentType(jpeg) != "image/jpeg" {
		return TestReadResult{}, ErrBadPhoto
	}
	return s.testRead(ctx, recognition.Image{Data: jpeg, MediaType: "image/jpeg", Detail: recognition.DetailUltraHigh})
}

// visitOpen reports whether a camera visit may still choose photos of the
// move for reading. Visits do not survive a restart.
func (s *Service) visitOpen(moveID string) bool {
	s.visitMu.Lock()
	defer s.visitMu.Unlock()
	return s.visits[moveID]
}

// noContainer reports whether a camera move's reads prove an empty lane: at
// least one photo was read, every read succeeded, and none saw a container.
// No read at all proves nothing, a failed read is not proof either, and a
// container whose number is illegible (seen, or its type code read) goes to a
// person.
func noContainer(frames []recognition.Frame) bool {
	if len(frames) == 0 {
		return false
	}
	for _, f := range frames {
		if f.Error != "" || len(f.Candidates) > 0 || f.Visible != "none" || f.TypeCode != "" {
			return false
		}
	}
	return true
}

// capCameraConfidence keeps a camera move's confidence at what one still
// actually read. Every still counts as its own view, so stills of the same
// side would otherwise add agreement bonuses (0.95 becoming 1.00).
func capCameraConfidence(res *recognition.Result, frames []recognition.Frame) {
	if res.Number == "" {
		return
	}
	direct, repaired, found := 0.0, 0.0, false
	for _, f := range frames {
		for _, c := range f.Candidates {
			if c.Number != res.Number {
				continue
			}
			if c.RepairedFrom == "" {
				direct, found = max(direct, c.Confidence), true
			} else {
				repaired = max(repaired, c.Confidence)
			}
		}
	}
	limit := direct
	if !found {
		limit = repaired // only ever guessed by repair: Reconcile adds no bonus to those
	}
	res.Confidence = min(res.Confidence, limit)
}

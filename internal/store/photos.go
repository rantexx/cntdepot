package store

import (
	"context"
	"time"

	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

// Photo is one picture of a move. SHA256 is the original as captured (kept
// for evidence and training); AISHA256 is the downscaled copy sent to engines.
type Photo struct {
	ID          string    `json:"id"`
	DepotID     string    `json:"depot_id"` // the move's depot, so a caller can build a per-photo scope
	MoveID      string    `json:"move_id"`
	Role        string    `json:"role"`
	SHA256      string    `json:"sha256"`
	AISHA256    string    `json:"ai_sha256"`
	MediaType   string    `json:"media_type"`
	AIMediaType string    `json:"ai_media_type"`
	Bytes       int64     `json:"bytes"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	CapturedAt  time.Time `json:"captured_at"`
	// Deferred photos are stored but not read until chosen: a camera visit
	// takes a still every second and reads only a few of them.
	Deferred bool `json:"deferred"`
	// ReadEnabled is set when the photo is stored: nothing is ever read by AI
	// unless it is true, so switching the ocr module on never reads older photos.
	ReadEnabled bool `json:"read_enabled"`
}

func photoFrom(r sqlcdb.Photo, depot string) Photo {
	return Photo{
		ID: r.ID, DepotID: depot, MoveID: r.MoveID, Role: r.Role, SHA256: r.Sha256, AISHA256: r.AiSha256, MediaType: r.MediaType,
		AIMediaType: r.AiMediaType, Bytes: r.Bytes, Width: int(r.Width), Height: int(r.Height),
		CapturedAt: r.CapturedAt, Deferred: r.Deferred, ReadEnabled: r.ReadEnabled,
	}
}

// AddPhoto records a photo of a move.
func (s *Store) AddPhoto(ctx context.Context, p Photo, actor string) (Photo, error) {
	p.ID = newID()
	if p.CapturedAt.IsZero() {
		p.CapturedAt = s.now()
	}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		// The move must be in the scope's depot; the composite foreign key covers the company only.
		m, err := exists(ctx, q, sc, p.MoveID)
		if err != nil {
			return err
		}
		p.DepotID = m.DepotID
		err = q.InsertPhoto(ctx, sqlcdb.InsertPhotoParams{
			CompanyID: sc.CompanyID, ID: p.ID, MoveID: p.MoveID, Role: p.Role, Sha256: p.SHA256, AiSha256: p.AISHA256,
			MediaType: p.MediaType, AiMediaType: p.AIMediaType, Bytes: p.Bytes, Width: int64(p.Width),
			Height: int64(p.Height), CapturedAt: p.CapturedAt, Deferred: p.Deferred, ReadEnabled: p.ReadEnabled,
		})
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, q, sc, p.MoveID, "photo.added", actor,
			map[string]any{"photo_id": p.ID, "role": p.Role, "sha256": p.SHA256})
	})
	return p, err
}

// ListPhotos returns a move's photos in capture order.
func (s *Store) ListPhotos(ctx context.Context, moveID string) ([]Photo, error) {
	photos := []Photo{}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.ListPhotos(ctx, sqlcdb.ListPhotosParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, MoveID: moveID})
		for _, r := range rows {
			photos = append(photos, photoFrom(r.Photo, r.DepotID))
		}
		return err
	})
	return photos, err
}

// GetPhoto returns one photo.
func (s *Store) GetPhoto(ctx context.Context, id string) (Photo, error) {
	var p Photo
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		r, err := q.GetPhoto(ctx, sqlcdb.GetPhotoParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		p = photoFrom(r.Photo, r.DepotID)
		return notFound(err)
	})
	return p, err
}

// PhotoByDigest finds a photo of the scope's company (and depot, when the scope
// has one) by the SHA-256 of its original or of its AI copy. ErrNotFound when none.
func (s *Store) PhotoByDigest(ctx context.Context, digest string) (Photo, error) {
	var p Photo
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		r, err := q.PhotoByDigest(ctx, sqlcdb.PhotoByDigestParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, Digest: digest})
		p = photoFrom(r.Photo, r.DepotID)
		return notFound(err)
	})
	return p, err
}

// MarkForReading chooses a deferred photo for reading. A photo that is already
// chosen, or was never deferred, is left alone and writes no second event; an
// unknown id is ErrNotFound.
func (s *Store) MarkForReading(ctx context.Context, id, actor string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		p, err := q.GetPhoto(ctx, sqlcdb.GetPhotoParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID, ID: id})
		if err != nil {
			return notFound(err)
		}
		n, err := q.ChoosePhoto(ctx, sqlcdb.ChoosePhotoParams{CompanyID: sc.CompanyID, ID: id})
		if err != nil || n == 0 {
			return err // n == 0: the photo exists and is already chosen
		}
		return s.appendEvent(ctx, q, sc, p.Photo.MoveID, "photo.chosen", actor, map[string]any{"photo_id": id})
	})
}

// DisableRead marks a photo as never to be read (its read was skipped while
// the ocr module was off).
func (s *Store) DisableRead(ctx context.Context, id string) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		return q.DisableRead(ctx, sqlcdb.DisableReadParams{CompanyID: sc.CompanyID, ID: id})
	})
}

// PhotosWithoutAttempts returns read-enabled photos of open moves that have
// not been read yet, e.g. because the depot service stopped mid-read. Start
// re-queues them. Deferred photos are left alone: nobody chose them for
// reading, and photos stored with read_enabled false are never read. With an
// empty scope depot it covers every depot of the company; Photo.DepotID says which.
func (s *Store) PhotosWithoutAttempts(ctx context.Context) ([]Photo, error) {
	photos := []Photo{}
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		rows, err := q.PhotosWithoutAttempts(ctx, sqlcdb.PhotosWithoutAttemptsParams{CompanyID: sc.CompanyID, DepotID: sc.DepotID})
		for _, r := range rows {
			photos = append(photos, photoFrom(r.Photo, r.DepotID))
		}
		return err
	})
	return photos, err
}

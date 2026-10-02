package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Photo is one picture of a move. SHA256 is the original as captured (kept
// for evidence and training); AISHA256 is the downscaled copy sent to engines.
type Photo struct {
	ID          string    `json:"id"`
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
}

const photoColumns = `id, move_id, role, sha256, ai_sha256, media_type, ai_media_type, bytes, width, height, captured_at, deferred`

func scanPhoto(sc scanner) (Photo, error) {
	var p Photo
	var captured string
	var deferred int
	err := sc.Scan(&p.ID, &p.MoveID, &p.Role, &p.SHA256, &p.AISHA256, &p.MediaType, &p.AIMediaType,
		&p.Bytes, &p.Width, &p.Height, &captured, &deferred)
	p.CapturedAt, p.Deferred = parseTS(captured), deferred == 1
	return p, err
}

// AddPhoto records a photo of a move.
func (s *Store) AddPhoto(ctx context.Context, p Photo, actor string) (Photo, error) {
	p.ID = newID()
	if p.CapturedAt.IsZero() {
		p.CapturedAt = s.now()
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO photos (id, tenant_id, move_id, role, sha256, ai_sha256, media_type, ai_media_type,
			 bytes, width, height, captured_at, deferred) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, s.tenant, p.MoveID, p.Role, p.SHA256, p.AISHA256, p.MediaType, p.AIMediaType,
			p.Bytes, p.Width, p.Height, ts(p.CapturedAt), boolInt(p.Deferred))
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, p.MoveID, "photo.added", actor,
			map[string]any{"photo_id": p.ID, "role": p.Role, "sha256": p.SHA256})
	})
	return p, err
}

// ListPhotos returns a move's photos in capture order.
func (s *Store) ListPhotos(ctx context.Context, moveID string) ([]Photo, error) {
	return s.queryPhotos(ctx, `SELECT `+photoColumns+` FROM photos WHERE move_id = ? ORDER BY id`, moveID)
}

// GetPhoto returns one photo.
func (s *Store) GetPhoto(ctx context.Context, id string) (Photo, error) {
	p, err := scanPhoto(s.db.QueryRowContext(ctx, `SELECT `+photoColumns+` FROM photos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	return p, err
}

// MarkForReading chooses a deferred photo for reading. A photo that is already
// chosen, or was never deferred, is left alone and writes no second event; an
// unknown id is ErrNotFound.
func (s *Store) MarkForReading(ctx context.Context, id, actor string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var moveID string
		err := tx.QueryRowContext(ctx, `SELECT move_id FROM photos WHERE id = ?`, id).Scan(&moveID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE photos SET deferred = 0 WHERE id = ? AND deferred = 1`, id)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return nil // the photo exists and is already chosen
		}
		return s.appendEvent(ctx, tx, moveID, "photo.chosen", actor, map[string]any{"photo_id": id})
	})
}

// PhotosWithoutAttempts returns photos of open moves that should be read but
// have not been yet, e.g. because the hub stopped mid-read. Start re-queues
// them. Deferred photos are left alone: nobody chose them for reading.
func (s *Store) PhotosWithoutAttempts(ctx context.Context) ([]Photo, error) {
	return s.queryPhotos(ctx,
		`SELECT `+photoColumns+` FROM photos p
		 WHERE p.deferred = 0
		   AND NOT EXISTS (SELECT 1 FROM recognition_attempts a WHERE a.photo_id = p.id)
		   AND p.move_id IN (SELECT id FROM gate_moves WHERE resolved_by = '')
		 ORDER BY id`)
}

func (s *Store) queryPhotos(ctx context.Context, query string, args ...any) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	photos := []Photo{}
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

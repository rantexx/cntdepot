package store

import (
	"errors"
	"strings"
	"testing"

	"depot/internal/db"

	"github.com/jackc/pgx/v5"
)

func TestMovesRecordTheirSource(t *testing.T) {
	s, _ := openTest(t)
	phone, err := s.CreateMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	cam, err := s.CreateMoveWithSource(ctx, "camera", SourceCamera, false, "camera")
	if err != nil {
		t.Fatal(err)
	}
	if phone.Source != SourcePhone || cam.Source != SourceCamera {
		t.Fatalf("sources = %q, %q", phone.Source, cam.Source)
	}
	got, err := s.GetMove(ctx, cam.ID)
	if err != nil || got.Source != SourceCamera {
		t.Fatalf("GetMove = %+v, %v", got, err)
	}
	list, err := s.ListMoves(ctx, ListFilter{Source: SourceCamera})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != cam.ID {
		t.Fatalf("camera moves = %+v", list)
	}
}

func TestDeferredPhotosWaitUntilChosen(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMoveWithSource(ctx, "camera", SourceCamera, false, "camera")
	p, err := s.AddPhoto(ctx, Photo{MoveID: m.ID, Role: "rear", SHA256: "a", AISHA256: "a", MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true, ReadEnabled: true}, "camera")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PhotosWithoutAttempts(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v; want none while deferred", pending, err)
	}
	if err := s.MarkForReading(ctx, p.ID, "camera"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPhoto(ctx, p.ID)
	if err != nil || got.Deferred {
		t.Fatalf("GetPhoto = %+v, %v; want deferred false", got, err)
	}
	pending, _ = s.PhotosWithoutAttempts(ctx)
	if len(pending) != 1 || pending[0].ID != p.ID {
		t.Fatalf("pending = %+v; want the chosen photo", pending)
	}
	if err := s.MarkForReading(ctx, "nope", "camera"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkForReading(unknown) = %v; want ErrNotFound", err)
	}
	if _, err := s.GetPhoto(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPhoto(unknown) = %v; want ErrNotFound", err)
	}
}

// countEvents returns how many events of a kind the store has written.
func countEvents(t *testing.T, s *Store, kind string) int {
	t.Helper()
	var n int
	err := db.InCompany(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COUNT(*) FROM events WHERE kind = $1`, kind).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChoosingAPhotoTwiceWritesOneEvent(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMoveWithSource(ctx, "camera", SourceCamera, false, "camera")
	p, err := s.AddPhoto(ctx, Photo{MoveID: m.ID, Role: "rear", SHA256: "a", AISHA256: "a", MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true, ReadEnabled: true}, "camera")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.MarkForReading(ctx, p.ID, "camera"); err != nil {
			t.Fatalf("MarkForReading #%d = %v; want nil", i+1, err)
		}
	}
	if n := countEvents(t, s, "photo.chosen"); n != 1 {
		t.Fatalf("photo.chosen events = %d; want 1 for two calls", n)
	}
	got, err := s.GetPhoto(ctx, p.ID)
	if err != nil || got.Deferred {
		t.Fatalf("GetPhoto = %+v, %v; want deferred false", got, err)
	}

	// A phone photo was never deferred: nothing to choose, so no event.
	phone, _ := s.CreateMove(ctx, "1", false, "clerk")
	if err := s.MarkForReading(ctx, addPhoto(t, s, phone.ID).ID, "clerk"); err != nil {
		t.Fatalf("MarkForReading(phone photo) = %v; want nil", err)
	}
	if n := countEvents(t, s, "photo.chosen"); n != 1 {
		t.Fatalf("photo.chosen events = %d after a phone photo; want still 1", n)
	}
}

func TestSavingNoSettingsWritesNothing(t *testing.T) {
	s, _ := openTest(t)
	for _, empty := range []map[string]string{nil, {}} {
		if err := s.SetSettings(ctx, empty, "admin"); err != nil {
			t.Fatalf("SetSettings(%v) = %v; want nil", empty, err)
		}
	}
	if n := countEvents(t, s, "settings.changed"); n != 0 {
		t.Fatalf("settings.changed events = %d; want none for empty saves", n)
	}
	if got, err := s.Settings(ctx, ""); err != nil || len(got) != 0 {
		t.Fatalf("settings = %v, %v; want none", got, err)
	}
	if err := s.SetSettings(ctx, map[string]string{"camera.host": "h"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, s, "settings.changed"); n != 1 {
		t.Fatalf("settings.changed events = %d; want 1 for a real save", n)
	}
}

func TestSettingsRoundTripWithoutLeakingValues(t *testing.T) {
	s, _ := openTest(t)
	if err := s.SetSettings(ctx, map[string]string{"camera.host": "192.168.1.50", "camera.password": "pw-secret"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{"camera.host": "192.168.1.51", "other.key": "x"}, "admin"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx, "camera.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["camera.host"] != "192.168.1.51" || got["camera.password"] != "pw-secret" {
		t.Fatalf("settings = %v", got)
	}
	var payloads []string
	err = db.InCompany(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT payload::text FROM events WHERE kind = 'settings.changed'`)
		if err != nil {
			return err
		}
		payloads, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 {
		t.Fatalf("settings events = %d; want 2", len(payloads))
	}
	for _, p := range payloads {
		if strings.Contains(p, "pw-secret") || strings.Contains(p, "192.168") {
			t.Fatalf("event payload carries a value: %s", p)
		}
	}
}

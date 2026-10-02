package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"depot/internal/testdir"
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
	p, err := s.AddPhoto(ctx, Photo{MoveID: m.ID, Role: "rear", SHA256: "a", AISHA256: "a", MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, Deferred: true}, "camera")
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
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM events WHERE kind = 'settings.changed'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		payloads = append(payloads, p)
	}
	rows.Close()
	if len(payloads) != 2 {
		t.Fatalf("settings events = %d; want 2", len(payloads))
	}
	for _, p := range payloads {
		if strings.Contains(p, "pw-secret") || strings.Contains(p, "192.168") {
			t.Fatalf("event payload carries a value: %s", p)
		}
	}
}

func TestMigratesAVersion1Database(t *testing.T) {
	path := filepath.Join(testdir.New(t), "depot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(schemaV1, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`)
	mustExec(`INSERT INTO schema_version (version) VALUES (1)`)
	mustExec(`INSERT INTO gate_moves (id, tenant_id, depot_id, status, created_at, updated_at) VALUES ('m1', 'local', 'own-depot', 'needs_review', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`)
	mustExec(`INSERT INTO photos (id, tenant_id, move_id, role, sha256, ai_sha256, media_type, ai_media_type, bytes, captured_at) VALUES ('p1', 'local', 'm1', 'rear', 'a', 'a', 'image/jpeg', 'image/jpeg', 1, '2026-10-01T00:00:00Z')`)
	db.Close()

	s, err := Open(path, "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.GetMove(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != SourcePhone || len(m.Photos) != 1 || m.Photos[0].Deferred {
		t.Fatalf("migrated move = %+v", m)
	}
}

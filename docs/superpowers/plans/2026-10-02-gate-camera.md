# Gate Camera Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect the owner's Reolink RLC-810WA to the local gate hub. The camera gets live video in the web app, its own vehicle detection triggers 4K photo bursts that are read automatically, and a Camera page helps set up and aim it.

**Architecture:**
- A `camera` package talks to the Reolink HTTP API. Behind a brand-neutral interface it provides stills, vehicle detection and the RTSP address.
- A `gatecam` watcher polls detection and runs **visits** through new gate-service methods. A visit is a photo every second and up to 8 engine reads, stopping early when two reads agree.
- A `live` supervisor runs go2rtc on 127.0.0.1 with a per-start password. The hub proxies one fixed WebSocket path to it, and the browser plays the video with a small MSE player.
- Hub-side previews (`?w=`) keep 4K stills off phones.

**Tech Stack:** Go 1.27 standard library (net/http, httputil.ReverseProxy, image/jpeg, os/exec), existing `modernc.org/sqlite` store, Gemini REST (per-part `mediaResolution`), go2rtc v1.9.14 (MIT, external binary), React 19 + TypeScript + Vite.

**Spec:** `docs/superpowers/specs/2026-10-02-gate-camera-design.md`
**Branch:** `feat/gate-camera` (already created from `feat/local-gate-test-kit`).

---

## Ground rules for every task

- **Location and tools:** the repo is `C:\Users\OZGUN\depot-platform` (Windows 11). Use Git Bash or PowerShell, Go 1.27 and Node 24.
- **Hands off the running hub:** the owner is using it. Never touch `bin\depotd.exe`, the `depot-data` folder or port 8080.
- **Builds:** build check binaries to `%TEMP%`, for example `go build -o $env:TEMP\depotd-check.exe ./cmd/depotd`.
- **Processes:** never kill processes you did not start yourself, and never kill by image name (no `taskkill /IM`, no `Stop-Process -Name`).
- **Temporary folders in Go tests:** use `depot/internal/testdir` (`testdir.New(t)`), not `t.TempDir()`. Windows keeps SQLite files locked for a moment after closing.
- **After each task:** run `gofmt -l .`, which must print nothing, then `go vet ./...` and `go test ./...`.
- **Web tasks:** run `npm --prefix web run typecheck` and `npm --prefix web run build`.
- **Commits:** commit at the end of each task with the message given, ending with a blank line and `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Comments and code style:** match the existing comment density and naming. Comments explain *why*, and package docs say what the package is for.

## File map

```
internal/store/schema_v2.sql            NEW  migration v2: moves.source, photos.deferred, settings table
internal/store/store.go                 MOD  embed v2, StatusNoContainer, SourcePhone/SourceCamera
internal/store/moves.go                 MOD  Move.Source, CreateMoveWithSource, ListFilter.Source
internal/store/photos.go                MOD  Photo.Deferred, GetPhoto, MarkForReading, pending excludes deferred
internal/store/settings.go              NEW  Settings / SetSettings (key-value, events never carry values)
internal/thumb/thumb.go                 NEW  JPEG previews (box downscale), allowed widths
internal/recognition/recognizer.go      MOD  Image.Detail, DetailUltraHigh
internal/recognition/gemini.go          MOD  per-part mediaResolution
internal/gate/camera.go                 NEW  NewCameraMove, AddFrame, ReadFrame, SettleCamera, ReadStill, read hook
internal/gate/service.go                MOD  deferred rules, camera settling, hook call, ultra-high detail for camera moves
internal/camera/camera.go               NEW  Camera interface, Settings, Info, Detection, Report, errors
internal/camera/reolink.go              NEW  Reolink HTTP API client
internal/camera/fake.go                 NEW  scripted camera + generated demo still
internal/gatecam/watcher.go             NEW  polling, visit state machine, manual capture, status
internal/gatecam/letters.go             NEW  letter height in pixels + verdict
internal/gatecam/manager.go             NEW  settings ↔ store, applies camera to watcher and live video
internal/live/live.go                   NEW  go2rtc supervisor (config, process, backoff)
internal/live/proxy.go                  NEW  fixed-path WebSocket reverse proxy
internal/httpapi/camera.go              NEW  /api/camera*, /api/live/ws
internal/httpapi/handlers.go            MOD  ?w= previews on blobs, ?source= on moves
internal/httpapi/server.go              MOD  Options.Camera/Live, routes
cmd/depotd/main.go                      MOD  wiring, -fake-camera, -go2rtc
web/src/api.ts                          MOD  camera types and calls, source/deferred/no_container, preview URLs
web/src/i18n.ts                         MOD  camera strings (tr + en)
web/src/camera/mse.ts                   NEW  go2rtc MSE-over-WebSocket player
web/src/camera/LiveVideo.tsx            NEW  live video with still-refresh fallback
web/src/pages/Camera.tsx                NEW  setup, test, live, read this frame, capture, recent visits
web/src/App.tsx, pages/Home.tsx         MOD  Camera route, nav and tile
web/src/pages/Queue.tsx                 MOD  Camera badge, preview thumbnails
web/src/pages/MoveDetail.tsx            MOD  Live button, read vs other photos, previews
web/src/styles.css                      MOD  live video, status light, test steps
docs/runbooks/local-test-kit.md         MOD  camera chapter, go2rtc install, troubleshooting
```

---

### Task 1: Store migration v2 (camera moves, deferred photos, settings)

**Files:**
- Create: `internal/store/schema_v2.sql`
- Create: `internal/store/settings.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/moves.go`
- Modify: `internal/store/photos.go`
- Test: `internal/store/camera_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/camera_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/`
Expected: compile errors such as `undefined: SourceCamera`, `unknown field Deferred`, `s.CreateMoveWithSource undefined`.

- [ ] **Step 3: Add the migration**

Create `internal/store/schema_v2.sql` (the migration runner splits on `;`, so no semicolons inside statements):

```sql
-- Schema v2: fixed gate cameras (spec 2026-10-02-gate-camera-design.md)
ALTER TABLE gate_moves ADD COLUMN source TEXT NOT NULL DEFAULT 'phone';
CREATE INDEX gate_moves_source ON gate_moves (source, id);
ALTER TABLE photos ADD COLUMN deferred INTEGER NOT NULL DEFAULT 0;
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
```

In `internal/store/store.go`, replace the embed and migrations block and the status constants:

```go
//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2.sql
var schemaV2 string

// migrations run in order. Append new versions; never edit a shipped one.
var migrations = []string{schemaV1, schemaV2}

// Move statuses.
const (
	StatusCapturing   = "capturing"
	StatusReading     = "reading"
	StatusNeedsReview = "needs_review"
	StatusAccepted    = "accepted"
	StatusRefused     = "refused"
	// StatusNoContainer is a camera visit whose photos showed no container
	// number, such as a car or forklift passing. It stays out of the review queue.
	StatusNoContainer = "no_container"
)

// Where a move's photos come from.
const (
	SourcePhone  = "phone"
	SourceCamera = "camera"
)
```

- [ ] **Step 4: Moves carry their source**

In `internal/store/moves.go`:

1. Add `Source` to `Move`, right after `Direction`:

```go
	Direction       string          `json:"direction"`
	Source          string          `json:"source"` // phone | camera
```

2. Add `Source` to `ListFilter`:

```go
// ListFilter selects moves, newest first. Before is a move ID (keyset paging).
type ListFilter struct {
	Status string
	Source string
	Limit  int
	Before string
}
```

3. Put `source` after `direction` in `moveColumns`, and scan it in the same position:

```go
const moveColumns = `id, lane, direction, source, status, number, type_code, color, confidence, check_digit_valid,
	decision, reason, result_json, verified, corrected_from, resolved_by, note, is_test, created_at, updated_at`
```

```go
	err := sc.Scan(&m.ID, &m.Lane, &m.Direction, &m.Source, &m.Status, &m.Number, &m.TypeCode, &m.Color, &m.Confidence, &cdv,
		&m.Decision, &m.Reason, &result, &verified, &m.CorrectedFrom, &m.ResolvedBy, &m.Note, &isTest, &created, &updated)
```

4. Replace `CreateMove` with:

```go
// CreateMove starts a phone gate-in on a lane.
func (s *Store) CreateMove(ctx context.Context, lane string, isTest bool, actor string) (Move, error) {
	return s.CreateMoveWithSource(ctx, lane, SourcePhone, isTest, actor)
}

// CreateMoveWithSource starts a gate-in whose photos come from source.
func (s *Store) CreateMoveWithSource(ctx context.Context, lane, source string, isTest bool, actor string) (Move, error) {
	now := s.now()
	m := Move{
		ID: newID(), Lane: lane, Direction: "in", Source: source, Status: StatusCapturing, Result: json.RawMessage("{}"),
		IsTest: isTest, CreatedAt: now, UpdatedAt: now, Photos: []Photo{},
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO gate_moves (id, tenant_id, depot_id, lane, direction, source, status, is_test, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, s.tenant, s.depot, lane, m.Direction, source, m.Status, boolInt(isTest), ts(now), ts(now))
		if err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, m.ID, "move.created", actor, map[string]any{"lane": lane, "source": source, "is_test": isTest})
	})
	return m, err
}
```

5. Filter on source in `ListMoves`:

```go
	return s.queryMoves(ctx,
		`SELECT `+moveColumns+` FROM gate_moves
		 WHERE (? = '' OR status = ?) AND (? = '' OR source = ?) AND (? = '' OR id < ?)
		 ORDER BY id DESC LIMIT ?`,
		f.Status, f.Status, f.Source, f.Source, f.Before, f.Before, f.Limit)
```

- [ ] **Step 5: Photos can be deferred**

In `internal/store/photos.go`:

1. Add `"errors"` to the imports.
2. Add the field at the end of `Photo`:

```go
	CapturedAt  time.Time `json:"captured_at"`
	// Deferred photos are stored but not read until chosen: a camera visit
	// takes a still every second and reads only a few of them.
	Deferred bool `json:"deferred"`
```

3. Replace `photoColumns`, `scanPhoto` and the INSERT in `AddPhoto`:

```go
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
```

```go
		_, err := tx.ExecContext(ctx,
			`INSERT INTO photos (id, tenant_id, move_id, role, sha256, ai_sha256, media_type, ai_media_type,
			 bytes, width, height, captured_at, deferred) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, s.tenant, p.MoveID, p.Role, p.SHA256, p.AISHA256, p.MediaType, p.AIMediaType,
			p.Bytes, p.Width, p.Height, ts(p.CapturedAt), boolInt(p.Deferred))
```

4. Exclude deferred photos from `PhotosWithoutAttempts`, and update its comment:

```go
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
```

5. Add `GetPhoto` and `MarkForReading` below `ListPhotos`:

```go
// GetPhoto returns one photo.
func (s *Store) GetPhoto(ctx context.Context, id string) (Photo, error) {
	p, err := scanPhoto(s.db.QueryRowContext(ctx, `SELECT `+photoColumns+` FROM photos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	return p, err
}

// MarkForReading chooses a deferred photo for reading.
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
		if _, err := tx.ExecContext(ctx, `UPDATE photos SET deferred = 0 WHERE id = ?`, id); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, moveID, "photo.chosen", actor, map[string]any{"photo_id": id})
	})
}
```

- [ ] **Step 6: Settings**

Create `internal/store/settings.go`:

```go
package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// Settings returns every setting whose key starts with prefix. The table is
// tiny, so filtering happens here rather than in SQL.
func (s *Store) Settings(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, rows.Err()
}

// SetSettings stores values in one transaction. The event names the keys but
// never the values, because settings include passwords.
func (s *Store) SetSettings(ctx context.Context, values map[string]string, actor string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := ts(s.now())
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				k, values[k], now); err != nil {
				return err
			}
		}
		return s.appendEvent(ctx, tx, "settings", "settings.changed", actor, map[string]any{"keys": keys})
	})
}
```

- [ ] **Step 7: Run the tests**

Run: `go test ./...`
Expected: all packages PASS. The existing tests compile unchanged because `CreateMove` and `AddPhoto` keep their signatures, and a zero `Deferred` means "read it".

- [ ] **Step 8: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/store
git commit -m "feat(store): camera moves, deferred photos and settings (schema v2)"
```

---

### Task 2: Photo previews (`?w=`)

**Files:**
- Create: `internal/thumb/thumb.go`
- Test: `internal/thumb/thumb_test.go`
- Modify: `internal/httpapi/handlers.go` (`getBlob`, new `previewWidth`)
- Test: `internal/httpapi/server_test.go` (append one test)

- [ ] **Step 1: Write the failing tests**

Create `internal/thumb/thumb_test.go`:

```go
package thumb

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestScaleShrinksToWidth(t *testing.T) {
	out, err := Scale(testJPEG(t, 2000, 1000), 320)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 320 || b.Dy() != 160 {
		t.Fatalf("size = %dx%d; want 320x160", b.Dx(), b.Dy())
	}
	r, g, b, _ := img.At(160, 80).RGBA()
	if r>>8 < 170 || g>>8 > 80 || b>>8 > 80 {
		t.Fatalf("color = %d,%d,%d; want about 200,40,40", r>>8, g>>8, b>>8)
	}
}

func TestScaleKeepsNarrowImages(t *testing.T) {
	in := testJPEG(t, 300, 200)
	out, err := Scale(in, 320)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("an image narrower than the width must come back unchanged")
	}
}

func TestScaleRejectsNonImages(t *testing.T) {
	if _, err := Scale([]byte("not an image"), 320); !errors.Is(err, ErrNotImage) {
		t.Fatalf("err = %v; want ErrNotImage", err)
	}
}

func TestAllowedWidths(t *testing.T) {
	for _, w := range []int{160, 320, 640, 1280} {
		if !Allowed(w) {
			t.Errorf("Allowed(%d) = false", w)
		}
	}
	for _, w := range []int{0, 100, 4000, -1} {
		if Allowed(w) {
			t.Errorf("Allowed(%d) = true", w)
		}
	}
}
```

Append to `internal/httpapi/server_test.go`:

```go
func TestBlobPreviewWidthIsChecked(t *testing.T) {
	srv := newTestServer(t, "")
	client := srv.Client()
	_, d := createAndRead(t, client, srv.URL, "orig-preview")
	if len(d.Photos) == 0 {
		t.Fatal("move has no photos")
	}
	digest := d.Photos[0].SHA256
	if status, body := call(t, client, "GET", srv.URL+"/api/blobs/"+digest+"?w=999", ""); status != 400 || errorCode(body) != "E4006" {
		t.Fatalf("w=999 = %d %s; want 400 E4006", status, body)
	}
	// The test photo is not a real image: a preview falls back to the original.
	if status, _ := call(t, client, "GET", srv.URL+"/api/blobs/"+digest+"?w=320", ""); status != 200 {
		t.Fatalf("w=320 = %d; want 200", status)
	}
}
```

(`createAndRead`, `call`, `errorCode` and the `detail` struct with `Photos[].SHA256` already exist in `server_test.go`.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/thumb/ ./internal/httpapi/`
Expected: `thumb`: `undefined: Scale`. `httpapi`: the w=999 request returns 200 instead of 400.

- [ ] **Step 3: Implement thumb**

Create `internal/thumb/thumb.go`:

```go
// Package thumb makes small JPEG previews of photos for screens, so phones do
// not download 4K camera stills just to show a thumbnail.
package thumb

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png" // phone photos may be PNG
)

// widths are the preview widths the API serves. Anything else is refused, so
// nobody can make the hub do unbounded work.
var widths = []int{160, 320, 640, 1280}

// Allowed reports whether w is a preview width the API serves.
func Allowed(w int) bool {
	for _, x := range widths {
		if x == w {
			return true
		}
	}
	return false
}

// ErrNotImage means the data is not a JPEG or PNG image.
var ErrNotImage = errors.New("not a JPEG or PNG image")

// Scale returns a JPEG at most width pixels wide. Images that are already
// narrow enough come back unchanged.
func Scale(data []byte, width int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= width {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	height := max(1, cfg.Height*width/cfg.Width)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shrink(src, width, height), &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// samples caps how many source pixels per axis feed one preview pixel. A box
// average over at most 4×4 samples looks smooth and keeps a 4K still under
// about 100 ms.
const samples = 4

// shrink box-averages src down to w×h.
func shrink(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+(y+1)*sh/h
		y1 = max(y1, y0+1)
		ystep := max(1, (y1-y0)/samples)
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+(x+1)*sw/w
			x1 = max(x1, x0+1)
			xstep := max(1, (x1-x0)/samples)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy += ystep {
				for sx := x0; sx < x1; sx += xstep {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8((r / n) >> 8)
			dst.Pix[i+1] = uint8((g / n) >> 8)
			dst.Pix[i+2] = uint8((bl / n) >> 8)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}
```

- [ ] **Step 4: Serve previews**

In `internal/httpapi/handlers.go`, add `"depot/internal/thumb"` to the imports, and add this helper below `formFile`:

```go
// previewWidth reads the optional ?w= preview width (0 when absent). ok is
// false when the value is not allowed; the error answer has been written.
func previewWidth(w http.ResponseWriter, r *http.Request) (width int, ok bool) {
	q := r.URL.Query().Get("w")
	if q == "" {
		return 0, true
	}
	n, err := strconv.Atoi(q)
	if err != nil || !thumb.Allowed(n) {
		writeError(w, http.StatusBadRequest, "E4006", "w must be one of 160, 320, 640, 1280")
		return 0, false
	}
	return n, true
}
```

In `getBlob`, insert this after the `switch` on errors and before the content-type comment:

```go
	width, ok := previewWidth(w, r)
	if !ok {
		return
	}
	if width > 0 {
		if small, err := thumb.Scale(data, width); err == nil {
			data = small
		}
	}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/thumb internal/httpapi
git commit -m "feat: small JPEG previews of stored photos (?w=)"
```

---

### Task 3: Ask Gemini to look closer at camera stills

**Files:**
- Modify: `internal/recognition/recognizer.go`
- Modify: `internal/recognition/gemini.go` (`request`)
- Test: `internal/recognition/gemini_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/recognition/gemini_test.go`:

```go
func TestGeminiAsksForUltraHighDetailOnlyWhenTold(t *testing.T) {
	var got []any
	srv, _ := fakeGemini(t, []int{200}, []string{geminiOK}, func(r *http.Request, p map[string]any) {
		got = append(got, dig(p, "contents", 0, "parts", 0, "mediaResolution", "level"))
	})
	g := testGemini(srv, 0)
	if _, err := g.Read(context.Background(), jpeg); err != nil {
		t.Fatal(err)
	}
	still := jpeg
	still.Detail = DetailUltraHigh
	if _, err := g.Read(context.Background(), still); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != nil || got[1] != "MEDIA_RESOLUTION_ULTRA_HIGH" {
		t.Fatalf("mediaResolution levels = %v; want [<nil> MEDIA_RESOLUTION_ULTRA_HIGH]", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/recognition/ -run UltraHigh`
Expected: compile error `still.Detail undefined` / `undefined: DetailUltraHigh`.

- [ ] **Step 3: Implement**

In `internal/recognition/recognizer.go`, replace `Image` with:

```go
// Image is one photo as sent to an engine.
type Image struct {
	Data      []byte
	MediaType string // image/jpeg, image/png or image/webp
	// Detail asks the engine to look closer. Use DetailUltraHigh for wide 4K
	// camera stills, where the marks are small. Empty keeps the engine default.
	Detail string
}

// DetailUltraHigh is the Detail for 4K camera stills.
const DetailUltraHigh = "ultra_high"
```

In `internal/recognition/gemini.go`, replace the `return map[string]any{...}` at the end of `request` with:

```go
	image := map[string]any{"inlineData": map[string]any{
		"mimeType": img.MediaType,
		"data":     base64.StdEncoding.EncodeToString(img.Data),
	}}
	if img.Detail == DetailUltraHigh {
		// Per-part resolution (Gemini 3): twice the image tokens of the
		// default, for container marks that are small in a wide 4K still.
		image["mediaResolution"] = map[string]any{"level": "MEDIA_RESOLUTION_ULTRA_HIGH"}
	}
	return map[string]any{
		"contents": []any{map[string]any{
			"role":  "user",
			"parts": []any{image, map[string]any{"text": Prompt}},
		}},
		"generationConfig": config,
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/recognition/`
Expected: PASS, including the existing `TestGeminiSendsImageSchemaAndThinking`, which still finds the image at part 0 and the prompt at part 1.

- [ ] **Step 5: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/recognition
git commit -m "feat(recognition): ultra-high image detail for camera stills"
```

---
### Task 4: Gate service support for camera visits

The watcher (Task 6) will drive camera moves through four new service methods. A **read hook** tells it how each read went. Deferred photos are ignored by recompute, Start and Reread. A camera move stays open while its visit may still choose photos. Once settled, it is never auto-accepted, and it becomes `no_container` when every read succeeded without finding a number.

**Files:**
- Create: `internal/gate/camera.go`
- Modify: `internal/gate/service.go`
- Test: `internal/gate/camera_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/gate/camera_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gate/`
Expected: compile errors such as `h.svc.SetReadHook undefined` and `h.svc.NewCameraMove undefined`.

- [ ] **Step 3: Add the camera methods**

Create `internal/gate/camera.go`:

```go
package gate

import (
	"context"
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
	s.notify(m.ID)
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
		Bytes: int64(len(jpeg)), Width: width, Height: height, Deferred: true,
	}, "camera")
	if err != nil {
		return store.Photo{}, err
	}
	s.notify(m.ID)
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
	if err := s.store.SetStatus(ctx, p.MoveID, store.StatusReading); err != nil {
		return err
	}
	s.notify(p.MoveID)
	s.enqueue(job{moveID: p.MoveID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType})
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
	s.notify(moveID)
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

// noContainer reports whether every read of a camera move succeeded and none
// found a number. A failed read is not proof of an empty lane.
func noContainer(frames []recognition.Frame) bool {
	for _, f := range frames {
		if f.Error != "" || len(f.Candidates) > 0 {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Teach the service about deferred photos and camera moves**

In `internal/gate/service.go`:

1. Add fields to `Service`, after `wg`:

```go
	hook    ReadHook        // told about every finished read; may be nil
	visitMu sync.Mutex      // guards visits
	visits  map[string]bool // camera moves whose visit may still choose photos
```

2. Initialise `visits: map[string]bool{}` in the `&Service{...}` literal in `New`.

3. Add `role` to `job`:

```go
type job struct {
	moveID, photoID, role, aiSHA, aiType string
}
```

4. Pass `role: p.Role` at every `enqueue(job{...})` call: in `AddPhoto`, in `Start` and in `Reread`.

5. Replace the end of `Start` and the whole of `settleStuckReading` with:

```go
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
			s.notify(m.ID)
		}
		if len(moves) < page {
			return nil
		}
		f.Before = moves[len(moves)-1].ID
	}
}
```

6. Replace `Reread` with:

```go
// Reread queues every chosen photo of an open move for another engine run,
// e.g. after an engine outage. Deferred camera photos stay unread.
func (s *Service) Reread(ctx context.Context, moveID string) error {
	m, err := s.openMove(ctx, moveID)
	if err != nil {
		return err
	}
	var photos []store.Photo
	for _, p := range m.Photos {
		if !p.Deferred {
			photos = append(photos, p)
		}
	}
	if len(photos) == 0 {
		return nil
	}
	if err := s.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		return err
	}
	s.notify(m.ID)
	for _, p := range photos {
		s.enqueue(job{moveID: m.ID, photoID: p.ID, role: p.Role, aiSHA: p.AISHA256, aiType: p.AIMediaType})
	}
	return nil
}
```

7. Replace `TestRead` with a thin wrapper around a shared `testRead`:

```go
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
```

8. Replace `process` with:

```go
func (s *Service) process(j job) {
	ctx := s.ctx
	img := recognition.Image{MediaType: j.aiType}
	if m, err := s.store.GetMove(ctx, j.moveID); err == nil && m.Source == store.SourceCamera {
		img.Detail = recognition.DetailUltraHigh // wide 4K stills: the marks are small
	}
	data, err := s.blobs.Get(j.aiSHA)
	att := recognition.Attempt{Engine: s.rec.Name(), Model: s.rec.Model(), PromptVersion: recognition.PromptVersion}
	if err == nil {
		img.Data = data
		att, err = s.rec.Read(ctx, img)
	}
	if ctx.Err() != nil {
		return // shutting down: the photo stays unread and Start re-queues it
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
	s.notify(j.moveID)
}
```

9. In `recompute`, skip deferred photos, count each camera still as its own view, and add the camera rules. Replace the photo loop with:

```go
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
```

The status `switch` becomes:

```go
	res := recognition.Reconcile(frames, s.cfg.AutoAcceptThreshold)
	status := store.StatusNeedsReview
	switch {
	case pending > 0:
		status = store.StatusReading
	case camera && s.visitOpen(m.ID):
		status = store.StatusCapturing // the visit may still choose photos to read
	case camera && noContainer(frames):
		status, res.Reason = store.StatusNoContainer, "no container number in any photo"
	case camera:
		// A person confirms every camera read in this kit (camera spec §2.2).
		if res.Decision == recognition.DecisionAutoAccept {
			res.Decision, res.Reason = recognition.DecisionReview, "camera reads are confirmed by a person"
		}
	case res.Decision == recognition.DecisionAutoAccept && s.cfg.AutoAcceptEnabled:
		status = store.StatusAccepted
	}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/gate/ -count=3` and then `go test ./...`
Expected: PASS, with the existing gate tests unchanged and passing.

- [ ] **Step 6: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/gate
git commit -m "feat(gate): camera moves with deferred frames, read hook and no-container outcome"
```

---

### Task 5: Camera package (interface, Reolink client, fake)

**Files:**
- Create: `internal/camera/camera.go`
- Create: `internal/camera/reolink.go`
- Create: `internal/camera/fake.go`
- Test: `internal/camera/reolink_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/camera/reolink_test.go`:

```go
package camera

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var ctx = context.Background()

// fakeAPI imitates a Reolink camera's /cgi-bin/api.cgi.
type fakeAPI struct {
	mu         sync.Mutex
	user, pass string
	logins     int
	expireNext bool   // the next command answers "please login first"
	aiShape    string // "new", "old" or "none" (no vehicle detection)
	vehicle    int
	motion     int
	cmds       []string
}

func (f *fakeAPI) token() string { return fmt.Sprintf("tok%d", f.logins) }

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/cgi-bin/api.cgi" {
		w.WriteHeader(http.StatusOK) // the camera's web page
		return
	}
	cmd := r.URL.Query().Get("cmd")
	f.cmds = append(f.cmds, cmd)
	reply := func(v any) { _ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 0, "value": v}}) }
	fail := func(rsp int, detail string) {
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"cmd": cmd, "code": 1, "error": map[string]any{"rspCode": rsp, "detail": detail}}})
	}
	if cmd != "Login" && (r.URL.Query().Get("token") != f.token() || f.expireNext) {
		f.expireNext = false
		fail(-6, "please login first")
		return
	}
	switch cmd {
	case "Login":
		var body []struct {
			Param struct {
				User struct {
					UserName string `json:"userName"`
					Password string `json:"password"`
				} `json:"User"`
			} `json:"param"`
		}
		data, _ := io.ReadAll(r.Body)
		if json.Unmarshal(data, &body) != nil || len(body) == 0 || body[0].Param.User.UserName != f.user || body[0].Param.User.Password != f.pass {
			fail(-7, "login failed")
			return
		}
		f.logins++
		reply(map[string]any{"Token": map[string]any{"leaseTime": 3600, "name": f.token()}})
	case "Snap":
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(DemoStill())
	case "GetAiState":
		switch f.aiShape {
		case "old":
			reply(map[string]any{"channel": 0, "people_state": 0, "vehicle_state": f.vehicle})
		case "none":
			fail(-9, "not support")
		default:
			reply(map[string]any{"channel": 0, "people": map[string]int{"alarm_state": 0, "support": 1},
				"vehicle": map[string]int{"alarm_state": f.vehicle, "support": 1}})
		}
	case "GetMdState":
		reply(map[string]any{"state": f.motion})
	case "GetDevInfo":
		reply(map[string]any{"DevInfo": map[string]any{"model": "RLC-810WA", "firmVer": "v3.1.0.2368"}})
	case "GetNetPort":
		reply(map[string]any{"NetPort": map[string]any{"httpEnable": 1, "rtspEnable": 1, "rtspPort": 554}})
	case "GetEnc":
		reply(map[string]any{"Enc": map[string]any{"mainStream": map[string]any{"size": "3840*2160"}}})
	default:
		fail(-1, "unknown command")
	}
}

func (f *fakeAPI) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func newFakeCamera(t *testing.T) (*fakeAPI, *Reolink) {
	t.Helper()
	api := &fakeAPI{user: "admin", pass: "pw-secret", aiShape: "new"}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: "pw-secret"})
}

func TestReolinkLogsInOnceAndReusesTheToken(t *testing.T) {
	api, cam := newFakeCamera(t)
	for i := 0; i < 3; i++ {
		if _, err := cam.Detect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := api.loginCount(); n != 1 {
		t.Fatalf("logins = %d; want 1", n)
	}
}

func TestReolinkLogsInAgainWhenTheSessionExpires(t *testing.T) {
	api, cam := newFakeCamera(t)
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	api.expireNext = true
	api.mu.Unlock()
	if _, err := cam.Detect(ctx); err != nil {
		t.Fatalf("Detect after expiry = %v", err)
	}
	if n := api.loginCount(); n != 2 {
		t.Fatalf("logins = %d; want 2", n)
	}
}

func TestReolinkReadsBothAiStateShapesAndFallsBackToMotion(t *testing.T) {
	for _, tc := range []struct {
		shape string
		want  Detection
	}{
		{"new", Detection{Present: true, Kind: KindVehicle}},
		{"old", Detection{Present: true, Kind: KindVehicle}},
		{"none", Detection{Present: true, Kind: KindMotion}},
	} {
		api, cam := newFakeCamera(t)
		api.aiShape, api.vehicle, api.motion = tc.shape, 1, 1
		got, err := cam.Detect(ctx)
		if err != nil || got != tc.want {
			t.Errorf("%s: Detect = %+v, %v; want %+v", tc.shape, got, err, tc.want)
		}
	}
}

func TestReolinkWrongPasswordIsAuthErrorWithoutThePassword(t *testing.T) {
	api := &fakeAPI{user: "admin", pass: "pw-secret", aiShape: "new"}
	srv := httptest.NewServer(api)
	defer srv.Close()
	cam := NewReolink(Settings{Host: strings.TrimPrefix(srv.URL, "http://"), User: "admin", Password: "wrong-pw"})
	_, err := cam.Detect(ctx)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v; want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestReolinkSnapshotIsAJPEG(t *testing.T) {
	_, cam := newFakeCamera(t)
	data, err := cam.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if http.DetectContentType(data) != "image/jpeg" {
		t.Fatalf("snapshot is %s", http.DetectContentType(data))
	}
}

func TestReolinkUnreachableErrorHidesTheToken(t *testing.T) {
	cam := NewReolink(Settings{Host: "127.0.0.1:1", User: "admin", Password: "pw"})
	_, err := cam.Detect(ctx)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v; want ErrUnreachable", err)
	}
	if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("error leaks a secret: %v", err)
	}
}

func TestReolinkRawAnswerHidesTokenAndPassword(t *testing.T) {
	_, cam := newFakeCamera(t)
	if _, err := cam.login(ctx, true); err != nil {
		t.Fatal(err)
	}
	raw := cam.lastRaw()
	if !strings.Contains(raw, "Login") || strings.Contains(raw, "tok1") || strings.Contains(raw, "pw-secret") {
		t.Fatalf("raw answer = %s", raw)
	}
}

func TestReolinkTestReportIsAllGreen(t *testing.T) {
	_, cam := newFakeCamera(t)
	rep := cam.Test(ctx)
	if !rep.OK() {
		t.Fatalf("report = %+v", rep)
	}
	names := []string{}
	for _, s := range rep.Steps {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "reach,login,info,ports,detect,snapshot" {
		t.Fatalf("steps = %v", names)
	}
	if rep.Info.Model != "RLC-810WA" || rep.Info.Width != 3840 || rep.Info.Height != 2160 || !rep.Info.RTSP || rep.Info.Detection != KindVehicle {
		t.Fatalf("info = %+v", rep.Info)
	}
	if rep.Raw == "" {
		t.Fatal("report has no raw answer")
	}
}

func TestReolinkTestReportExplainsAnUnreachableCamera(t *testing.T) {
	cam := NewReolink(Settings{Host: "127.0.0.1:1", User: "admin", Password: "pw"})
	rep := cam.Test(ctx)
	if rep.OK() || len(rep.Steps) != 1 || rep.Steps[0].Name != "reach" || !strings.Contains(rep.Steps[0].Detail, "Port Settings") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestReolinkLiveSourceUsesTheSubStreamOnPort554(t *testing.T) {
	cam := NewReolink(Settings{Host: "192.168.1.50:80", User: "admin", Password: "p@ss word"})
	want := "rtsp://admin:p%40ss%20word@192.168.1.50:554/h264Preview_01_sub"
	if got := cam.LiveSource(); got != want {
		t.Fatalf("LiveSource = %s; want %s", got, want)
	}
}

func TestFakeCamera(t *testing.T) {
	f := NewFake([]byte("a"), []byte("b"))
	d, _ := f.Detect(ctx)
	if d.Present {
		t.Fatal("vehicle present before SetVehicle")
	}
	f.SetVehicle(true)
	if d, _ := f.Detect(ctx); !d.Present || d.Kind != KindVehicle {
		t.Fatalf("Detect = %+v", d)
	}
	s1, _ := f.Snapshot(ctx)
	s2, _ := f.Snapshot(ctx)
	s3, _ := f.Snapshot(ctx)
	if string(s1) != "a" || string(s2) != "b" || string(s3) != "a" || f.Snaps() != 3 {
		t.Fatalf("stills = %q %q %q, snaps %d", s1, s2, s3, f.Snaps())
	}
	f.SetSnapErr(ErrUnreachable)
	if _, err := f.Snapshot(ctx); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Snapshot = %v", err)
	}
	if http.DetectContentType(DemoStill()) != "image/jpeg" {
		t.Fatal("DemoStill is not a JPEG")
	}
	if !NewDemoFake().Test(ctx).OK() {
		t.Fatal("demo fake test report is not green")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/camera/`
Expected: compile errors (`undefined: NewReolink`, `undefined: Settings`, ...).

- [ ] **Step 3: The interface and shared types**

Create `internal/camera/camera.go`:

```go
// Package camera talks to fixed gate cameras: still photos, the camera's own
// vehicle detection, and the address of its live video.
package camera

import (
	"context"
	"errors"
)

// Settings are what a person enters to connect a camera.
type Settings struct {
	Host     string // address, optionally with the HTTP port: 192.168.1.50 or 192.168.1.50:8080
	User     string
	Password string
	Auto     bool // the camera's vehicle detection starts photo bursts
}

// Configured reports whether there is enough to connect.
func (s Settings) Configured() bool { return s.Host != "" && s.User != "" }

// Detection kinds.
const (
	KindVehicle = "vehicle"
	KindMotion  = "motion" // the camera cannot tell vehicles from other movement
)

// Info describes the connected camera.
type Info struct {
	Model     string `json:"model"`
	Firmware  string `json:"firmware"`
	Width     int    `json:"width"`  // main stream, i.e. still size
	Height    int    `json:"height"` //
	RTSP      bool   `json:"rtsp"`   // RTSP switched on (needed for live video)
	Detection string `json:"detection"`
}

// Detection is the camera's answer to "is a vehicle there now?".
type Detection struct {
	Present bool   `json:"present"`
	Kind    string `json:"kind"`
}

// Step is one line of a connection test: reach, login, info, ports, detect
// or snapshot.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the outcome of a connection test.
type Report struct {
	Steps []Step `json:"steps"`
	Info  Info   `json:"info"`
	Raw   string `json:"raw"` // the camera's last answer, secrets removed
}

// OK reports whether every step passed.
func (r Report) OK() bool {
	for _, s := range r.Steps {
		if !s.OK {
			return false
		}
	}
	return len(r.Steps) > 0
}

// Camera errors. ErrAuth must stop polling: cameras lock logins after a few
// wrong passwords.
var (
	ErrUnreachable = errors.New("camera unreachable")
	ErrAuth        = errors.New("camera rejected the user name or password")
	ErrProtocol    = errors.New("camera answered something unexpected")

	errRTSPOff     = errors.New("RTSP is off")
	errNoDetection = errors.New("the camera did not answer a detection query")
)

// Camera is one fixed camera.
type Camera interface {
	Info(ctx context.Context) (Info, error)
	// Snapshot returns a full-resolution JPEG.
	Snapshot(ctx context.Context) ([]byte, error)
	Detect(ctx context.Context) (Detection, error)
	// LiveSource is the RTSP address of the small live stream, credentials
	// included, or "" when the camera has none.
	LiveSource() string
	// Test checks the connection step by step.
	Test(ctx context.Context) Report
}

// advice turns a failed step into what the person should do.
func advice(step string, err error) string {
	switch {
	case errors.Is(err, ErrUnreachable):
		return "Cannot connect. Check the address, that the camera has power and is on this network, " +
			"and that HTTP is on (Reolink app: Settings → Network → Advanced → Port Settings). " + err.Error()
	case errors.Is(err, ErrAuth):
		return "Wrong user name or password. Careful: the camera locks logins after several wrong tries. " + err.Error()
	case errors.Is(err, errRTSPOff):
		return "RTSP is off: turn it on in Port Settings, otherwise there is no live video."
	default:
		return err.Error()
	}
}
```

- [ ] **Step 4: The Reolink client**

Create `internal/camera/reolink.go`:

```go
package camera

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reolink talks to a Reolink camera's HTTP API (/cgi-bin/api.cgi). New
// firmware ships with HTTP and RTSP switched off: they must be turned on in
// the camera (Settings → Network → Advanced → Port Settings).
type Reolink struct {
	host, user, password string
	client               *http.Client
	now                  func() time.Time

	loginMu sync.Mutex // one login at a time: the camera limits sessions

	mu       sync.Mutex
	token    string
	tokenEnd time.Time
	kind     string // detection kind, once known
	raw      string // last API answer, token and password removed
}

var _ Camera = (*Reolink)(nil)

// NewReolink describes the camera at s.Host. Nothing is sent until the first
// call.
func NewReolink(s Settings) *Reolink {
	return &Reolink{
		host: s.Host, user: s.User, password: s.Password,
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
}

// rspLoginFirst is the camera's "please login first": the session expired.
const rspLoginFirst = -6

type apiReply struct {
	Cmd   string          `json:"cmd"`
	Code  int             `json:"code"`
	Value json.RawMessage `json:"value"`
	Error *struct {
		Detail  string `json:"detail"`
		RspCode int    `json:"rspCode"`
	} `json:"error"`
}

func (r apiReply) expired() bool { return r.Error != nil && r.Error.RspCode == rspLoginFirst }

func (r apiReply) detail() string {
	if r.Error == nil {
		return fmt.Sprintf("code %d", r.Code)
	}
	return fmt.Sprintf("%s (rspCode %d)", r.Error.Detail, r.Error.RspCode)
}

func (c *Reolink) endpoint(q url.Values) string {
	return "http://" + c.host + "/cgi-bin/api.cgi?" + q.Encode()
}

// cmd runs one API command, logging in first when needed and once more if
// the camera says the session expired.
func (c *Reolink) cmd(ctx context.Context, name string, param, out any) error {
	for try := 0; ; try++ {
		token, err := c.login(ctx, try > 0)
		if err != nil {
			return err
		}
		reply, err := c.post(ctx, name, token, param)
		if err != nil {
			return err
		}
		if reply.Code != 0 {
			if reply.expired() && try == 0 {
				continue
			}
			return fmt.Errorf("%w: %s failed: %s", ErrProtocol, name, reply.detail())
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(reply.Value, out); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrProtocol, name, err)
		}
		return nil
	}
}

// login returns a valid session token. It logs in when there is none, when
// the current one is about to expire, or when force is set.
func (c *Reolink) login(ctx context.Context, force bool) (string, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	c.mu.Lock()
	if !force && c.token != "" && c.now().Before(c.tokenEnd) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()
	param := map[string]any{"User": map[string]any{"userName": c.user, "password": c.password}}
	reply, err := c.post(ctx, "Login", "", param)
	if err != nil {
		return "", err
	}
	if reply.Code != 0 {
		return "", fmt.Errorf("%w: %s", ErrAuth, reply.detail())
	}
	var v struct {
		Token struct {
			LeaseTime int    `json:"leaseTime"`
			Name      string `json:"name"`
		} `json:"Token"`
	}
	if err := json.Unmarshal(reply.Value, &v); err != nil || v.Token.Name == "" {
		return "", fmt.Errorf("%w: login answer has no token", ErrProtocol)
	}
	lease := max(time.Duration(v.Token.LeaseTime)*time.Second, 2*time.Minute)
	c.mu.Lock()
	c.token, c.tokenEnd = v.Token.Name, c.now().Add(lease-time.Minute) // renew a minute early
	c.mu.Unlock()
	return v.Token.Name, nil
}

// post sends one command and returns the camera's first reply.
func (c *Reolink) post(ctx context.Context, name, token string, param any) (apiReply, error) {
	q := url.Values{"cmd": {name}}
	if token != "" {
		q.Set("token", token)
	}
	body, err := json.Marshal([]any{map[string]any{"cmd": name, "action": 0, "param": param}})
	if err != nil {
		return apiReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(q), bytes.NewReader(body))
	if err != nil {
		return apiReply{}, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return apiReply{}, c.unreachable(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return apiReply{}, c.unreachable(err)
	}
	c.remember(name, data)
	if res.StatusCode != http.StatusOK {
		return apiReply{}, fmt.Errorf("%w: %s: HTTP %d", ErrProtocol, name, res.StatusCode)
	}
	var replies []apiReply
	if err := json.Unmarshal(data, &replies); err != nil || len(replies) == 0 {
		return apiReply{}, fmt.Errorf("%w: %s: not a camera API answer", ErrProtocol, name)
	}
	return replies[0], nil
}

// unreachable wraps a transport error. The URL is dropped from it because it
// carries the session token.
func (c *Reolink) unreachable(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%w: %s: %v", ErrUnreachable, c.host, err)
}

var tokenRE = regexp.MustCompile(`("name"\s*:\s*")[^"]*(")`)

// remember keeps the camera's last answer for the "raw answer" view, with the
// session token and password removed.
func (c *Reolink) remember(cmd string, data []byte) {
	raw := string(data)
	if len(raw) > 4000 {
		raw = raw[:4000] + "…"
	}
	if cmd == "Login" {
		raw = tokenRE.ReplaceAllString(raw, "${1}***${2}")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		raw = strings.ReplaceAll(raw, c.token, "***")
	}
	if c.password != "" {
		raw = strings.ReplaceAll(raw, c.password, "***")
	}
	c.raw = cmd + ": " + raw
}

func (c *Reolink) lastRaw() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw
}

// Snapshot fetches a full-resolution JPEG.
func (c *Reolink) Snapshot(ctx context.Context) ([]byte, error) {
	for try := 0; ; try++ {
		token, err := c.login(ctx, try > 0)
		if err != nil {
			return nil, err
		}
		q := url.Values{"cmd": {"Snap"}, "channel": {"0"}, "rs": {strconv.FormatUint(rand.Uint64(), 36)}, "token": {token}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(q), nil)
		if err != nil {
			return nil, err
		}
		res, err := c.client.Do(req)
		if err != nil {
			return nil, c.unreachable(err)
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, 20<<20))
		res.Body.Close()
		if err != nil {
			return nil, c.unreachable(err)
		}
		if res.StatusCode == http.StatusOK && http.DetectContentType(data) == "image/jpeg" {
			return data, nil
		}
		// Failures come back as JSON, such as an expired session.
		c.remember("Snap", data)
		var replies []apiReply
		if json.Unmarshal(data, &replies) == nil && len(replies) > 0 && replies[0].expired() && try == 0 {
			continue
		}
		return nil, fmt.Errorf("%w: Snap: HTTP %d, %d bytes, not a JPEG", ErrProtocol, res.StatusCode, len(data))
	}
}

// Detect asks whether a vehicle is in the camera's detection zone. Cameras
// without vehicle detection answer with plain motion detection instead.
func (c *Reolink) Detect(ctx context.Context) (Detection, error) {
	c.mu.Lock()
	kind := c.kind
	c.mu.Unlock()
	if kind != KindMotion {
		var v map[string]json.RawMessage
		err := c.cmd(ctx, "GetAiState", map[string]any{"channel": 0}, &v)
		if err != nil && !errors.Is(err, ErrProtocol) {
			return Detection{}, err
		}
		if present, ok := vehicleState(v); err == nil && ok {
			c.setKind(KindVehicle)
			return Detection{Present: present, Kind: KindVehicle}, nil
		}
		c.setKind(KindMotion) // firmware without vehicle detection
	}
	var md struct {
		State int `json:"state"`
	}
	if err := c.cmd(ctx, "GetMdState", map[string]any{"channel": 0}, &md); err != nil {
		return Detection{}, err
	}
	return Detection{Present: md.State == 1, Kind: KindMotion}, nil
}

func (c *Reolink) setKind(k string) {
	c.mu.Lock()
	c.kind = k
	c.mu.Unlock()
}

// vehicleState reads GetAiState in either shape that firmware uses:
// {"vehicle": {"alarm_state": 1, "support": 1}} or {"vehicle_state": 1}.
func vehicleState(v map[string]json.RawMessage) (present, supported bool) {
	if raw, ok := v["vehicle"]; ok {
		var o struct {
			AlarmState int `json:"alarm_state"`
			Support    int `json:"support"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Support == 1 {
			return o.AlarmState == 1, true
		}
		return false, false
	}
	if raw, ok := v["vehicle_state"]; ok {
		var n int
		if json.Unmarshal(raw, &n) == nil {
			return n == 1, true
		}
	}
	return false, false
}

// Info reads model, firmware, still size, RTSP state and detection kind.
func (c *Reolink) Info(ctx context.Context) (Info, error) {
	var info Info
	var dev struct {
		DevInfo struct {
			Model   string `json:"model"`
			FirmVer string `json:"firmVer"`
		} `json:"DevInfo"`
	}
	if err := c.cmd(ctx, "GetDevInfo", map[string]any{}, &dev); err != nil {
		return info, err
	}
	info.Model, info.Firmware = dev.DevInfo.Model, dev.DevInfo.FirmVer
	var ports struct {
		NetPort struct {
			RtspEnable int `json:"rtspEnable"`
		} `json:"NetPort"`
	}
	if err := c.cmd(ctx, "GetNetPort", map[string]any{}, &ports); err == nil {
		info.RTSP = ports.NetPort.RtspEnable == 1
	}
	var enc struct {
		Enc struct {
			MainStream struct {
				Size string `json:"size"`
			} `json:"mainStream"`
		} `json:"Enc"`
	}
	if err := c.cmd(ctx, "GetEnc", map[string]any{"channel": 0}, &enc); err == nil {
		info.Width, info.Height = parseSize(enc.Enc.MainStream.Size)
	}
	if d, err := c.Detect(ctx); err == nil {
		info.Detection = d.Kind
	}
	return info, nil
}

// parseSize reads Reolink's "3840*2160".
func parseSize(s string) (int, int) {
	a, b, ok := strings.Cut(s, "*")
	if !ok {
		return 0, 0
	}
	w, _ := strconv.Atoi(a)
	h, _ := strconv.Atoi(b)
	return w, h
}

// reach checks that something answers HTTP at the camera's address.
func (c *Reolink) reach(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.host+"/", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	res.Body.Close()
	return nil
}

// Test checks the connection step by step, so the Camera page can say
// exactly what to fix.
func (c *Reolink) Test(ctx context.Context) (r Report) {
	defer func() { r.Raw = c.lastRaw() }()
	step := func(name string, err error, detail string) bool {
		s := Step{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			s.Detail = advice(name, err)
		}
		r.Steps = append(r.Steps, s)
		return err == nil
	}
	if !step("reach", c.reach(ctx), "http://"+c.host) {
		return r
	}
	if _, err := c.login(ctx, true); !step("login", err, c.user) {
		return r
	}
	info, err := c.Info(ctx)
	r.Info = info
	if !step("info", err, strings.TrimSpace(info.Model+" "+info.Firmware)) {
		return r
	}
	var portErr error
	if !info.RTSP {
		portErr = errRTSPOff
	}
	step("ports", portErr, "RTSP on")
	switch info.Detection {
	case KindVehicle:
		step("detect", nil, "vehicle detection")
	case KindMotion:
		step("detect", nil, "motion only: this camera cannot tell vehicles from other movement")
	default:
		step("detect", errNoDetection, "")
	}
	still, err := c.Snapshot(ctx)
	detail := ""
	if err == nil {
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(still)); err == nil {
			detail = fmt.Sprintf("%d×%d, %d KB", cfg.Width, cfg.Height, len(still)/1024)
		}
	}
	step("snapshot", err, detail)
	return r
}

// LiveSource is the H.264 sub stream: small, and playable in every browser.
func (c *Reolink) LiveSource() string {
	host := c.host
	if h, _, err := net.SplitHostPort(c.host); err == nil {
		host = h // the HTTP port; RTSP is on 554
	}
	u := url.URL{Scheme: "rtsp", User: url.UserPassword(c.user, c.password), Host: net.JoinHostPort(host, "554"), Path: "/h264Preview_01_sub"}
	return u.String()
}
```

- [ ] **Step 5: The fake camera**

Create `internal/camera/fake.go`:

```go
package camera

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"sync"
	"time"
)

// Fake is a scripted camera for tests and for trying the hub without one
// (`depotd hub -fake-camera`).
type Fake struct {
	mu        sync.Mutex
	stills    [][]byte
	next      int
	vehicle   bool
	cycle     time.Duration // when set, a vehicle comes and goes on this period
	start     time.Time
	snapErr   error
	detectErr error
	snaps     int
	detects   int
}

var _ Camera = (*Fake)(nil)

// NewFake serves stills in turn and reports no vehicle until SetVehicle.
func NewFake(stills ...[]byte) *Fake { return &Fake{stills: stills, start: time.Now()} }

// NewDemoFake serves a generated still and reports a vehicle for 20 seconds
// out of every 40.
func NewDemoFake() *Fake {
	f := NewFake(DemoStill())
	f.cycle = 20 * time.Second
	return f
}

func (f *Fake) SetVehicle(present bool) { f.mu.Lock(); f.vehicle = present; f.mu.Unlock() }
func (f *Fake) SetSnapErr(err error)    { f.mu.Lock(); f.snapErr = err; f.mu.Unlock() }
func (f *Fake) SetDetectErr(err error)  { f.mu.Lock(); f.detectErr = err; f.mu.Unlock() }

// Snaps counts stills served; Detects counts detection queries.
func (f *Fake) Snaps() int   { f.mu.Lock(); defer f.mu.Unlock(); return f.snaps }
func (f *Fake) Detects() int { f.mu.Lock(); defer f.mu.Unlock(); return f.detects }

func (f *Fake) Info(ctx context.Context) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detectErr != nil {
		return Info{}, f.detectErr
	}
	return Info{Model: "Fake camera", Firmware: "demo", Width: 1280, Height: 720, Detection: KindVehicle}, nil
}

func (f *Fake) Snapshot(ctx context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	if len(f.stills) == 0 {
		return nil, fmt.Errorf("%w: the fake camera has no stills", ErrProtocol)
	}
	s := f.stills[f.next%len(f.stills)]
	f.next++
	f.snaps++
	return s, nil
}

func (f *Fake) Detect(ctx context.Context) (Detection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detects++
	if f.detectErr != nil {
		return Detection{}, f.detectErr
	}
	present := f.vehicle
	if f.cycle > 0 {
		present = (time.Since(f.start)/f.cycle)%2 == 1
	}
	return Detection{Present: present, Kind: KindVehicle}, nil
}

// LiveSource is empty: a fake camera has no video, so pages show stills.
func (f *Fake) LiveSource() string { return "" }

func (f *Fake) Test(ctx context.Context) Report {
	info, err := f.Info(ctx)
	if err != nil {
		return Report{Steps: []Step{{Name: "reach", Detail: advice("reach", err)}}}
	}
	return Report{Info: info, Raw: "fake camera", Steps: []Step{
		{Name: "reach", OK: true, Detail: "fake camera"},
		{Name: "login", OK: true},
		{Name: "info", OK: true, Detail: info.Model},
		{Name: "ports", OK: true, Detail: "a fake camera has no live video"},
		{Name: "detect", OK: true, Detail: "vehicle detection"},
		{Name: "snapshot", OK: true, Detail: "1280×720"},
	}}
}

// DemoStill is a plain picture standing in for a camera still: a blue
// container side with a white plate where the number would be. The demo
// engine's number box covers the plate.
func DemoStill() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 30, G: 80, B: 160, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(384, 144, 794, 216), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/camera/ -count=2`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/camera
git commit -m "feat(camera): Reolink HTTP API client and a scripted fake camera"
```

---
### Task 6: Camera watcher and visits

The watcher's timing logic is all in `step(ctx, now, vehicle, cam)` and `handleRead(ctx, result)`, which take the time as an argument. Tests drive them with a synthetic clock and need no sleeping. `Run` is a thin loop around them.

**Files:**
- Create: `internal/gatecam/watcher.go`
- Create: `internal/gatecam/letters.go`
- Test: `internal/gatecam/watcher_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/gatecam/watcher_test.go`:

```go
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
	closed  bool // AddFrame fails, as for a move a person already resolved
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
	if g.closed {
		return store.Photo{}, errors.New("move already resolved")
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
	g.closed = true
	g.mu.Unlock()
	w.step(ctx, at(1), true, cam)
	if w.visit != nil {
		t.Fatal("visit still running after its move was closed")
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
		{&recognition.Box{X0: 0.3, Y0: 0.2, X1: 0.62, Y1: 0.3}, 1280, 720, 72},   // one line
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gatecam/`
Expected: compile errors (`undefined: New`, `undefined: DefaultConfig`, ...).

- [ ] **Step 3: Letter height**

Create `internal/gatecam/letters.go`:

```go
package gatecam

import (
	"math"

	"depot/internal/recognition"
)

// LetterHeight estimates the height of the container number's letters in
// pixels from the engine's box. A box at least as wide as it is tall is one
// line of text. A taller box is the number written top to bottom, so the
// height is shared by its 11 characters.
func LetterHeight(b *recognition.Box, width, height int) int {
	if b == nil || width <= 0 || height <= 0 {
		return 0
	}
	bw := (b.X1 - b.X0) * float64(width)
	bh := (b.Y1 - b.Y0) * float64(height)
	if bw >= bh {
		return int(math.Round(bh))
	}
	return int(math.Round(bh / 11))
}

// Verdicts on a letter height.
const (
	VerdictGood       = "good"
	VerdictBorderline = "borderline"
	VerdictTooSmall   = "too_small"
)

// Verdict grades a letter height. From 30 px up, numbers read reliably;
// under 20 px they rarely do. Zero means no number was found.
func Verdict(px int) string {
	switch {
	case px <= 0:
		return ""
	case px >= 30:
		return VerdictGood
	case px >= 20:
		return VerdictBorderline
	default:
		return VerdictTooSmall
	}
}
```

- [ ] **Step 4: The watcher**

Create `internal/gatecam/watcher.go`:

```go
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
	ErrNoCamera = errors.New("no camera is set up")
	ErrBusy     = errors.New("a visit is already running")
	ErrStopped  = errors.New("the camera watcher has stopped")
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

	reads    chan readResult
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
	photos   []string     // photo IDs in capture order
	chosen   map[int]bool // indexes of photos sent to the engine
	inFlight int
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
		reads: make(chan readResult, 256), captures: make(chan chan captureReply), done: make(chan struct{}),
		settling: map[string]*visit{}, armed: true,
	}
}

// SetCamera replaces the camera. nil means none is set up.
func (w *Watcher) SetCamera(c camera.Camera) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cam, w.changed = c, true
	state := StateConnecting
	if c == nil {
		state = StateUnconfigured
	}
	w.status = Status{State: state, Since: w.now(), Auto: w.auto}
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
// so it only hands the result over to Run.
func (w *Watcher) OnRead(moveID, photoID string, f recognition.Frame) {
	select {
	case w.reads <- readResult{moveID: moveID, photoID: photoID, frame: f}:
	case <-w.done:
	}
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
		case r := <-w.reads:
			w.handleRead(ctx, r)
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
		return w.cfg.Poll
	}
	if !w.infoOK {
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
	cctx, cancel := context.WithTimeout(ctx, w.cfg.Timeout)
	det, err := cam.Detect(cctx)
	cancel()
	now := w.now()
	if err != nil {
		state := StateOffline
		if errors.Is(err, camera.ErrAuth) {
			state = StateAuth
		}
		w.update(cam, func(s *Status) {
			if s.State != state {
				s.Since = now
			}
			s.State, s.Error, s.Vehicle = state, err.Error(), false
		})
		w.step(ctx, now, false, cam)
		if state == StateAuth {
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
	return w.cfg.Poll
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

func (w *Watcher) startManual(ctx context.Context, now time.Time) (string, error) {
	cam := w.Camera()
	if cam == nil {
		return "", ErrNoCamera
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
		chosen: map[int]bool{}, votes: map[string]int{}}
	w.visit, w.armed, w.offSince = v, false, time.Time{}
	w.setVisitMove(m.ID)
	w.shoot(ctx, now, cam)
	w.pump(ctx, v)
	return m.ID, nil
}

// shoot takes one still and stores it with the visit's move. A failed still
// is skipped. A failure to store it means the move was closed (a person
// already resolved it), and that ends the visit.
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
	if err != nil {
		log.Printf("gatecam: store still for move %s: %v", v.moveID, err)
		w.endVisit(ctx, now, true)
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
// photos agree, and chooses the next photo.
func (w *Watcher) handleRead(ctx context.Context, r readResult) {
	v := w.visit
	if v == nil || v.moveID != r.moveID {
		v = w.settling[r.moveID]
	}
	if v == nil || v.inFlight == 0 {
		return // a phone photo, a person's re-read, or a read from before a restart
	}
	v.inFlight--
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
	for !v.stopped && v.inFlight < w.cfg.Parallel && v.reads < w.cfg.MaxReads {
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
		v.inFlight++
		v.reads++
	}
	finished := v.stopped || v.reads >= w.cfg.MaxReads || (v.ended && v.pick() < 0)
	if finished && v.inFlight == 0 && !v.settled {
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
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/gatecam/ -race -count=3` (if `-race` is unavailable on this machine because it needs cgo, run without it).
Expected: PASS.

If `TestReadsSpreadEvenlyAfterTheVisitAndStopAtEight` disagrees on the order, recheck `pick` against the comment rather than changing the expected order. With `{0,1}` chosen out of 10 photos, the farthest is index 9 (p10). Then index 5 (p6), then 3 (p4), 7 (p8), 2 (p3) and 4 (p5).

- [ ] **Step 6: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/gatecam
git commit -m "feat(gatecam): vehicle-triggered visits with early stop and even read spread"
```

---

### Task 7: Camera settings manager

**Files:**
- Create: `internal/gatecam/manager.go`
- Test: `internal/gatecam/manager_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/gatecam/manager_test.go`:

```go
package gatecam

import (
	"errors"
	"sync"
	"testing"

	"depot/internal/camera"
	"depot/internal/testkit"
)

type fakeLive struct {
	mu      sync.Mutex
	sources []string
}

func (f *fakeLive) SetSource(src string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sources = append(f.sources, src)
}

func (f *fakeLive) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sources) == 0 {
		return "<none>"
	}
	return f.sources[len(f.sources)-1]
}

func ptr[T any](v T) *T { return &v }

func TestManagerSavesAppliesAndKeepsThePassword(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	live := &fakeLive{}
	var built []camera.Settings
	build := func(s camera.Settings) camera.Camera {
		built = append(built, s)
		return camera.NewReolink(s)
	}
	m := NewManager(st, w, live, build)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Camera() != nil || w.Status().State != StateUnconfigured || live.last() != "" {
		t.Fatalf("before setup: camera %v, status %+v, live %q", w.Camera(), w.Status(), live.last())
	}

	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.50"), User: ptr("admin"), Password: ptr("pw-secret")}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0].Password != "pw-secret" || w.Camera() == nil {
		t.Fatalf("built = %+v", built)
	}
	if got := live.last(); got != "rtsp://admin:pw-secret@192.168.1.50:554/h264Preview_01_sub" {
		t.Fatalf("live source = %s", got)
	}

	// Switching automatic photos on keeps the connection.
	if _, err := m.Save(ctx, Update{Auto: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || !w.Status().Auto {
		t.Fatalf("built %d cameras, auto %v; want the same camera with auto on", len(built), w.Status().Auto)
	}

	// An empty password keeps the saved one.
	if _, err := m.Save(ctx, Update{Host: ptr("192.168.1.51"), Password: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if len(built) != 2 || built[1].Host != "192.168.1.51" || built[1].Password != "pw-secret" {
		t.Fatalf("built = %+v", built)
	}

	// A new manager over the same database loads the same settings.
	m2 := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, build)
	if err := m2.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s := m2.Settings(); s.Host != "192.168.1.51" || s.User != "admin" || s.Password != "pw-secret" || !s.Auto {
		t.Fatalf("loaded settings = %+v", s)
	}
}

func TestManagerRejectsAnythingButAnAddress(t *testing.T) {
	st, _ := testkit.Open(t)
	m := NewManager(st, New(newFakeGate(), DefaultConfig()), &fakeLive{}, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) })
	for _, host := range []string{"http://192.168.1.50", "192.168.1.50/cgi-bin", "admin@192.168.1.50", "192.168.1.50:99999", "cam era", "fe80::1"} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); !errors.Is(err, ErrBadSettings) {
			t.Errorf("Save(host %q) = %v; want ErrBadSettings", host, err)
		}
	}
	for _, host := range []string{"192.168.1.50", "192.168.1.50:8080", "gate-cam.local", ""} {
		if _, err := m.Save(ctx, Update{Host: ptr(host), User: ptr("admin")}); err != nil {
			t.Errorf("Save(host %q) = %v", host, err)
		}
	}
}

func TestManagerUseFixedIgnoresSavedSettings(t *testing.T) {
	st, _ := testkit.Open(t)
	w := New(newFakeGate(), DefaultConfig())
	fake := camera.NewFake(camera.DemoStill())
	m := NewManager(st, w, &fakeLive{}, func(s camera.Settings) camera.Camera { t.Fatal("must not build a camera"); return nil })
	m.UseFixed(fake)
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if w.Camera() != camera.Camera(fake) {
		t.Fatal("the fixed camera is not in use")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/gatecam/ -run Manager`
Expected: compile errors (`undefined: NewManager`, `undefined: Update`).

- [ ] **Step 3: Implement**

Create `internal/gatecam/manager.go`:

```go
package gatecam

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"depot/internal/camera"
	"depot/internal/store"
)

// LiveSink receives the camera's live video address. The live supervisor
// implements it.
type LiveSink interface{ SetSource(src string) }

// Builder connects to the camera that settings describe.
type Builder func(camera.Settings) camera.Camera

// Settings keys in the hub database.
const (
	keyHost     = "camera.host"
	keyUser     = "camera.user"
	keyPassword = "camera.password"
	keyAuto     = "camera.auto"
)

// ErrBadSettings means the entered settings cannot work.
var ErrBadSettings = errors.New("invalid camera settings")

// Update is a change from the Camera page. Nil fields keep their value, and
// an empty password keeps the saved one; the page never sees it.
type Update struct {
	Host     *string `json:"host"`
	User     *string `json:"user"`
	Password *string `json:"password"`
	Auto     *bool   `json:"auto"`
}

// Manager keeps the camera settings and applies them to the watcher and the
// live video.
type Manager struct {
	store   *store.Store
	watcher *Watcher
	live    LiveSink
	build   Builder

	mu      sync.Mutex
	current camera.Settings
	fixed   camera.Camera // replaces the saved settings (depotd hub -fake-camera)
	applied bool
}

// NewManager wires the pieces together. Call Load next.
func NewManager(st *store.Store, w *Watcher, live LiveSink, build Builder) *Manager {
	return &Manager{store: st, watcher: w, live: live, build: build}
}

// UseFixed makes the manager use cam, whatever the saved settings say.
func (m *Manager) UseFixed(cam camera.Camera) {
	m.mu.Lock()
	m.fixed = cam
	m.mu.Unlock()
}

// Load reads the saved settings and applies them.
func (m *Manager) Load(ctx context.Context) error {
	vals, err := m.store.Settings(ctx, "camera.")
	if err != nil {
		return err
	}
	m.apply(camera.Settings{Host: vals[keyHost], User: vals[keyUser], Password: vals[keyPassword], Auto: vals[keyAuto] == "1"})
	return nil
}

// Settings returns the current settings, password included. Callers must
// never send the password to a browser.
func (m *Manager) Settings() camera.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Save validates, stores and applies an update.
func (m *Manager) Save(ctx context.Context, u Update) (camera.Settings, error) {
	s := m.Settings()
	if u.Host != nil {
		s.Host = strings.TrimSpace(*u.Host)
	}
	if u.User != nil {
		s.User = strings.TrimSpace(*u.User)
	}
	if u.Password != nil && *u.Password != "" {
		s.Password = *u.Password
	}
	if u.Auto != nil {
		s.Auto = *u.Auto
	}
	if err := checkHost(s.Host); err != nil {
		return s, err
	}
	if len(s.User) > 64 || len(s.Password) > 128 {
		return s, fmt.Errorf("%w: user name or password too long", ErrBadSettings)
	}
	auto := "0"
	if s.Auto {
		auto = "1"
	}
	err := m.store.SetSettings(ctx, map[string]string{keyHost: s.Host, keyUser: s.User, keyPassword: s.Password, keyAuto: auto}, "admin")
	if err != nil {
		return s, err
	}
	m.apply(s)
	return s, nil
}

// apply hands the settings to the watcher and the live video. Changing only
// the automatic switch keeps the connection.
func (m *Manager) apply(s camera.Settings) {
	m.mu.Lock()
	prev, fixed, applied := m.current, m.fixed, m.applied
	m.current, m.applied = s, true
	m.mu.Unlock()
	m.watcher.SetAuto(s.Auto)
	if applied && prev.Host == s.Host && prev.User == s.User && prev.Password == s.Password {
		return
	}
	var cam camera.Camera
	switch {
	case fixed != nil:
		cam = fixed
	case s.Configured():
		cam = m.build(s)
	}
	m.watcher.SetCamera(cam)
	src := ""
	if cam != nil {
		src = cam.LiveSource()
	}
	m.live.SetSource(src)
}

// Status is the watcher's camera status.
func (m *Manager) Status() Status { return m.watcher.Status() }

// Camera is the connected camera, or nil.
func (m *Manager) Camera() camera.Camera { return m.watcher.Camera() }

// Capture starts a visit by hand.
func (m *Manager) Capture(ctx context.Context) (string, error) { return m.watcher.Capture(ctx) }

// checkHost accepts an IPv4 address or a name, optionally with a port, and
// nothing else: no scheme, path or credentials.
func checkHost(h string) error {
	if h == "" {
		return nil // no camera
	}
	bad := fmt.Errorf("%w: enter only the camera's address, for example 192.168.1.50", ErrBadSettings)
	if len(h) > 253 || strings.ContainsAny(h, "/\\@?#% \t") {
		return bad
	}
	host := h
	if hh, port, err := net.SplitHostPort(h); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad
		}
		host = hh
	}
	if host == "" || strings.Contains(host, ":") {
		return bad
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gatecam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/gatecam
git commit -m "feat(gatecam): camera settings stored on the hub and applied live"
```

---

### Task 8: Live video supervisor (go2rtc) and WebSocket proxy

**Files:**
- Create: `internal/live/live.go`
- Create: `internal/live/proxy.go`
- Test: `internal/live/live_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/live/live_test.go`:

```go
package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"depot/internal/testdir"
)

type fakeProc struct {
	exit   chan error
	killed atomic.Bool
}

func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) Kill() error {
	if p.killed.CompareAndSwap(false, true) {
		p.exit <- errors.New("killed")
	}
	return nil
}

type starts struct {
	mu      sync.Mutex
	procs   []*fakeProc
	configs []string
}

func (s *starts) start(ctx context.Context, binary, config string) (Process, error) {
	data, err := os.ReadFile(config)
	if err != nil {
		return nil, err
	}
	p := &fakeProc{exit: make(chan error, 2)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs = append(s.procs, p)
	s.configs = append(s.configs, string(data))
	return p, nil
}

func (s *starts) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *starts) proc(i int) *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[i]
}

func (s *starts) config(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configs[i]
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testSupervisor(t *testing.T) (*Supervisor, *starts) {
	t.Helper()
	st := &starts{}
	sup := New(Config{Binary: "go2rtc.exe", DataDir: testdir.New(t)})
	sup.start = st.start
	sup.port = func() (int, error) { return 18555, nil }
	sup.exists = func(string) bool { return true }
	sup.minBackoff = 10 * time.Millisecond
	return sup, st
}

func TestSupervisorStartsRestartsAndStopsGo2rtc(t *testing.T) {
	sup, st := testSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()

	waitFor(t, "off", func() bool { return sup.Status().State == StateOff })
	sup.SetSource("rtsp://admin:pw@cam1:554/h264Preview_01_sub")
	waitFor(t, "first start", func() bool { return st.count() == 1 && sup.Status().State == StateRunning })
	if addr, user, pass := sup.target(); addr != "127.0.0.1:18555" || user == "" || pass == "" {
		t.Fatalf("target = %q %q %q", addr, user, pass)
	}

	st.proc(0).exit <- errors.New("crashed")
	waitFor(t, "restart after a crash", func() bool { return st.count() == 2 && sup.Status().State == StateRunning })

	sup.SetSource("rtsp://admin:pw@cam2:554/h264Preview_01_sub")
	waitFor(t, "restart with the new source", func() bool { return st.count() == 3 })
	if !st.proc(1).killed.Load() || !strings.Contains(st.config(2), "cam2") {
		t.Fatalf("old process killed %v, new config %s", st.proc(1).killed.Load(), st.config(2))
	}

	sup.SetSource("")
	waitFor(t, "stop", func() bool { return sup.Status().State == StateOff && st.proc(2).killed.Load() })
	if addr, _, _ := sup.target(); addr != "" {
		t.Fatalf("target after stop = %q", addr)
	}
	cancel()
	<-done
}

func TestSupervisorReportsAMissingBinary(t *testing.T) {
	sup, st := testSupervisor(t)
	sup.exists = func(string) bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sup.Run(ctx)
	sup.SetSource("rtsp://x")
	waitFor(t, "missing", func() bool { return sup.Status().State == StateMissing })
	if !strings.Contains(sup.Status().Reason, "go2rtc.exe") || st.count() != 0 {
		t.Fatalf("status = %+v, starts %d", sup.Status(), st.count())
	}
}

func TestConfigListensLocallyWithAPassword(t *testing.T) {
	path := filepath.Join(testdir.New(t), "go2rtc.yaml")
	if err := writeConfig(path, 18555, "u1", "p1", "rtsp://admin:pw@cam:554/s"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil { // JSON is valid YAML for go2rtc
		t.Fatalf("config is not JSON: %v\n%s", err, data)
	}
	api := cfg["api"]
	if api["listen"] != "127.0.0.1:18555" || api["username"] != "u1" || api["password"] != "p1" || api["local_auth"] != true {
		t.Fatalf("api = %v", api)
	}
	for _, mod := range []string{"rtsp", "webrtc", "srtp"} {
		if cfg[mod]["listen"] != "127.0.0.1:0" {
			t.Errorf("%s listen = %v; want 127.0.0.1:0", mod, cfg[mod]["listen"])
		}
	}
	if cfg["streams"]["gate"] != "rtsp://admin:pw@cam:554/s" {
		t.Fatalf("streams = %v", cfg["streams"])
	}
}

func TestProxyPinsThePathAndAddsTheGo2rtcPassword(t *testing.T) {
	seen := make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Clone(context.Background())
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, addr, "u1", "p1")
	front := httptest.NewServer(sup.Handler())
	defer front.Close()

	req, _ := http.NewRequest("GET", front.URL+"/api/live/ws?src=exec:calc", nil)
	req.Header.Set("Cookie", "depot_access=secret")
	req.Header.Set("Origin", "http://10.0.0.5:8080")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	got := <-seen
	if got.URL.Path != "/api/ws" || got.URL.RawQuery != "src=gate" {
		t.Fatalf("backend got %s?%s; want /api/ws?src=gate", got.URL.Path, got.URL.RawQuery)
	}
	if got.Host != addr || got.Header.Get("Origin") != "http://"+addr {
		t.Fatalf("Host %q Origin %q; want go2rtc's own address", got.Host, got.Header.Get("Origin"))
	}
	if u, p, ok := got.BasicAuth(); !ok || u != "u1" || p != "p1" {
		t.Fatalf("basic auth = %q %q %v", u, p, ok)
	}
	if got.Header.Get("Cookie") != "" {
		t.Fatal("the hub's cookie was forwarded to go2rtc")
	}
}

func TestProxyCarriesTheWebSocketUpgrade(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "no upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		msg := make([]byte, 4)
		if _, err := io.ReadFull(buf, msg); err == nil {
			_, _ = conn.Write(msg)
		}
	}))
	defer backend.Close()
	sup := New(Config{})
	sup.set(Status{State: StateRunning}, strings.TrimPrefix(backend.URL, "http://"), "u1", "p1")
	front := httptest.NewServer(sup.Handler())
	defer front.Close()

	host := strings.TrimPrefix(front.URL, "http://")
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /api/live/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", host)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d; want 101", res.StatusCode)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, %v", echo, err)
	}
}

func TestProxyAnswers503WhenLiveVideoIsNotRunning(t *testing.T) {
	front := httptest.NewServer(New(Config{}).Handler())
	defer front.Close()
	res, err := http.Get(front.URL + "/api/live/ws")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "E5030") {
		t.Fatalf("answer = %d %s", res.StatusCode, body)
	}
}

func TestGo2rtcOutputHidesCameraCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	_, _ = logWriter{}.Write([]byte("WRN [rtsp] url=rtsp://admin:pw-secret@192.168.1.50:554/h264Preview_01_sub i/o timeout\n"))
	if out := buf.String(); strings.Contains(out, "pw-secret") || !strings.Contains(out, "rtsp://***@192.168.1.50") {
		t.Fatalf("log = %q", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/live/`
Expected: compile errors (`undefined: New`, `undefined: Supervisor`, ...).

- [ ] **Step 3: The supervisor**

Create `internal/live/live.go`:

```go
// Package live runs go2rtc, which turns the camera's RTSP stream into video a
// browser can play, and passes that video through the hub's own address.
// go2rtc listens on 127.0.0.1 only and requires a user name and password
// generated at every start. Nothing else on the network, and no web page open
// on the PC, can use it.
package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Live video states.
const (
	StateOff     = "off"     // no camera with live video
	StateMissing = "missing" // go2rtc is not installed
	StateRunning = "running"
	StateFailing = "failing" // go2rtc stopped; restarting
)

// Status is what the Camera page shows about live video.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Process is a running go2rtc.
type Process interface {
	Wait() error
	Kill() error
}

// Config locates go2rtc and its config file.
type Config struct {
	Binary  string // go2rtc executable
	DataDir string // go2rtc.yaml is written here (it holds the camera password)
}

// Supervisor keeps go2rtc running while there is a live source.
type Supervisor struct {
	cfg        Config
	start      func(ctx context.Context, binary, config string) (Process, error)
	port       func() (int, error)
	exists     func(path string) bool
	minBackoff time.Duration

	mu     sync.Mutex
	source string
	addr   string // go2rtc's API address while running
	user   string
	pass   string
	status Status
	wake   chan struct{}
}

// New builds a supervisor. Nothing runs until Run.
func New(cfg Config) *Supervisor {
	return &Supervisor{
		cfg: cfg, start: execStart, port: freePort, exists: fileExists, minBackoff: time.Second,
		status: Status{State: StateOff}, wake: make(chan struct{}, 1),
	}
}

// SetSource sets the RTSP address to show. "" stops go2rtc.
func (s *Supervisor) SetSource(src string) {
	s.mu.Lock()
	changed := s.source != src
	s.source = src
	s.mu.Unlock()
	if changed {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Status reports whether live video is running.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Supervisor) target() (addr, user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr, s.user, s.pass
}

func (s *Supervisor) set(st Status, addr, user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.addr, s.user, s.pass = st, addr, user, pass
}

var errChanged = errors.New("live source changed")

// Run keeps go2rtc running for the current source until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.minBackoff
	for {
		s.mu.Lock()
		src := s.source
		s.mu.Unlock()
		switch {
		case src == "":
			s.set(Status{State: StateOff}, "", "", "")
			if !s.sleep(ctx, 0) {
				return
			}
			continue
		case !s.exists(s.cfg.Binary):
			s.set(Status{State: StateMissing, Reason: "go2rtc not found at " + s.cfg.Binary}, "", "", "")
			if !s.sleep(ctx, 30*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := s.runOnce(ctx, src)
		if ctx.Err() != nil {
			s.set(Status{State: StateOff}, "", "", "")
			return
		}
		if errors.Is(err, errChanged) {
			backoff = s.minBackoff
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = s.minBackoff // it ran fine for a while: restart quickly
		}
		log.Printf("live: go2rtc stopped: %v; restarting in %v", err, backoff)
		s.set(Status{State: StateFailing, Reason: fmt.Sprintf("go2rtc stopped (%v); restarting", err)}, "", "", "")
		if !s.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// runOnce starts go2rtc for src and waits until it exits, the source changes,
// or ctx ends.
func (s *Supervisor) runOnce(ctx context.Context, src string) error {
	port, err := s.port()
	if err != nil {
		return err
	}
	user, pass := randomToken(), randomToken()
	path := filepath.Join(s.cfg.DataDir, "go2rtc.yaml")
	if err := writeConfig(path, port, user, pass, src); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	proc, err := s.start(runCtx, s.cfg.Binary, path)
	if err != nil {
		return err
	}
	s.set(Status{State: StateRunning}, fmt.Sprintf("127.0.0.1:%d", port), user, pass)
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("exited")
		}
		return err
	case <-s.wake:
		_ = proc.Kill()
		<-exited
		return errChanged
	case <-ctx.Done():
		_ = proc.Kill()
		<-exited
		return ctx.Err()
	}
}

// sleep waits for d (forever when d is 0), a source change, or the end of
// ctx. It reports false when ctx ended.
func (s *Supervisor) sleep(ctx context.Context, d time.Duration) bool {
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
		return true
	case <-timeout:
		return true
	}
}

// writeConfig writes go2rtc's config as JSON, which is valid YAML. Every
// listener is bound to 127.0.0.1, and the API needs the password even from
// this PC (local_auth). The file holds the camera password, so only the
// owner may read it.
func writeConfig(path string, port int, user, pass, src string) error {
	cfg := map[string]any{
		"api": map[string]any{
			"listen":     fmt.Sprintf("127.0.0.1:%d", port),
			"username":   user,
			"password":   pass,
			"local_auth": true,
		},
		"rtsp":    map[string]any{"listen": "127.0.0.1:0"},
		"webrtc":  map[string]any{"listen": "127.0.0.1:0"},
		"srtp":    map[string]any{"listen": "127.0.0.1:0"},
		"log":     map[string]any{"level": "warn"},
		"streams": map[string]any{"gate": src},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func execStart(ctx context.Context, binary, config string) (Process, error) {
	cmd := exec.CommandContext(ctx, binary, "-config", config)
	cmd.Stdout, cmd.Stderr = logWriter{}, logWriter{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmdProcess{cmd}, nil
}

type cmdProcess struct{ cmd *exec.Cmd }

func (p cmdProcess) Wait() error { return p.cmd.Wait() }
func (p cmdProcess) Kill() error { return p.cmd.Process.Kill() }

var credsRE = regexp.MustCompile(`rtsp://[^@/\s]+@`)

// logWriter copies go2rtc's output into the hub log, without the camera
// credentials that RTSP addresses carry.
type logWriter struct{}

func (logWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Printf("go2rtc: %s", credsRE.ReplaceAllString(line, "rtsp://***@"))
		}
	}
	return len(b), nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: The proxy**

Create `internal/live/proxy.go`:

```go
package live

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Handler proxies the browser's live-video WebSocket to go2rtc. The path and
// stream name are fixed here, so no other part of go2rtc's API can be reached
// through the hub. Host and Origin are rewritten to go2rtc's own address so
// its WebSocket origin check passes. go2rtc's password is added, and the
// hub's cookie is not passed on.
func (s *Supervisor) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, user, pass := s.target()
		if addr == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"E5030","message":"live video is not running"}}` + "\n"))
			return
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL = &url.URL{Scheme: "http", Host: addr, Path: "/api/ws", RawQuery: "src=gate"}
				pr.Out.Host = addr
				pr.Out.Header.Set("Origin", "http://"+addr)
				pr.Out.Header.Del("Cookie")
				pr.Out.SetBasicAuth(user, pass)
			},
		}
		proxy.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/live/ -count=3`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/live
git commit -m "feat(live): run go2rtc locally with a per-start password and proxy one WebSocket"
```

---
### Task 9: Camera HTTP API

**Files:**
- Create: `internal/httpapi/camera.go`
- Modify: `internal/httpapi/server.go` (Options, routes)
- Modify: `internal/httpapi/handlers.go` (`listMoves` source filter)
- Test: `internal/httpapi/camera_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/httpapi/camera_test.go`:

```go
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"depot/internal/blob"
	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
)

// newCameraServer is newTestServer with camera support: a fake camera behind
// the manager, a running watcher, and live video that is never started.
func newCameraServer(t *testing.T, withCamera bool) *httptest.Server {
	t.Helper()
	dir := testdir.New(t)
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	engine := &recognition.Fake{Reading: recognition.Reading{
		Candidates: []recognition.RawCandidate{{Number: "CSQU3054383", Confidence: 0.9}},
		TypeCode:   "45G1", Color: "blue", Visible: "full",
		Box: &recognition.Box{X0: 0.3, Y0: 0.2, X1: 0.62, Y1: 0.3},
	}}
	broker := NewBroker()
	svc := gate.New(st, bl, engine, gate.Config{}, broker.Publish)
	opts := Options{
		Service: svc, Store: st, Blobs: bl, Version: "test",
		Static: fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	if withCamera {
		cam := camera.NewFake(camera.DemoStill())
		w := gatecam.New(svc, gatecam.DefaultConfig())
		svc.SetReadHook(w.OnRead)
		sup := live.New(live.Config{}) // never run: live video stays off
		mgr := gatecam.NewManager(st, w, sup, func(camera.Settings) camera.Camera { return cam })
		if err := mgr.Load(runCtx); err != nil {
			t.Fatal(err)
		}
		opts.Camera, opts.Live = mgr, sup
		go func() { w.Run(runCtx); close(watcherDone) }()
	} else {
		close(watcherDone)
	}
	if err := svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(opts, broker).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-watcherDone
		svc.Wait()
		st.Close()
	})
	return srv
}

const setupBody = `{"host":"192.168.1.50","user":"admin","password":"pw-secret"}`

func TestCameraEndpointsWithoutCameraSupport(t *testing.T) {
	srv := newCameraServer(t, false)
	c := srv.Client()
	status, body := call(t, c, "GET", srv.URL+"/api/camera", "")
	if status != 200 || !strings.Contains(string(body), `"available":false`) {
		t.Fatalf("GET /api/camera = %d %s", status, body)
	}
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody); status != 503 || errorCode(body) != "E5031" {
		t.Fatalf("PUT = %d %s; want 503 E5031", status, body)
	}
	if status, body := call(t, c, "GET", srv.URL+"/api/live/ws", ""); status != 503 || errorCode(body) != "E5030" {
		t.Fatalf("live = %d %s; want 503 E5030", status, body)
	}
}

func TestCameraSettingsNeverReturnThePassword(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	if status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	for _, b := range [][]byte{body, func() []byte { _, b := call(t, c, "GET", srv.URL+"/api/camera", ""); return b }()} {
		if strings.Contains(string(b), "pw-secret") {
			t.Fatalf("camera answer leaks the password: %s", b)
		}
		var v struct {
			Available bool `json:"available"`
			Settings  struct {
				Host        string `json:"host"`
				PasswordSet bool   `json:"password_set"`
			} `json:"settings"`
		}
		if err := json.Unmarshal(b, &v); err != nil || !v.Available || v.Settings.Host != "192.168.1.50" || !v.Settings.PasswordSet {
			t.Fatalf("camera view = %s", b)
		}
	}
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", `{"host":"http://192.168.1.50/","user":"admin"}`); status != 400 || errorCode(body) != "E4007" {
		t.Fatalf("PUT with a URL = %d %s; want 400 E4007", status, body)
	}
}

func TestCameraMustBeSetUpBeforeUse(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	for _, path := range []string{"/api/camera/read", "/api/camera/capture", "/api/camera/test"} {
		if status, body := call(t, c, "POST", srv.URL+path, ""); status != 409 || errorCode(body) != "E4091" {
			t.Errorf("POST %s = %d %s; want 409 E4091", path, status, body)
		}
	}
}

func TestReadThisFrameMeasuresTheLetters(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	if status, body := call(t, c, "PUT", srv.URL+"/api/camera", setupBody); status != 200 {
		t.Fatalf("PUT = %d %s", status, body)
	}
	status, body := call(t, c, "POST", srv.URL+"/api/camera/read", "")
	if status != 200 {
		t.Fatalf("read = %d %s", status, body)
	}
	var r struct {
		Still    string `json:"still"`
		Width    int    `json:"width"`
		LetterPX int    `json:"letter_px"`
		Verdict  string `json:"verdict"`
		Result   struct {
			Frame struct {
				Candidates []struct {
					Number string `json:"number"`
				} `json:"candidates"`
			} `json:"frame"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Width != 1280 || r.LetterPX != 72 || r.Verdict != gatecam.VerdictGood || len(r.Result.Frame.Candidates) == 0 {
		t.Fatalf("read = %s", body)
	}
	res, err := c.Get(srv.URL + "/api/blobs/" + r.Still + "?w=320")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	img, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if res.StatusCode != 200 || err != nil || img.Width != 320 {
		t.Fatalf("still preview = %d, %v, width %d", res.StatusCode, err, img.Width)
	}
}

func TestCameraTestAndStill(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	status, body := call(t, c, "POST", srv.URL+"/api/camera/test", "")
	var rep camera.Report
	if status != 200 || json.Unmarshal(body, &rep) != nil || !rep.OK() {
		t.Fatalf("test = %d %s", status, body)
	}
	res, err := c.Get(srv.URL + "/api/camera/still?w=320")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("still = %d, Cache-Control %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if img, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil || img.Width != 320 {
		t.Fatalf("still = %v, width %d", err, img.Width)
	}
}

func TestTakePhotosNowCreatesACameraMove(t *testing.T) {
	srv := newCameraServer(t, true)
	c := srv.Client()
	call(t, c, "PUT", srv.URL+"/api/camera", setupBody)
	status, body := call(t, c, "POST", srv.URL+"/api/camera/capture", "")
	var r struct {
		MoveID string `json:"move_id"`
	}
	if status != 202 || json.Unmarshal(body, &r) != nil || r.MoveID == "" {
		t.Fatalf("capture = %d %s", status, body)
	}
	_, body = call(t, c, "GET", srv.URL+"/api/moves?source=camera", "")
	if !strings.Contains(string(body), r.MoveID) {
		t.Fatalf("camera moves = %s; want %s", body, r.MoveID)
	}
	_, body = call(t, c, "GET", srv.URL+"/api/moves?source=phone", "")
	if strings.Contains(string(body), r.MoveID) {
		t.Fatalf("phone moves include the camera move: %s", body)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/`
Expected: compile errors (`unknown field Camera in struct literal of type Options`).

- [ ] **Step 3: Options and routes**

In `internal/httpapi/server.go`, add the imports `"depot/internal/gatecam"` and `"depot/internal/live"`, then extend `Options`:

```go
// Options wires the server to the rest of the hub.
type Options struct {
	Service    *gate.Service
	Store      *store.Store
	Blobs      *blob.Store
	Static     fs.FS  // built web app; nil serves a "not built" notice
	AccessCode string // empty: no code required
	Version    string
	Camera     *gatecam.Manager // nil: no camera support (camera endpoints answer 503)
	Live       *live.Supervisor // nil: no live video
}
```

In `routes()`, add these before the catch-all `/api/` route:

```go
	s.mux.HandleFunc("GET /api/camera", s.getCamera)
	s.mux.HandleFunc("PUT /api/camera", s.putCamera)
	s.mux.HandleFunc("POST /api/camera/test", s.testCamera)
	s.mux.HandleFunc("POST /api/camera/read", s.readCamera)
	s.mux.HandleFunc("POST /api/camera/capture", s.captureCamera)
	s.mux.HandleFunc("GET /api/camera/still", s.cameraStill)
	s.mux.HandleFunc("GET /api/live/ws", s.liveWS)
```

In `internal/httpapi/handlers.go` `listMoves`, pass the source filter:

```go
	moves, err := s.opts.Store.ListMoves(r.Context(), store.ListFilter{
		Status: q.Get("status"), Source: q.Get("source"), Limit: limit, Before: q.Get("before"),
	})
```

- [ ] **Step 4: The handlers**

Create `internal/httpapi/camera.go`:

```go
package httpapi

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"net/http"
	"time"

	"depot/internal/camera"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/thumb"
)

// cameraView is everything the Camera page shows. It never includes the
// camera password.
type cameraView struct {
	Available bool           `json:"available"`
	Settings  settingsView   `json:"settings"`
	Status    gatecam.Status `json:"status"`
	Live      live.Status    `json:"live"`
}

type settingsView struct {
	Host        string `json:"host"`
	User        string `json:"user"`
	PasswordSet bool   `json:"password_set"`
	Auto        bool   `json:"auto"`
}

func (s *Server) cameraView() cameraView {
	v := cameraView{Status: gatecam.Status{State: gatecam.StateUnconfigured}, Live: live.Status{State: live.StateOff}}
	if s.opts.Camera == nil {
		return v
	}
	set := s.opts.Camera.Settings()
	v.Available = true
	v.Settings = settingsView{Host: set.Host, User: set.User, PasswordSet: set.Password != "", Auto: set.Auto}
	v.Status = s.opts.Camera.Status()
	if s.opts.Live != nil {
		v.Live = s.opts.Live.Status()
	}
	return v
}

func (s *Server) getCamera(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cameraView())
}

func (s *Server) putCamera(w http.ResponseWriter, r *http.Request) {
	if !s.needCamera(w) {
		return
	}
	var u gatecam.Update
	if !decodeJSON(w, r, &u) {
		return
	}
	if _, err := s.opts.Camera.Save(r.Context(), u); err != nil {
		if errors.Is(err, gatecam.ErrBadSettings) {
			writeError(w, http.StatusBadRequest, "E4007", err.Error())
			return
		}
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cameraView())
}

// needCamera answers 503 when the hub runs without camera support.
func (s *Server) needCamera(w http.ResponseWriter) bool {
	if s.opts.Camera == nil {
		writeError(w, http.StatusServiceUnavailable, "E5031", "camera support is not enabled on this hub")
		return false
	}
	return true
}

// currentCamera returns the connected camera, or answers 409 when none is
// set up yet.
func (s *Server) currentCamera(w http.ResponseWriter) (camera.Camera, bool) {
	if !s.needCamera(w) {
		return nil, false
	}
	cam := s.opts.Camera.Camera()
	if cam == nil {
		writeError(w, http.StatusConflict, "E4091", "no camera is set up yet")
		return nil, false
	}
	return cam, true
}

func (s *Server) testCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, cam.Test(ctx))
}

// cameraRead is "read this frame": the still, what the engine saw, and how
// tall the number's letters are in the picture.
type cameraRead struct {
	Still    string              `json:"still"`
	Width    int                 `json:"width"`
	Height   int                 `json:"height"`
	Result   gate.TestReadResult `json:"result"`
	LetterPX int                 `json:"letter_px"`
	Verdict  string              `json:"verdict"`
}

func (s *Server) readCamera(w http.ResponseWriter, r *http.Request) {
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	data, err := cam.Snapshot(ctx)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5020", "camera: "+err.Error())
		return
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5021", "the camera's still is not a JPEG")
		return
	}
	digest, err := s.opts.Blobs.Put(data)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	res, err := s.opts.Service.ReadStill(r.Context(), data)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	px := gatecam.LetterHeight(res.Attempt.Reading.Box, cfg.Width, cfg.Height)
	writeJSON(w, http.StatusOK, cameraRead{
		Still: digest, Width: cfg.Width, Height: cfg.Height, Result: res, LetterPX: px, Verdict: gatecam.Verdict(px),
	})
}

func (s *Server) captureCamera(w http.ResponseWriter, r *http.Request) {
	if !s.needCamera(w) {
		return
	}
	id, err := s.opts.Camera.Capture(r.Context())
	switch {
	case errors.Is(err, gatecam.ErrNoCamera):
		writeError(w, http.StatusConflict, "E4091", "no camera is set up yet")
	case errors.Is(err, gatecam.ErrBusy):
		writeError(w, http.StatusConflict, "E4092", "the camera is already taking photos of a vehicle")
	case err != nil:
		writeServiceError(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"move_id": id})
	}
}

// cameraStill is a fresh still for the live view's fallback when video
// cannot play.
func (s *Server) cameraStill(w http.ResponseWriter, r *http.Request) {
	width, ok := previewWidth(w, r)
	if !ok {
		return
	}
	cam, ok := s.currentCamera(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	data, err := cam.Snapshot(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "E5020", "camera: "+err.Error())
		return
	}
	if width > 0 {
		if small, err := thumb.Scale(data, width); err == nil {
			data = small
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *Server) liveWS(w http.ResponseWriter, r *http.Request) {
	if s.opts.Live == nil {
		writeError(w, http.StatusServiceUnavailable, "E5030", "live video is not running")
		return
	}
	s.opts.Live.Handler().ServeHTTP(w, r)
}
```

The capture endpoint returns 409 E4091 when no camera is set up. `TestCameraMustBeSetUpBeforeUse` expects that for capture too, and `Manager.Capture` returns `gatecam.ErrNoCamera` in that case through the watcher.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/httpapi/ -count=2` and then `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . ; go vet ./...
git add internal/httpapi
git commit -m "feat(httpapi): camera setup, test, read-this-frame, capture, still and live endpoints"
```

---

### Task 10: Wire the camera into `depotd hub`

**Files:**
- Modify: `cmd/depotd/main.go`
- Modify: `.gitignore` (ignore `bin/go2rtc*`, if `bin/` is not already ignored)

- [ ] **Step 1: Add the flags and wiring**

In `cmd/depotd/main.go`:

1. Add the imports `"runtime"`, `"sync"`, `"depot/internal/camera"`, `"depot/internal/gatecam"` and `"depot/internal/live"`.

2. Below the `Version` variable, add a compile-time check that the gate service satisfies the watcher's interface:

```go
// The camera watcher drives gate-ins through the gate service.
var _ gatecam.Gate = (*gate.Service)(nil)
```

3. In `runHub`, add two flags after `accessCode`:

```go
	fakeCamera := fs.Bool("fake-camera", false, "use a simulated camera (try the Camera page without one)")
	go2rtcPath := fs.String("go2rtc", defaultGo2rtc(), "go2rtc executable for live video")
```

4. Replace the block from `broker := httpapi.NewBroker()` through the `if err := svc.Start(ctx)` check with:

```go
	broker := httpapi.NewBroker()
	svc := gate.New(st, bl, rec, gate.Config{AutoAcceptEnabled: *autoAccept, AutoAcceptThreshold: *threshold}, broker.Publish)

	watcher := gatecam.New(svc, gatecam.DefaultConfig())
	svc.SetReadHook(watcher.OnRead)
	liveVideo := live.New(live.Config{Binary: *go2rtcPath, DataDir: data.dir})
	cams := gatecam.NewManager(st, watcher, liveVideo, func(s camera.Settings) camera.Camera { return camera.NewReolink(s) })
	if *fakeCamera {
		cams.UseFixed(camera.NewDemoFake())
	}
	if err := cams.Load(ctx); err != nil {
		return err
	}
	if err := svc.Start(ctx); err != nil {
		return err
	}
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); watcher.Run(ctx) }()
	go func() { defer background.Done(); liveVideo.Run(ctx) }() // stops go2rtc on exit
```

5. Pass the camera and live video to the server:

```go
		Handler: httpapi.New(httpapi.Options{
			Service: svc, Store: st, Blobs: bl, Static: static, AccessCode: *accessCode, Version: Version,
			Camera: cams, Live: liveVideo,
		}, broker).Handler(),
```

6. After the existing `log.Printf("depotd %s · engine ...")` line, log the camera:

```go
	switch s := cams.Settings(); {
	case *fakeCamera:
		log.Print("camera: simulated (-fake-camera)")
	case s.Configured():
		log.Printf("camera: %s (automatic photos %v)", s.Host, s.Auto)
	default:
		log.Print("camera: not set up; open the Camera page")
	}
```

7. In the shutdown sequence, wait for the watcher and go2rtc before the workers:

```go
	_ = server.Shutdown(shutdownCtx)
	background.Wait()
	svc.Wait()
	return nil
```

8. Add the helper at the bottom of the file:

```go
// defaultGo2rtc is go2rtc next to depotd itself (bin\go2rtc.exe).
func defaultGo2rtc() string {
	name := "go2rtc"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe, err := os.Executable()
	if err != nil {
		return name
	}
	return filepath.Join(filepath.Dir(exe), name)
}
```

9. Run `git check-ignore bin/go2rtc.exe`. If it prints nothing, append `bin/go2rtc*` to `.gitignore`.

- [ ] **Step 2: Build and run the tests**

Run:
```bash
go vet ./...
go test ./...
go build -o "$TEMP/depotd-check.exe" ./cmd/depotd
```
Expected: PASS and a binary in `%TEMP%`. Never build to `bin\depotd.exe`, which the owner is running.

- [ ] **Step 3: Smoke test with the simulated camera**

Start a separate hub on another port and data folder:

```powershell
$data = Join-Path $env:TEMP "depot-cam-smoke"
$p = Start-Process -FilePath "$env:TEMP\depotd-check.exe" -ArgumentList "hub","-engine","fake","-fake-camera","-addr","127.0.0.1:18080","-data",$data -PassThru -NoNewWindow
Start-Sleep -Seconds 2
curl.exe -s http://127.0.0.1:18080/api/camera
curl.exe -s -X POST -H "X-Depot-Client: web" http://127.0.0.1:18080/api/camera/capture
Start-Sleep -Seconds 4
curl.exe -s "http://127.0.0.1:18080/api/moves?source=camera"
Stop-Process -Id $p.Id
```

Expected:
- `/api/camera` shows `"available":true`, `"state":"online"` and `"model":"Fake camera"`.
- The capture call returns a `move_id`.
- `/api/moves?source=camera` lists that move with several photos and the number `CSQU3054383`.
- The hub log says `camera: simulated (-fake-camera)`.

Only stop the process you started, by its `Id`.

- [ ] **Step 4: Commit**

```bash
gofmt -l . ; go vet ./...
git add cmd/depotd .gitignore
git commit -m "feat(depotd): camera watcher, settings and live video in the hub (-fake-camera, -go2rtc)"
```

---
### Task 11: Web foundation: API client, strings, live video player

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/i18n.ts`
- Modify: `web/src/styles.css`
- Create: `web/src/camera/mse.ts`
- Create: `web/src/camera/LiveVideo.tsx`

The web app has no unit-test runner. `npm --prefix web run typecheck` and `build` are the checks, and Task 12 adds a manual check against `-fake-camera`.

- [ ] **Step 1: API types and calls**

In `web/src/api.ts`:

1. Extend `Status`:

```ts
export type Status = "capturing" | "reading" | "needs_review" | "accepted" | "refused" | "no_container";
```

2. Add `deferred: boolean;` at the end of `Photo`. Add `source: "phone" | "camera";` to `Move`, after `direction`.

3. Replace `blobUrl` with a version that can ask for a preview:

```ts
// blobUrl links a stored photo. With a width the hub sends a small JPEG
// preview instead, so phones do not download 4K camera stills.
export type PreviewWidth = 160 | 320 | 640 | 1280;
export const blobUrl = (digest: string, w?: PreviewWidth) => (w ? `/api/blobs/${digest}?w=${w}` : `/api/blobs/${digest}`);
```

4. Add the camera types after `Health`:

```ts
export interface CameraInfo {
  model: string;
  firmware: string;
  width: number;
  height: number;
  rtsp: boolean;
  detection: string;
}

export type CameraState = "unconfigured" | "connecting" | "online" | "offline" | "auth";

export interface CameraStatus {
  state: CameraState;
  since: string;
  error?: string;
  vehicle: boolean;
  detection: string;
  info: CameraInfo;
  auto: boolean;
  visit_move?: string;
}

export interface LiveStatus {
  state: "off" | "missing" | "running" | "failing";
  reason?: string;
}

export interface CameraView {
  available: boolean;
  settings: { host: string; user: string; password_set: boolean; auto: boolean };
  status: CameraStatus;
  live: LiveStatus;
}

export type CameraStepName = "reach" | "login" | "info" | "ports" | "detect" | "snapshot";

export interface CameraReport {
  steps: { name: CameraStepName; ok: boolean; detail?: string }[] | null;
  info: CameraInfo;
  raw: string;
}

export interface CameraRead {
  still: string;
  width: number;
  height: number;
  result: TestReadResult;
  letter_px: number;
  verdict: "" | "good" | "borderline" | "too_small";
}

export interface CameraUpdate {
  host?: string;
  user?: string;
  password?: string;
  auto?: boolean;
}
```

5. Add these calls to the `api` object:

```ts
  listMovesBy: (f: { status?: string; source?: string; limit?: number }) => {
    const q = new URLSearchParams();
    if (f.status) q.set("status", f.status);
    if (f.source) q.set("source", f.source);
    q.set("limit", String(f.limit ?? 100));
    return call<{ moves: Move[] }>(`/api/moves?${q.toString()}`);
  },
  camera: () => call<CameraView>("/api/camera"),
  saveCamera: (u: CameraUpdate) =>
    call<CameraView>("/api/camera", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(u),
    }),
  testCamera: () => callWithTimeout<CameraReport>("/api/camera/test", { method: "POST" }, 45_000),
  readCamera: () => callWithTimeout<CameraRead>("/api/camera/read", { method: "POST" }, UPLOAD_TIMEOUT_MS),
  captureCamera: () => call<{ move_id: string }>("/api/camera/capture", { method: "POST" }),
```

- [ ] **Step 2: Strings**

In `web/src/i18n.ts`, add these keys at the end of the `tr` object (just before its closing `};`):

```ts
  navCamera: "Kamera",
  homeCameraHint: "Sabit kamerayı bağla, canlı izle ve ayarla",
  cameraTag: "Kamera",
  status_no_container: "Konteyner yok",
  camNotAvailable: "Bu sunucuda kamera desteği kapalı",
  camSetup: "Kamera bağlantısı",
  camHost: "Kamera adresi (IP)",
  camUser: "Kullanıcı adı",
  camPassword: "Şifre",
  camPasswordSaved: "Şifre kayıtlı; değiştirmek için yazın",
  camSaveTest: "Kaydet ve test et",
  camTest: "Bağlantıyı test et",
  camStep_reach: "Kameraya ulaşma",
  camStep_login: "Giriş",
  camStep_info: "Cihaz bilgisi",
  camStep_ports: "Canlı video portu (RTSP)",
  camStep_detect: "Araç algılama",
  camStep_snapshot: "4K fotoğraf",
  camRaw: "Kameranın ham cevabı",
  camState_unconfigured: "Kamera ayarlanmadı",
  camState_connecting: "Bağlanıyor…",
  camState_online: "Bağlı",
  camState_offline: "Bağlantı yok",
  camState_auth: "Kullanıcı adı veya şifre yanlış",
  camSince: "şu saatten beri:",
  camLive: "Canlı görüntü",
  camVehicle: "Araç görüldü",
  camNoVehicle: "Araç yok",
  camMotionOnly: "bu kamera sadece hareket algılıyor",
  camAuto: "Otomatik fotoğraf (araç gelince)",
  camReadFrame: "Bu kareyi oku",
  camCaptureNow: "Şimdi fotoğraf çek",
  camLetter: "Harf yüksekliği",
  camVerdict_good: "iyi",
  camVerdict_borderline: "sınırda; kamerayı biraz yaklaştırın",
  camVerdict_too_small: "çok küçük; kamerayı yaklaştırın",
  camRecent: "Son kamera geçişleri",
  camPhotos: "fotoğraf",
  camLiveConnecting: "Canlı video bağlanıyor…",
  camLivePlaying: "Canlı",
  camLiveStill: "Canlı video yok; her 2 saniyede bir fotoğraf gösteriliyor",
  camLiveRetry: "Videoyu tekrar dene",
  otherPhotos: "Okunmayan fotoğraflar",
  readTag: "okundu",
```

And at the end of the `en` object:

```ts
  navCamera: "Camera",
  homeCameraHint: "Connect, watch and aim the fixed camera",
  cameraTag: "Camera",
  status_no_container: "No container",
  camNotAvailable: "Camera support is off on this hub",
  camSetup: "Camera connection",
  camHost: "Camera address (IP)",
  camUser: "User name",
  camPassword: "Password",
  camPasswordSaved: "Password saved; type to change it",
  camSaveTest: "Save and test",
  camTest: "Test connection",
  camStep_reach: "Reach the camera",
  camStep_login: "Log in",
  camStep_info: "Device info",
  camStep_ports: "Live video port (RTSP)",
  camStep_detect: "Vehicle detection",
  camStep_snapshot: "4K still",
  camRaw: "Camera's raw answer",
  camState_unconfigured: "Camera not set up",
  camState_connecting: "Connecting…",
  camState_online: "Connected",
  camState_offline: "Offline",
  camState_auth: "Wrong user name or password",
  camSince: "since",
  camLive: "Live view",
  camVehicle: "Vehicle seen",
  camNoVehicle: "No vehicle",
  camMotionOnly: "this camera only detects motion",
  camAuto: "Automatic photos (when a vehicle arrives)",
  camReadFrame: "Read this frame",
  camCaptureNow: "Take photos now",
  camLetter: "Letter height",
  camVerdict_good: "good",
  camVerdict_borderline: "borderline; move the camera a little closer",
  camVerdict_too_small: "too small; move the camera closer",
  camRecent: "Recent camera visits",
  camPhotos: "photos",
  camLiveConnecting: "Connecting live video…",
  camLivePlaying: "Live",
  camLiveStill: "No live video; showing a photo every 2 seconds",
  camLiveRetry: "Try video again",
  otherPhotos: "Unread photos",
  readTag: "read",
```

- [ ] **Step 3: Styles**

Append to `web/src/styles.css`:

```css
.live-video { width: 100%; max-height: 60vh; background: #000; border-radius: 8px; object-fit: contain; display: block; }
.light { display: inline-block; width: 12px; height: 12px; border-radius: 50%; background: var(--border); margin-right: 6px; vertical-align: middle; }
.light.on { background: var(--ok); }
.light.bad { background: var(--bad); }
.steps { list-style: none; padding: 0; display: grid; gap: 4px; }
.steps li.ok::before { content: "✔ "; color: var(--ok); }
.steps li.bad::before { content: "✖ "; color: var(--bad); }
.chip-no_container { background: var(--border); color: var(--muted); }
details > summary { cursor: pointer; margin: 12px 0; }
```

- [ ] **Step 4: The MSE player**

Create `web/src/camera/mse.ts`:

```ts
// Plays go2rtc's live video with Media Source Extensions over a WebSocket.
// The browser lists the video codecs it can decode, go2rtc answers with the
// MIME type it will send, then it streams fragmented MP4, which is appended
// to a SourceBuffer. Only video codecs are offered, so go2rtc sends no audio.

const VIDEO_CODECS = ["avc1.640029", "avc1.64002A", "avc1.640033", "hvc1.1.6.L153.B0"];
const KEEP_SECONDS = 5; // buffer kept behind the live edge
const MAX_LAG = 1; // seconds behind the live edge before jumping forward

export type LiveState = "connecting" | "playing" | "error";

export interface LivePlayer {
  stop(): void;
}

export function liveUrl(): string {
  const scheme = window.location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${window.location.host}/api/live/ws`;
}

// mediaSource prefers ManagedMediaSource, the only kind Safari on iPhone
// supports (iOS 17.1+).
function mediaSource(): { MS: typeof MediaSource; managed: boolean } | null {
  const w = window as unknown as { ManagedMediaSource?: typeof MediaSource };
  if (w.ManagedMediaSource) return { MS: w.ManagedMediaSource, managed: true };
  if (typeof window.MediaSource !== "undefined") return { MS: window.MediaSource, managed: false };
  return null;
}

export function playLive(
  video: HTMLVideoElement,
  url: string,
  onState: (s: LiveState, detail?: string) => void,
): LivePlayer {
  const found = mediaSource();
  if (!found) {
    onState("error", "this browser cannot play live video");
    return { stop() {} };
  }
  const { MS, managed } = found;
  const ms = new MS();
  let objectUrl = "";
  if (managed) {
    video.disableRemotePlayback = true;
    video.srcObject = ms;
  } else {
    objectUrl = URL.createObjectURL(ms);
    video.src = objectUrl;
  }
  let ws: WebSocket | null = null;
  let sb: SourceBuffer | null = null;
  const queue: Uint8Array[] = [];
  let stopped = false;
  let playing = false;

  const fail = (detail: string) => {
    if (!stopped) onState("error", detail);
  };

  // pump appends queued data, trims the old buffer and keeps playback at the
  // live edge. It runs on every message and after every SourceBuffer update.
  const pump = () => {
    if (!sb || sb.updating || stopped) return;
    if (queue.length > 0) {
      const size = queue.reduce((n, b) => n + b.byteLength, 0);
      const data = new Uint8Array(size);
      let offset = 0;
      for (const b of queue) {
        data.set(b, offset);
        offset += b.byteLength;
      }
      queue.length = 0;
      try {
        sb.appendBuffer(data);
      } catch (e) {
        fail(String(e));
      }
      return;
    }
    const ranges = sb.buffered;
    if (ranges.length === 0) return;
    const start = ranges.start(0);
    const end = ranges.end(ranges.length - 1);
    if (end - start > KEEP_SECONDS + 1) {
      try {
        sb.remove(start, end - KEEP_SECONDS);
      } catch {
        // removal fails while the media source is closing; nothing to do
      }
      return;
    }
    if (end - video.currentTime > MAX_LAG) video.currentTime = Math.max(start, end - 0.3);
    if (video.paused) void video.play().catch(() => undefined);
    if (!playing) {
      playing = true;
      onState("playing");
    }
  };

  ms.addEventListener(
    "sourceopen",
    () => {
      if (stopped) return;
      onState("connecting");
      const socket = new WebSocket(url);
      ws = socket;
      socket.binaryType = "arraybuffer";
      socket.onopen = () => {
        const codecs = VIDEO_CODECS.filter((c) => MS.isTypeSupported(`video/mp4; codecs="${c}"`)).join(",");
        socket.send(JSON.stringify({ type: "mse", value: codecs }));
      };
      socket.onmessage = (ev: MessageEvent) => {
        if (typeof ev.data === "string") {
          let msg: { type?: string; value?: string };
          try {
            msg = JSON.parse(ev.data) as { type?: string; value?: string };
          } catch {
            return;
          }
          if (msg.type === "mse" && msg.value && !sb) {
            try {
              sb = ms.addSourceBuffer(msg.value);
              sb.mode = "segments";
              sb.addEventListener("updateend", pump);
            } catch {
              fail(`this browser cannot play ${msg.value}`);
            }
          } else if (msg.type === "error") {
            fail(msg.value ?? "live video error");
          }
          return;
        }
        queue.push(new Uint8Array(ev.data as ArrayBuffer));
        pump();
      };
      socket.onerror = () => fail("connection failed");
      socket.onclose = () => fail("connection closed");
    },
    { once: true },
  );

  return {
    stop() {
      stopped = true;
      ws?.close();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
      video.removeAttribute("src");
      video.srcObject = null;
      video.load();
    },
  };
}
```

- [ ] **Step 5: The live video component**

Create `web/src/camera/LiveVideo.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import { liveUrl, playLive, type LiveState } from "./mse";

// LiveVideo shows the camera's live video. When video cannot play (go2rtc not
// installed, the stream failing, an old browser), it shows a still refreshed
// every 2 seconds instead.
export function LiveVideo({ videoAvailable }: { videoAvailable: boolean }) {
  const { t } = useT();
  const videoRef = useRef<HTMLVideoElement>(null);
  const failures = useRef(0); // failures in a row; reset once video plays
  const [mode, setMode] = useState<"video" | "still">(videoAvailable ? "video" : "still");
  const [state, setState] = useState<LiveState>("connecting");
  const [detail, setDetail] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    const video = videoRef.current;
    if (mode !== "video" || !video) return;
    let failed = false;
    let timer = 0;
    const player = playLive(video, liveUrl(), (s, d) => {
      setState(s);
      if (s === "playing") failures.current = 0;
      if (s !== "error" || failed) return;
      failed = true;
      setDetail(d ?? "");
      failures.current += 1;
      // Reconnect twice, then fall back to stills.
      timer = window.setTimeout(() => {
        if (failures.current <= 2) setAttempt((a) => a + 1);
        else setMode("still");
      }, 2000);
    });
    return () => {
      window.clearTimeout(timer);
      player.stop();
    };
  }, [mode, attempt]);

  useEffect(() => {
    if (mode !== "still") return;
    const timer = window.setInterval(() => setTick((n) => n + 1), 2000);
    return () => window.clearInterval(timer);
  }, [mode]);

  let caption: string;
  if (mode === "still") caption = t("camLiveStill");
  else if (state === "playing") caption = t("camLivePlaying");
  else if (state === "connecting") caption = t("camLiveConnecting");
  else caption = `${t("error")}: ${detail}`;

  return (
    <div className="live">
      {mode === "video" ? (
        <video ref={videoRef} className="live-video" muted playsInline autoPlay />
      ) : (
        <img className="live-video" src={`/api/camera/still?w=1280&n=${tick}`} alt="" />
      )}
      <p className="small muted">
        {caption}{" "}
        {mode === "still" && videoAvailable && (
          <button
            type="button"
            onClick={() => {
              failures.current = 0;
              setAttempt((a) => a + 1);
              setMode("video");
            }}
          >
            {t("camLiveRetry")}
          </button>
        )}
      </p>
    </div>
  );
}
```

- [ ] **Step 6: Typecheck and build**

Run: `npm --prefix web run typecheck` and then `npm --prefix web run build`
Expected: no errors. Existing pages still compile because `blobUrl(digest)` keeps working.

If `video.srcObject = ms` fails to typecheck with this TypeScript version's DOM types, use `video.srcObject = ms as unknown as MediaStream;` and keep a comment explaining that iPhone Safari needs `ManagedMediaSource` as `srcObject`.

- [ ] **Step 7: Commit**

```bash
git add web/src
git commit -m "feat(web): camera API client, strings and MSE live video player"
```

---

### Task 12: Camera page, navigation, queue and truck screen

**Files:**
- Create: `web/src/pages/Camera.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/pages/Home.tsx`
- Modify: `web/src/pages/Queue.tsx`
- Modify: `web/src/pages/MoveDetail.tsx`

- [ ] **Step 1: The Camera page**

Create `web/src/pages/Camera.tsx`:

```tsx
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, ApiError, blobUrl, type CameraRead, type CameraReport, type CameraView, type Move } from "../api";
import { LiveVideo } from "../camera/LiveVideo";
import { CandidateList, PhotoWithBox, StatusChip } from "../components";
import { useT, type Key } from "../i18n";
import { useMoveEvents } from "../live";

export function Camera() {
  const { t } = useT();
  const [view, setView] = useState<CameraView | null>(null);
  const [form, setForm] = useState<{ host: string; user: string; password: string } | null>(null);
  const [report, setReport] = useState<CameraReport | null>(null);
  const [read, setRead] = useState<CameraRead | null>(null);
  const [visits, setVisits] = useState<Move[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const v = await api.camera();
      setView(v);
      setForm((f) => f ?? { host: v.settings.host, user: v.settings.user, password: "" });
    } catch (e) {
      setError(String(e));
    }
  }, []);
  const loadVisits = useCallback(async () => {
    try {
      setVisits((await api.listMovesBy({ source: "camera", limit: 20 })).moves);
    } catch {
      // the status poll reports connection problems
    }
  }, []);

  useEffect(() => {
    void load();
    void loadVisits();
    const timer = window.setInterval(() => void load(), 1000); // the "vehicle seen" light
    return () => window.clearInterval(timer);
  }, [load, loadVisits]);
  useMoveEvents(
    () => void loadVisits(),
    () => void loadVisits(),
  );

  async function run(name: string, fn: () => Promise<void>) {
    setBusy(name);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy("");
    }
  }

  const save = (e: FormEvent) => {
    e.preventDefault();
    if (!form) return;
    void run("save", async () => {
      setView(await api.saveCamera({ host: form.host, user: form.user, password: form.password }));
      setForm({ ...form, password: "" });
      setReport(await api.testCamera());
    });
  };

  if (!view) return <p>{error || t("loading")}</p>;
  if (!view.available) return <p>{t("camNotAvailable")}</p>;
  const s = view.status;
  const configured = s.state !== "unconfigured";
  const light = s.state === "online" ? "on" : configured ? "bad" : "";

  return (
    <section>
      <div className="card">
        <h3>{t("camSetup")}</h3>
        <p>
          <span className={`light ${light}`} />
          {t(`camState_${s.state}` as Key)}
          {configured && s.state !== "online" && s.since && ` · ${t("camSince")} ${new Date(s.since).toLocaleTimeString()}`}
          {s.info.model && ` · ${s.info.model} ${s.info.firmware}`}
          {s.info.width > 0 && ` · ${s.info.width}×${s.info.height}`}
        </p>
        {s.error && <p className="small muted">{s.error}</p>}
        {form && (
          <form onSubmit={save}>
            <label>
              {t("camHost")}
              <input
                value={form.host}
                onChange={(e) => setForm({ ...form, host: e.target.value })}
                placeholder="192.168.1.50"
                autoComplete="off"
              />
            </label>
            <label>
              {t("camUser")}
              <input value={form.user} onChange={(e) => setForm({ ...form, user: e.target.value })} autoComplete="off" />
            </label>
            <label>
              {t("camPassword")}
              <input
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                placeholder={view.settings.password_set ? t("camPasswordSaved") : ""}
                autoComplete="new-password"
              />
            </label>
            <div className="row">
              <button type="submit" className="primary" disabled={busy !== "" || !form.host.trim() || !form.user.trim()}>
                {t("camSaveTest")}
              </button>
              {configured && (
                <button
                  type="button"
                  disabled={busy !== ""}
                  onClick={() => void run("test", async () => setReport(await api.testCamera()))}
                >
                  {t("camTest")}
                </button>
              )}
            </div>
          </form>
        )}
        {(busy === "save" || busy === "test") && <p>{t("loading")}</p>}
        {report && (
          <div>
            <ul className="steps">
              {(report.steps ?? []).map((st) => (
                <li key={st.name} className={st.ok ? "ok" : "bad"}>
                  <b>{t(`camStep_${st.name}` as Key)}</b>
                  {st.detail && ` · ${st.detail}`}
                </li>
              ))}
            </ul>
            <details>
              <summary>{t("camRaw")}</summary>
              <pre>{report.raw}</pre>
            </details>
          </div>
        )}
      </div>

      {configured && (
        <div className="card">
          <h3>{t("camLive")}</h3>
          <p>
            <span className={`light ${s.vehicle ? "on" : ""}`} />
            {s.vehicle ? t("camVehicle") : t("camNoVehicle")}
            {s.detection === "motion" && ` · ${t("camMotionOnly")}`}
          </p>
          <LiveVideo videoAvailable={view.live.state === "running"} />
          {view.live.state !== "running" && view.live.reason && <p className="small muted">{view.live.reason}</p>}
          <label className="check">
            <input
              type="checkbox"
              checked={view.settings.auto}
              disabled={busy !== ""}
              onChange={(e) => {
                const auto = e.target.checked;
                void run("auto", async () => setView(await api.saveCamera({ auto })));
              }}
            />
            {t("camAuto")}
          </label>
          <div className="row">
            <button
              type="button"
              className="primary"
              disabled={busy !== ""}
              onClick={() => void run("read", async () => setRead(await api.readCamera()))}
            >
              {t("camReadFrame")}
            </button>
            <button
              type="button"
              disabled={busy !== "" || !!s.visit_move}
              onClick={() =>
                void run("capture", async () => {
                  const r = await api.captureCamera();
                  window.location.hash = `#/moves/${r.move_id}`;
                })
              }
            >
              {t("camCaptureNow")}
            </button>
          </div>
          {busy === "read" && <p>{t("status_reading")}</p>}
          {read && <ReadResult read={read} />}
        </div>
      )}

      {error && <p className="error">{error}</p>}

      {configured && (
        <div className="card">
          <h3>{t("camRecent")}</h3>
          {visits.length === 0 && <p className="muted">{t("noMoves")}</p>}
          <ul className="queue">
            {visits.map((m) => {
              const first = m.photos?.find((p) => !p.deferred) ?? m.photos?.[0];
              return (
                <li key={m.id}>
                  <a href={`#/moves/${m.id}`} className="queue-row">
                    {first ? <img src={blobUrl(first.ai_sha256, 320)} alt="" loading="lazy" /> : <div className="noimg" />}
                    <div>
                      <div className="mono strong">{m.number || t("noNumber")}</div>
                      <StatusChip status={m.status} />
                      <div className="muted small">
                        {new Date(m.created_at).toLocaleTimeString()} · {(m.photos ?? []).length} {t("camPhotos")}
                      </div>
                    </div>
                  </a>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </section>
  );
}

// ReadResult shows "read this frame": the still with the number box, the
// reading, and whether the letters are big enough.
function ReadResult({ read }: { read: CameraRead }) {
  const { t } = useT();
  const r = read.result;
  return (
    <div>
      <PhotoWithBox src={blobUrl(read.still, 1280)} box={r.attempt.reading.box} alt="" />
      {r.error && (
        <p className="error">
          {t("error")}: {r.error}
        </p>
      )}
      <CandidateList candidates={r.frame.candidates} />
      <p>
        {t("camLetter")}: <b>{read.letter_px > 0 ? `${read.letter_px} px` : "–"}</b>
        {read.verdict && ` · ${t(`camVerdict_${read.verdict}` as Key)}`}
      </p>
      <p className="small">
        {t("typeCode")}: {r.frame.type_code || "–"} · {t("color")}: {r.frame.color || "–"} · {r.attempt.latency_ms} ms
      </p>
    </div>
  );
}
```

- [ ] **Step 2: Route, navigation and home tile**

In `web/src/App.tsx`:
- Add `import { Camera } from "./pages/Camera";`.
- Add the route `else if (route === "camera") page = <Camera />;` before the final `else`.
- Add the nav link `<a href="#/camera">{t("navCamera")}</a>` after the Test link.

Replace `web/src/pages/Home.tsx` with:

```tsx
import { useT } from "../i18n";

export function Home() {
  const { t } = useT();
  return (
    <div className="tiles">
      <a className="tile" href="#/capture">
        <b>{t("navCapture")}</b>
        <span>{t("homeCaptureHint")}</span>
      </a>
      <a className="tile" href="#/queue">
        <b>{t("navQueue")}</b>
        <span>{t("homeQueueHint")}</span>
      </a>
      <a className="tile" href="#/camera">
        <b>{t("navCamera")}</b>
        <span>{t("homeCameraHint")}</span>
      </a>
      <a className="tile" href="#/test">
        <b>{t("navTest")}</b>
        <span>{t("homeTestHint")}</span>
      </a>
    </div>
  );
}
```

- [ ] **Step 3: Queue**

In `web/src/pages/Queue.tsx`, replace the `moves.map(...)` body with this version. It uses small previews, prefers a photo that was read, and adds the Camera badge:

```tsx
        {moves.map((m) => {
          const first = m.photos?.find((p) => !p.deferred) ?? m.photos?.[0];
          return (
            <li key={m.id}>
              <a href={`#/moves/${m.id}`} className="queue-row">
                {first ? <img src={blobUrl(first.ai_sha256, 320)} alt="" loading="lazy" /> : <div className="noimg" />}
                <div>
                  <div className="mono strong">{m.number || t("noNumber")}</div>
                  <StatusChip status={m.status} /> {m.source === "camera" && <span className="chip">{t("cameraTag")}</span>}{" "}
                  {m.is_test && <span className="chip">{t("testTag")}</span>}
                  <div className="muted small">
                    {new Date(m.created_at).toLocaleString()} · {m.reason}
                  </div>
                </div>
              </a>
            </li>
          );
        })}
```

- [ ] **Step 4: Truck screen**

In `web/src/pages/MoveDetail.tsx`:

1. Change the imports:

```tsx
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, blobUrl, typedNumber, type Attempt, type MoveDetail as Detail, type Photo } from "../api";
import { LiveVideo } from "../camera/LiveVideo";
import { CandidateList, CheckDigitMath, PhotoWithBox, StatusChip } from "../components";
import { useT } from "../i18n";
import { useMoveEvents } from "../live";
import { readLocal, writeLocal } from "../storage";
```

2. Add a `PhotoCard` component at the bottom of the file:

```tsx
function PhotoCard({ photo, attempt, chosen }: { photo: Photo; attempt: Attempt | undefined; chosen: boolean }) {
  const { t } = useT();
  const reading = attempt?.reading;
  return (
    <figure className="card">
      <PhotoWithBox src={blobUrl(photo.ai_sha256, 640)} box={reading?.box} alt={photo.role} />
      <figcaption>
        <b>{photo.role}</b> {chosen && <span className="chip">{t("readTag")}</span>}
        {attempt?.error && (
          <p className="error">
            {t("error")}: {attempt.error}
          </p>
        )}
        {attempt && !attempt.error && (
          <p className="small">
            {(reading?.candidates ?? []).map((c) => `${c.number} ${Math.round(c.confidence * 100)}%`).join(", ") ||
              t("noNumber")}
            {reading?.type_code && ` · ${reading.type_code}`}
            {reading?.color && ` · ${reading.color}`}
            {` · ${attempt.engine}/${attempt.model} · ${attempt.latency_ms} ms`}
          </p>
        )}
        <a href={blobUrl(photo.sha256)} target="_blank" rel="noreferrer" className="small">
          {t("original")}
        </a>
      </figcaption>
    </figure>
  );
}
```

3. In `MoveDetail`, add live-video state next to the other `useState` calls:

```tsx
  // null: live view hidden; otherwise whether live video is running on the hub
  const [live, setLive] = useState<boolean | null>(null);
```

and this function next to `reread`:

```tsx
  async function toggleLive() {
    if (live !== null) {
      setLive(null);
      return;
    }
    try {
      const v = await api.camera();
      setLive(v.live.state === "running");
    } catch {
      setLive(false);
    }
  }
```

4. After the line `const resolved = move.resolved_by !== "";` add:

```tsx
  const photos = move.photos ?? [];
  const isCamera = move.source === "camera";
  const readPhotos = photos.filter((p) => !p.deferred);
  const otherPhotos = photos.filter((p) => p.deferred);
```

5. Replace the chip line at the top of the card with:

```tsx
        <StatusChip status={move.status} /> {isCamera && <span className="chip">{t("cameraTag")}</span>}{" "}
        {move.is_test && <span className="chip">{t("testTag")}</span>}{" "}
        {isCamera && (
          <button type="button" onClick={() => void toggleLive()}>
            {t("camLive")}
          </button>
        )}
        {live !== null && <LiveVideo videoAvailable={live} />}
```

6. Replace everything from `<h3>{t("photos")}</h3>` to the end of the photo grid with:

```tsx
      <h3>{t("photos")}</h3>
      <div className="photo-grid">
        {readPhotos.map((p) => (
          <PhotoCard key={p.id} photo={p} attempt={latest.get(p.id)} chosen={isCamera} />
        ))}
      </div>
      {otherPhotos.length > 0 && (
        <details>
          <summary>
            {t("otherPhotos")} ({otherPhotos.length})
          </summary>
          <div className="photo-grid">
            {otherPhotos.map((p) => (
              <PhotoCard key={p.id} photo={p} attempt={undefined} chosen={false} />
            ))}
          </div>
        </details>
      )}
```

7. Keep polling while a camera visit is still taking stills. Change the polling effect's condition from `move.status !== "reading"` to:

```tsx
    if (!move || (move.status !== "reading" && move.status !== "capturing")) return;
```

Remove anything that is now unused, so `noUnusedLocals` stays green.

- [ ] **Step 5: Typecheck and build**

Run: `npm --prefix web run typecheck` and then `npm --prefix web run build`
Expected: no errors.

- [ ] **Step 6: Manual check against the simulated camera**

```powershell
go build -o "$env:TEMP\depotd-check.exe" ./cmd/depotd
$data = Join-Path $env:TEMP "depot-cam-ui"
$p = Start-Process -FilePath "$env:TEMP\depotd-check.exe" -ArgumentList "hub","-engine","fake","-fake-camera","-addr","127.0.0.1:18081","-data",$data -PassThru -NoNewWindow
```

Open `http://127.0.0.1:18081/#/camera` in the built-in browser and check each of these:

1. The status line is green ("Bağlı" or "Connected") and shows "Fake camera · 1280×720".
2. The live area shows the refreshing still with the "no live video" caption. A fake camera has no RTSP, and no go2rtc is configured.
3. **Bu kareyi oku / Read this frame** shows the blue picture with an orange box, CSQU3054383, and "72 px · iyi/good".
4. Ticking **Otomatik fotoğraf** makes the "vehicle seen" light come and go every 20 s. A camera visit appears under recent visits.
5. **Şimdi fotoğraf çek / Take photos now** opens the truck screen. It shows the Camera chip and the read photos, with the rest under "Okunmayan fotoğraflar".
6. The Queue shows the Camera chip.

Then run `Stop-Process -Id $p.Id`, stopping only the process you started.

- [ ] **Step 7: Commit**

```bash
git add web/src
git commit -m "feat(web): Camera page with setup, live view, read-this-frame and capture; camera moves in queue and truck screen"
```

---

### Task 13: Runbook and final verification

**Files:**
- Modify: `docs/runbooks/local-test-kit.md` (append a chapter)

- [ ] **Step 1: Append the camera chapter**

Append to `docs/runbooks/local-test-kit.md`:

````markdown
## Fixed camera (Reolink RLC-810WA)

### Prepare the camera (once, in the Reolink app)

1. Power the camera (12 V adapter) and add it to the depot network. A **network cable to the
   router** is more reliable than Wi-Fi; on Wi-Fi use the 5 GHz network.
2. Update the firmware.
3. Settings → Network Information → Advanced → **Port Settings**: switch on **HTTP** and
   **RTSP**. New firmware ships with both off.
4. In the router, give the camera a **fixed address** (DHCP reservation). Write it down.
5. Use a strong camera password. Do **not** forward any router port to the camera, and switch
   UPnP off: the camera must only be reachable inside the depot.
6. Detection: draw the **vehicle detection zone** over the gate lane only; switch people and
   animal alarms off; set **anti-flicker to 50 Hz**.

### Mount and aim

- Aim at the **rear doors** of a container standing where trucks stop: the number and the type
  code (e.g. 45G1) are printed there together.
- Mount it **beside** the lane (not in it), 3.5–4.5 m high, pointing slightly down, at most
  about 30° off straight-on, **4–7 m** from the doors. Avoid low sun in the lens.
- Check with **Bu kareyi oku / Read this frame** on the Camera page: the letter height should
  say *iyi / good* (30 px or more). *Sınırda / borderline* (20–29 px): move closer. Under
  20 px it will not read reliably.

| Distance to the doors | Letter height (4K, this lens) |
|---|---|
| 4 m | ≈ 49 px |
| 6 m | ≈ 33 px |
| 8 m | ≈ 25 px |
| 10 m | ≈ 20 px |

### Live video (go2rtc, once)

Live video needs go2rtc next to `depotd.exe`. Build it from source with Go:

```
go install github.com/AlexxIT/go2rtc@v1.9.14
copy %USERPROFILE%\go\bin\go2rtc.exe bin\go2rtc.exe
```

The hub starts go2rtc by itself, bound to 127.0.0.1 with a password generated at every start. It
asks for no firewall permission and nothing else can use it. Without go2rtc the Camera page
shows a photo refreshed every 2 seconds instead of video.

### Connect it in the app

1. Start the hub as usual and open **Kamera / Camera**.
2. Enter the camera address, user name and password, then **Kaydet ve test et / Save and test**.
   Every line should be green. A red line says what to switch on or fix.
3. Leave **Otomatik fotoğraf** off while aiming. Switch it on when the camera is in place:
   every vehicle in the zone then starts a visit (a 4K photo every second, read until two photos
   agree). A person always confirms camera trucks in this kit.
4. **Şimdi fotoğraf çek / Take photos now** takes 10 photos by hand, for example to test on
   stacked containers when no truck is coming.

Visits where no photo showed a container number (a car, a forklift) are filed as
**Konteyner yok / No container**: not in the review queue, but listed on the Camera page.

### Try it without a camera

```
bin/depotd.exe hub -engine fake -fake-camera
```

A simulated camera "sees" a vehicle for 20 s out of every 40.

### Camera troubleshooting

| Symptom | Fix |
|---|---|
| "Kameraya ulaşma" red | Wrong address, camera unpowered or on another network, or HTTP off in Port Settings |
| "Giriş" red / status "Kullanıcı adı veya şifre yanlış" | Re-enter the password. The hub stops trying after a wrong one; the camera locks logins after several wrong tries |
| "Canlı video portu (RTSP)" red | Switch RTSP on in Port Settings |
| Live area shows photos, not video | `bin\go2rtc.exe` missing (the Camera page says so) or the stream failing; the hub log shows `go2rtc:` lines |
| Every visit is "Konteyner yok" | The camera does not see the number: check aim with Read this frame |
| Visits from cars or people | Shrink the detection zone in the Reolink app; switch people alarms off |
| Letter height "çok küçük / too small" | Move the camera closer to where the doors stop (see the table) |
````

- [ ] **Step 2: Full verification**

Run, from the repo root:

```bash
gofmt -l .
go vet ./...
go test ./... -count=1
npm --prefix web run typecheck
npm --prefix web run build
go build -o "$TEMP/depotd-check.exe" ./cmd/depotd
```

Expected: `gofmt` prints nothing, every package passes, and the typecheck, web build and Go build succeed.

- [ ] **Step 3: Commit**

```bash
git add docs/runbooks/local-test-kit.md
git commit -m "docs: runbook chapter for the fixed camera, go2rtc and aiming"
```

---

## Owner-run checks after the build (not for subagents)

These need the owner's camera, network and approval:

1. **Install go2rtc.** The download needs the owner's OK. Run `go install github.com/AlexxIT/go2rtc@v1.9.14` and copy it to `bin\go2rtc.exe`.
2. **Check go2rtc's isolation.** While the hub runs with a camera set up:
   - `netstat -ano | findstr <go2rtc PID>` shows listeners only on `127.0.0.1`.
   - `curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:<api port>/api/streams` returns `401`. The port is in `depot-data\go2rtc.yaml`.
   - If either check fails, fix `writeConfig` before using live video. The `local_auth` key or the listener keys may differ in v1.9.14, so check go2rtc's README for v1.9.14.
3. **Test with the real camera.**
   1. The Camera page setup test is all green.
   2. Live video plays on the PC and on a phone.
   3. Read this frame on a stacked container shows the letter height.
   4. With automatic photos on, a vehicle pass creates a camera move that reads the number.

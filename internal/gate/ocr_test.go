package gate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testkit"

	"github.com/jackc/pgx/v5"
)

// setModules sets a company's modules as the database owner (depot_app may
// not update companies).
func (h *harness) setModules(t *testing.T, company, modules string) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), h.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), `UPDATE companies SET modules = $2::text[] WHERE id = $1`, company, modules); err != nil {
		t.Fatal(err)
	}
}

// blockingRec holds its first read until release is closed.
type blockingRec struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (b *blockingRec) Name() string  { return "block" }
func (b *blockingRec) Model() string { return "block-1" }
func (b *blockingRec) Read(ctx context.Context, img recognition.Image) (recognition.Attempt, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return recognition.Attempt{Engine: "block", Model: "block-1", Reading: reading("CSQU3054383", 0.9)}, nil
}

func TestOCROffStoresPhotosWithoutReading(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	h.setModules(t, "A", "{}")
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	time.Sleep(150 * time.Millisecond)
	got, err := h.store.GetMove(ctx, m.ID)
	if err != nil || len(got.Photos) != 1 || got.Photos[0].ReadEnabled {
		t.Fatalf("move = %+v, %v; want one photo with ReadEnabled=false", got, err)
	}
	if fake.Calls() != 0 || got.ResolvedBy != "" {
		t.Fatalf("calls = %d, resolved by %q", fake.Calls(), got.ResolvedBy)
	}
}

func TestTurningOCROnDoesNotReadOlderPhotos(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	h.setModules(t, "A", "{}")
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	h.setModules(t, "A", "{ocr}")
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if fake.Calls() != 0 {
		t.Fatalf("calls = %d; want 0", fake.Calls())
	}
}

func TestQueuedReadsStopWhenModuleTurnsOff(t *testing.T) {
	rec := &blockingRec{started: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, rec, Config{Workers: 1}, false)
	reads := startWithHook(t, h)
	m1, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	m2, _ := h.svc.NewMove(ctx, "2", false, "clerk")
	h.photo(t, m1.ID, "rear", "a")
	h.photo(t, m2.ID, "rear", "b")
	h.photo(t, m2.ID, "left", "c")
	<-rec.started
	h.setModules(t, "A", "{}")
	close(rec.release)
	for i := 0; i < 3; i++ {
		nextRead(t, reads) // the hook hears about skipped reads too
	}
	if rec.calls.Load() != 1 {
		t.Fatalf("calls = %d; want 1", rec.calls.Load())
	}
	got := waitStatus(t, h.store, m2.ID, store.StatusNeedsReview)
	if got.Reason != "ocr module is off" || got.ResolvedBy != "" {
		t.Fatalf("move 2 = %+v; want open, in review because ocr is off", got)
	}
	got = waitStatus(t, h.store, m1.ID, store.StatusNeedsReview) // its one read finished; it keeps its result
	if got.Number != "CSQU3054383" || got.ResolvedBy != "" {
		t.Fatalf("move 1 = %+v", got)
	}
}

func TestRestartWithModuleOffSettlesToReview(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	sha, _ := h.blobs.Put([]byte("x"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		t.Fatal(err)
	}
	h.setModules(t, "A", "{}")
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.Reason != "ocr module is off" || fake.Calls() != 0 {
		t.Fatalf("reason = %q, calls = %d", got.Reason, fake.Calls())
	}
}

func TestRereadWithModuleOffDoesNothing(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	h.setModules(t, "A", "{}")
	if err := h.svc.Reread(ctx, m.ID); !errors.Is(err, ErrOCROff) {
		t.Fatalf("Reread = %v; want ErrOCROff", err)
	}
	time.Sleep(150 * time.Millisecond)
	got, _ := h.store.GetMove(ctx, m.ID)
	if fake.Calls() != 1 || got.Status != store.StatusNeedsReview {
		t.Fatalf("calls = %d, status = %s; want 1 and needs_review", fake.Calls(), got.Status)
	}
}

func TestManualEntryValidAcceptsInvalidGoesToReview(t *testing.T) {
	h := newHarness(t, &recognition.Fake{}, Config{}, false)
	m1, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	got, err := h.svc.ManualEntry(ctx, m1.ID, "mscu 123456-6", "ayse")
	if err != nil || got.Status != store.StatusAccepted || !got.Verified || got.Number != "MSCU1234566" {
		t.Fatalf("valid: %+v, %v", got, err)
	}
	m2, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	got, err = h.svc.ManualEntry(ctx, m2.ID, "MSCU1234565", "ayse")
	if err != nil || got.Status != store.StatusNeedsReview || got.Verified || got.Number != "MSCU1234565" ||
		got.Reason != "typed number fails its check digit" || got.ResolvedBy != "" {
		t.Fatalf("invalid: %+v, %v", got, err)
	}
	evs, err := h.store.Events(ctx, m2.ID)
	if err != nil || len(evs) == 0 || evs[len(evs)-1].Actor != "ayse" {
		t.Fatalf("events = %+v, %v; want the last one by the typist", evs, err)
	}
	if _, err := h.svc.ManualEntry(ctx, m2.ID, " - ", "ayse"); err != ErrNumberRequired {
		t.Fatalf("empty = %v; want ErrNumberRequired", err)
	}
}

func TestEachReadCountsOnce(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	h.photo(t, m.ID, "left", "b")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	var n int64
	err := h.ownerQueryRow(t, `SELECT COALESCE(sum(count), 0) FROM usage_counters WHERE company_id = 'A' AND kind = 'ocr_read'`).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("ocr_read = %d, %v; want 2", n, err)
	}
}

func TestEveryModelFailingLeavesTheMoveInReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	t.Cleanup(srv.Close)
	h := newHarness(t, recognition.NewGemini(recognition.GeminiConfig{APIKey: "k", BaseURL: srv.URL}), Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.ResolvedBy != "" {
		t.Fatalf("move = %+v", got)
	}
}

func TestStartContinuesPastAFailingCompany(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	var moves [2]store.Move
	for i, c := range []string{"A", "B"} {
		cc := testkit.CtxOf(c)
		moves[i], _ = h.store.CreateMove(cc, "1", false, "clerk")
		sha, _ := h.blobs.Put([]byte("p" + c))
		if _, err := h.store.AddPhoto(cc, store.Photo{MoveID: moves[i].ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk"); err != nil {
			t.Fatal(err)
		}
	}
	// Reading company A's photos now fails; company B comes after A.
	conn, err := pgx.Connect(context.Background(), h.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(context.Background(), `
		CREATE FUNCTION boom(c text) RETURNS boolean LANGUAGE plpgsql AS $$
		BEGIN IF c = 'A' THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN true; END $$;
		CREATE POLICY boom ON photos AS RESTRICTIVE FOR SELECT USING (boom(company_id))`)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatalf("Start = %v; want it to log the failing company and go on", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.Calls() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("company B's photo was never read")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

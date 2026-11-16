package gate

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testkit"

	"github.com/jackc/pgx/v5"
)

var ctx = testkit.Ctx()

type harness struct {
	svc      *Service
	store    *store.Store
	blobs    *blob.Store
	ownerURL string // the database owner, for tests that change the schema
	notified chan [2]string
}

func newHarness(t *testing.T, rec recognition.Recognizer, cfg Config, start bool) *harness {
	t.Helper()
	st, bl, owner := testkit.OpenCompanies(t, "A", "B")
	h := &harness{store: st, blobs: bl, ownerURL: owner, notified: make(chan [2]string, 256)}
	h.svc = New(st, bl, rec, cfg, func(company, move string) {
		select {
		case h.notified <- [2]string{company, move}:
		default:
		}
	})
	runCtx, cancel := context.WithCancel(context.Background())
	if start {
		if err := h.svc.Start(runCtx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cancel()
		h.svc.Wait()
	})
	return h
}

func reading(number string, conf float64) recognition.Reading {
	return recognition.Reading{Candidates: []recognition.RawCandidate{{Number: number, Confidence: conf}}, TypeCode: "45G1", Color: "blue", Visible: "full"}
}

func (h *harness) photo(t *testing.T, moveID, role, content string) {
	t.Helper()
	_, err := h.svc.AddPhoto(ctx, PhotoInput{
		MoveID: moveID, Role: role, Actor: "clerk",
		Original: []byte("original-" + content), OriginalType: "image/jpeg",
		AI: []byte(content), AIType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitStatus(t *testing.T, st *store.Store, id, want string) store.Move {
	t.Helper()
	return waitStatusCtx(t, ctx, st, id, want)
}

func waitStatusCtx(t *testing.T, ctx context.Context, st *store.Store, id, want string) store.Move {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m, err := st.GetMove(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status == want {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("move %s status = %s; want %s (reason %q)", id, m.Status, want, m.Reason)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgreeingPhotosWaitForAPersonByDefault(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, err := h.svc.NewMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	h.photo(t, m.ID, "rear", "a")
	h.photo(t, m.ID, "left", "b")
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.Number != "CSQU3054383" || got.Decision != string(recognition.DecisionAutoAccept) || got.TypeCode != "45G1" || got.Color != "blue" {
		t.Fatalf("move = %+v", got)
	}
}

func TestAutoAcceptWhenEnabled(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{AutoAcceptEnabled: true}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	h.photo(t, m.ID, "left", "b")
	got := waitStatus(t, h.store, m.ID, store.StatusAccepted)
	if got.Verified {
		t.Fatal("an automatic accept is not a person's verification")
	}
}

func TestEngineErrorGoesToReview(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Err: recognition.ErrUnavailable}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if !strings.Contains(got.Reason, "engine unavailable") {
		t.Fatalf("reason = %q", got.Reason)
	}
}

func TestCorrectionIsRecordedAndVerified(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054384", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)

	got, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: "csqu 305 4383", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusAccepted || got.Number != "CSQU3054383" || !got.Verified || got.CorrectedFrom != "CSQU3054384" {
		t.Fatalf("move = %+v", got)
	}
}

func TestInvalidCorrectionIsAcceptedButUnverified(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	got, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: "ABCU1111111", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusAccepted || got.Verified {
		t.Fatalf("move = %+v; want accepted but unverified", got)
	}
}

func TestResolvedMoveIsClosed(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "refuse", By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.AddPhoto(ctx, PhotoInput{MoveID: m.ID, Role: "rear", Original: []byte("x"), OriginalType: "image/jpeg", AI: []byte("x"), AIType: "image/jpeg"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("AddPhoto err = %v; want ErrClosed", err)
	}
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "accept", By: "ayse"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resolve err = %v; want ErrClosed", err)
	}
}

func TestAddingTheSamePhotoTwiceIsIdempotent(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	in := PhotoInput{
		MoveID: m.ID, Role: "rear", Actor: "clerk",
		Original: []byte("original-a"), OriginalType: "image/jpeg", AI: []byte("a"), AIType: "image/jpeg",
	}
	first, err := h.svc.AddPhoto(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)

	// A phone retrying an upload whose answer it never saw sends the same bytes.
	again, err := h.svc.AddPhoto(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("retry returned photo %s; want the existing %s", again.ID, first.ID)
	}
	got, err := h.store.GetMove(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Photos) != 1 {
		t.Fatalf("photos = %d; want 1", len(got.Photos))
	}
	if got.Status != store.StatusNeedsReview {
		t.Fatalf("status = %s; a retry must not send the move back to reading", got.Status)
	}
	time.Sleep(50 * time.Millisecond) // give a wrongly queued job time to reach the engine
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1", fake.Calls())
	}
}

func TestAddPhotoStoresAndQueuesEvenIfTheRequestIsCancelled(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	gone, cancel := context.WithCancel(ctx)
	cancel() // the phone disconnected
	_, err := h.svc.AddPhoto(gone, PhotoInput{
		MoveID: m.ID, Role: "rear", Actor: "clerk",
		Original: []byte("original-a"), OriginalType: "image/jpeg", AI: []byte("a"), AIType: "image/jpeg",
	})
	if err != nil {
		t.Fatalf("AddPhoto err = %v; a stored photo must always be queued", err)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1", fake.Calls())
	}
}

func TestBadInputsAreRejected(t *testing.T) {
	h := newHarness(t, &recognition.Fake{}, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "dance"}); !errors.Is(err, ErrBadAction) {
		t.Fatalf("err = %v; want ErrBadAction", err)
	}
	if _, err := h.svc.Resolve(ctx, m.ID, Resolution{Action: "correct", Number: " "}); !errors.Is(err, ErrNumberRequired) {
		t.Fatalf("err = %v; want ErrNumberRequired", err)
	}
	if _, err := h.svc.AddPhoto(ctx, PhotoInput{MoveID: m.ID, Role: "rear", Original: []byte("x"), AI: []byte("x"), AIType: "application/pdf"}); !errors.Is(err, ErrBadPhoto) {
		t.Fatalf("err = %v; want ErrBadPhoto", err)
	}
	if _, err := h.svc.Resolve(ctx, "missing", Resolution{Action: "accept"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}

func TestStartRereadsPhotosLeftUnread(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	// Simulate a photo saved just before a power cut: stored, never read.
	ai, _ := h.blobs.Put([]byte("a"))
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: ai, AISHA256: ai, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk"); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 1 {
		t.Fatalf("engine calls = %d; want 1", fake.Calls())
	}
}

func TestStartRecomputesMovesStuckReading(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	// Simulate a re-read cut short by a shutdown: the photo already has an
	// attempt, but the move was left saying "reading" and no job remains.
	ai, _ := h.blobs.Put([]byte("a"))
	p, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: ai, AISHA256: ai, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reading("CSQU3054383", 0.9))
	if _, err := h.store.AddAttempt(ctx, store.Attempt{MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1", Reading: raw, Raw: string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetStatus(ctx, m.ID, store.StatusReading); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if got.Number != "CSQU3054383" {
		t.Fatalf("move = %+v", got)
	}
	if fake.Calls() != 0 {
		t.Fatalf("engine calls = %d; want 0: the photo was already read", fake.Calls())
	}
}

func TestRereadQueuesEveryPhotoAgain(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, _ := h.svc.NewMove(ctx, "1", false, "clerk")
	h.photo(t, m.ID, "rear", "a")
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if err := h.svc.Reread(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.Calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if fake.Calls() != 2 {
		t.Fatalf("engine calls = %d; want 2", fake.Calls())
	}
}

func TestTestReadDoesNotStoreAnything(t *testing.T) {
	h := newHarness(t, &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}, Config{}, true)
	res, err := h.svc.TestRead(ctx, []byte("x"), "image/jpeg")
	if err != nil || res.Error != "" {
		t.Fatalf("TestRead = %+v, %v", res, err)
	}
	if len(res.Frame.Candidates) != 1 || !res.Frame.Candidates[0].CheckDigitValid {
		t.Fatalf("frame = %+v", res.Frame)
	}
	moves, _ := h.store.ListMoves(ctx, store.ListFilter{})
	if len(moves) != 0 {
		t.Fatalf("test read created moves: %d", len(moves))
	}
}

func TestStartRecoversEachCompanyInItsOwnScope(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, false)
	moves := map[string]store.Move{}
	for _, c := range []string{"A", "B"} {
		cctx := testkit.CtxOf(c)
		m, err := h.svc.NewMove(cctx, "1", false, "clerk")
		if err != nil {
			t.Fatal(err)
		}
		ai, _ := h.blobs.Put([]byte("photo-" + c))
		if _, err := h.store.AddPhoto(cctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: ai, AISHA256: ai, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1, ReadEnabled: true}, "clerk"); err != nil {
			t.Fatal(err)
		}
		moves[c] = m
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.svc.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	for c, m := range moves {
		cctx := testkit.CtxOf(c)
		waitStatusCtx(t, cctx, h.store, m.ID, store.StatusNeedsReview)
		attempts, err := h.store.ListAttempts(cctx, m.ID)
		if err != nil || len(attempts) != 1 {
			t.Fatalf("company %s: attempts = %d, %v; want 1", c, len(attempts), err)
		}
		var company string
		err = h.ownerQueryRow(t, `SELECT company_id FROM recognition_attempts WHERE id = $1`, attempts[0].ID).Scan(&company)
		if err != nil || company != c {
			t.Fatalf("attempt of company %s has company_id %q, %v", c, company, err)
		}
	}
	if fake.Calls() != 2 {
		t.Fatalf("engine calls = %d; want 2", fake.Calls())
	}
	got := map[string]string{}
	for len(h.notified) > 0 {
		n := <-h.notified
		got[n[1]] = n[0]
	}
	for c, m := range moves {
		if got[m.ID] != c {
			t.Fatalf("move %s notified for company %q; want %q", m.ID, got[m.ID], c)
		}
	}
}

// ownerQueryRow runs a query as the database owner, outside row-level security.
func (h *harness) ownerQueryRow(t *testing.T, sql string, args ...any) pgx.Row {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), h.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn.QueryRow(context.Background(), sql, args...)
}

func TestDamageAndSignaturePhotosAreNeverRead(t *testing.T) {
	fake := &recognition.Fake{Reading: reading("CSQU3054383", 0.9)}
	h := newHarness(t, fake, Config{}, true)
	m, err := h.svc.NewMove(ctx, "1", false, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	h.photo(t, m.ID, "damage", "dent")
	h.photo(t, m.ID, "signature", "sig")
	h.photo(t, m.ID, "rear", "a")
	// The move settles on the one readable photo: the others are not waited for.
	got := waitStatus(t, h.store, m.ID, store.StatusNeedsReview)
	if n := fake.Calls(); n != 1 {
		t.Fatalf("engine calls = %d, want 1 (the rear photo only)", n)
	}
	for _, p := range got.Photos {
		if p.ReadEnabled != (p.Role == "rear") {
			t.Fatalf("photo %s read_enabled = %v", p.Role, p.ReadEnabled)
		}
	}
	// Nor are they queued again after a restart.
	h2 := New(h.store, h.blobs, fake, Config{}, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	if err := h2.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	h2.Wait()
	if n := fake.Calls(); n != 1 {
		t.Fatalf("engine calls after restart = %d", n)
	}
}

func TestCreateMoveWithClientIDIsIdempotent(t *testing.T) {
	h := newHarness(t, &recognition.Fake{}, Config{}, false)
	const id = "01JD0000000000000000000001"
	a, err := h.svc.CreateMove(ctx, id, "1", false, "clerk")
	if err != nil || a.ID != id {
		t.Fatalf("first create = %+v %v", a, err)
	}
	h.photo(t, id, "rear", "a")
	b, err := h.svc.CreateMove(ctx, id, "2", true, "clerk")
	if err != nil || b.ID != id || b.Lane != "1" || b.IsTest || len(b.Photos) != 1 {
		t.Fatalf("second create = %+v %v", b, err)
	}
	if moves, _ := h.store.ListMoves(ctx, store.ListFilter{}); len(moves) != 1 {
		t.Fatalf("moves = %d", len(moves))
	}
	ev, _ := h.store.Events(ctx, id)
	if n := len(slices.DeleteFunc(ev, func(e store.Event) bool { return e.Kind != "move.created" })); n != 1 {
		t.Fatalf("move.created events = %d", n)
	}
	// Another company may use the same id; it gets its own move.
	if c, err := h.svc.CreateMove(testkit.CtxOf("B"), id, "1", false, "clerk"); err != nil || c.ID != id || len(c.Photos) != 0 {
		t.Fatalf("company B = %+v %v", c, err)
	}
	if m, err := h.svc.CreateMove(ctx, "", "1", false, "clerk"); err != nil || m.ID == "" || m.ID == id {
		t.Fatalf("without an id = %+v %v", m, err)
	}
	for _, bad := range []string{"x", "has space 000000000000000", strings.Repeat("a", 65)} {
		if _, err := h.svc.CreateMove(ctx, bad, "1", false, "clerk"); !errors.Is(err, ErrBadID) {
			t.Fatalf("id %q: %v", bad, err)
		}
	}
}

package gate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"depot/internal/blob"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
)

var ctx = context.Background()

type harness struct {
	svc   *Service
	store *store.Store
	blobs *blob.Store
}

func newHarness(t *testing.T, rec recognition.Recognizer, cfg Config, start bool) *harness {
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
	svc := New(st, bl, rec, cfg, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	if start {
		if err := svc.Start(runCtx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cancel()
		svc.Wait()
		st.Close()
	})
	return &harness{svc: svc, store: st, blobs: bl}
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
	if _, err := h.store.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: ai, AISHA256: ai, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 1}, "clerk"); err != nil {
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

package bench

import (
	"context"
	"strings"
	"sync"
	"testing"

	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testkit"
)

func reads(number string) recognition.Reading {
	return recognition.Reading{Candidates: []recognition.RawCandidate{{Number: number, Confidence: 0.9}}}
}

func TestBenchScoresMovesAndPhotos(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "photo-good", recognition.Reading{}, false)
	testkit.SeedVerifiedMove(t, st, bl, "TEXU1234569", "photo-bad", recognition.Reading{}, false)
	fake := &recognition.Fake{ByImage: map[string]recognition.Reading{
		"photo-good": reads("CSQU3054383"),
		"photo-bad":  reads("CSQU3054383"),
	}}

	rep, err := Run(context.Background(), st, bl, fake, 0.85, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moves != 2 || rep.MovesCorrect != 1 || rep.Photos != 2 || rep.PhotosCorrect != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Misses) != 1 || rep.Misses[0].Want != "TEXU1234569" || rep.Misses[0].Got != "CSQU3054383" {
		t.Fatalf("misses = %+v", rep.Misses)
	}
	if rep.MoveAccuracy() != 0.5 || rep.PhotoAccuracy() != 0.5 {
		t.Fatalf("accuracy = %v, %v", rep.MoveAccuracy(), rep.PhotoAccuracy())
	}
}

func TestBenchHonoursLimit(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "a", recognition.Reading{}, false)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "b", recognition.Reading{}, false)
	rep, err := Run(context.Background(), st, bl, &recognition.Fake{Reading: reads("CSQU3054383")}, 0.85, 1)
	if err != nil || rep.Moves != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

func TestBenchCountsEngineErrors(t *testing.T) {
	st, bl := testkit.Open(t)
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "a", recognition.Reading{}, false)
	rep, err := Run(context.Background(), st, bl, &recognition.Fake{Err: recognition.ErrUnavailable}, 0.85, 0)
	if err != nil || rep.Errors != 1 || rep.MovesCorrect != 0 || len(rep.Misses) != 1 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

func TestBenchSkipsDeferredPhotos(t *testing.T) {
	st, bl := testkit.Open(t)
	m := testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "chosen", recognition.Reading{}, false)
	// A camera still stored for training but never chosen for reading.
	sha, err := bl.Put([]byte("deferred"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPhoto(context.Background(), store.Photo{
		MoveID: m.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg",
		Bytes: 8, Deferred: true,
	}, "camera"); err != nil {
		t.Fatal(err)
	}
	fake := &recognition.Fake{ByImage: map[string]recognition.Reading{
		"chosen":   reads("CSQU3054383"),
		"deferred": reads("TEXU1234569"),
	}}
	rep, err := Run(context.Background(), st, bl, fake, 0.85, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fake.Calls() != 1 || rep.Photos != 1 || rep.PhotosCorrect != 1 || rep.MovesCorrect != 1 {
		t.Fatalf("engine calls %d, report %+v; want only the chosen photo read", fake.Calls(), rep)
	}
}

// detailEngine records the Detail of every image it reads.
type detailEngine struct {
	mu      sync.Mutex
	details []string
}

func (d *detailEngine) Name() string  { return "detail" }
func (d *detailEngine) Model() string { return "detail-1" }
func (d *detailEngine) Read(ctx context.Context, img recognition.Image) (recognition.Attempt, error) {
	d.mu.Lock()
	d.details = append(d.details, img.Detail)
	d.mu.Unlock()
	return recognition.Attempt{Reading: reads("CSQU3054383")}, nil
}

func TestBenchReadsCameraStillsInUltraHighDetail(t *testing.T) {
	st, bl := testkit.Open(t)
	ctx := context.Background()
	testkit.SeedVerifiedMove(t, st, bl, "CSQU3054383", "phone", recognition.Reading{}, false)
	m, err := st.CreateMoveWithSource(ctx, "camera", store.SourceCamera, false, "camera")
	if err != nil {
		t.Fatal(err)
	}
	sha, _ := bl.Put([]byte("\xff\xd8\xff\xe0still"))
	if _, err := st.AddPhoto(ctx, store.Photo{MoveID: m.ID, Role: "rear", SHA256: sha, AISHA256: sha, MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 9}, "camera"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Resolve(ctx, m.ID, store.ResolveUpdate{Status: store.StatusAccepted, Number: "CSQU3054383", Verified: true, By: "seed"}); err != nil {
		t.Fatal(err)
	}
	eng := &detailEngine{}
	if _, err := Run(ctx, st, bl, eng, 0.85, 0); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(eng.details, ","); got != ","+recognition.DetailUltraHigh {
		t.Fatalf("details = %q; want the phone photo by default, then the camera still in ultra-high", eng.details)
	}
}

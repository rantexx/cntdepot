package bench

import (
	"context"
	"testing"

	"depot/internal/recognition"
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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

var ctx = context.Background()

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "depot.db")
	s, err := Open(path, "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func addPhoto(t *testing.T, s *Store, moveID string) Photo {
	t.Helper()
	p, err := s.AddPhoto(ctx, Photo{MoveID: moveID, Role: "rear", SHA256: "a", AISHA256: "b", MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: 10}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateAndGetMove(t *testing.T) {
	s, _ := openTest(t)
	m, err := s.CreateMove(ctx, "2", true, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMove(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lane != "2" || !got.IsTest || got.Status != StatusCapturing || got.Direction != "in" || len(got.Photos) != 0 {
		t.Fatalf("move = %+v", got)
	}
	if _, err := s.GetMove(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v; want ErrNotFound", err)
	}
}

func TestPhotosAttemptsAndPending(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	p := addPhoto(t, s, m.ID)

	pending, err := s.PhotosWithoutAttempts(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != p.ID {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	if _, err := s.AddAttempt(ctx, Attempt{MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1", PromptVersion: "v", Reading: json.RawMessage(`{"candidates":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PhotosWithoutAttempts(ctx); len(pending) != 0 {
		t.Fatalf("pending after attempt = %+v", pending)
	}
	attempts, err := s.ListAttempts(ctx, m.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Engine != "fake" {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	got, _ := s.GetMove(ctx, m.ID)
	if len(got.Photos) != 1 || got.Photos[0].Role != "rear" {
		t.Fatalf("photos = %+v", got.Photos)
	}
}

func TestResultThenResolveKeepsHistory(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	err := s.SetResult(ctx, m.ID, ReadOutcome{Status: StatusNeedsReview, Number: "CSQU3054384", Decision: "review", Reason: "check digit failed", ResultJSON: []byte(`{"number":"CSQU3054384"}`)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054383", Verified: true, CorrectedFrom: "CSQU3054384", By: "ayse"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != "CSQU3054383" || !got.Verified || got.CorrectedFrom != "CSQU3054384" || got.ResolvedBy != "ayse" {
		t.Fatalf("resolved move = %+v", got)
	}

	events, err := s.Events(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[0] != "move.created" || kinds[1] != "move.read" || kinds[2] != "move.resolved" {
		t.Fatalf("event kinds = %v", kinds)
	}
	var read map[string]any
	if err := json.Unmarshal(events[1].Payload, &read); err != nil || read["number"] != "CSQU3054384" {
		t.Fatalf("the machine's original read must survive the correction: %s", events[1].Payload)
	}
}

func TestSetResultAfterResolveIsIgnored(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	if _, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusRefused, By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResult(ctx, m.ID, ReadOutcome{Status: StatusNeedsReview, Number: "CSQU3054383"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMove(ctx, m.ID)
	if got.Status != StatusRefused || got.Number != "" {
		t.Fatalf("a person's decision was overwritten: %+v", got)
	}
}

func TestListMovesFiltersAndPages(t *testing.T) {
	s, _ := openTest(t)
	a, _ := s.CreateMove(ctx, "1", false, "clerk")
	b, _ := s.CreateMove(ctx, "1", false, "clerk")
	c, _ := s.CreateMove(ctx, "1", false, "clerk")
	if err := s.SetStatus(ctx, b.ID, StatusNeedsReview); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMoves(ctx, ListFilter{})
	if err != nil || len(all) != 3 || all[0].ID != c.ID {
		t.Fatalf("all = %v, %v; want 3, newest first", ids(all), err)
	}
	review, _ := s.ListMoves(ctx, ListFilter{Status: StatusNeedsReview})
	if len(review) != 1 || review[0].ID != b.ID {
		t.Fatalf("needs_review = %v", ids(review))
	}
	older, _ := s.ListMoves(ctx, ListFilter{Before: b.ID})
	if len(older) != 1 || older[0].ID != a.ID {
		t.Fatalf("before b = %v", ids(older))
	}
}

func TestVerifiedMovesExcludesTestAndUnverified(t *testing.T) {
	s, _ := openTest(t)
	good, _ := s.CreateMove(ctx, "1", false, "clerk")
	practice, _ := s.CreateMove(ctx, "1", true, "clerk")
	unverified, _ := s.CreateMove(ctx, "1", false, "clerk")
	for _, m := range []Move{good, practice} {
		if _, err := s.Resolve(ctx, m.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054383", Verified: true, By: "ayse"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Resolve(ctx, unverified.ID, ResolveUpdate{Status: StatusAccepted, Number: "CSQU3054384", By: "ayse"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.VerifiedMoves(ctx)
	if err != nil || len(got) != 1 || got[0].ID != good.ID {
		t.Fatalf("verified = %v, %v", ids(got), err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	s, path := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	s.Close()
	again, err := Open(path, "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.GetMove(ctx, m.ID); err != nil {
		t.Fatalf("move lost after reopen: %v", err)
	}
}

func ids(moves []Move) []string {
	out := []string{}
	for _, m := range moves {
		out = append(out, m.ID)
	}
	return out
}

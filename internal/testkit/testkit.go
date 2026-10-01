// Package testkit holds helpers shared by tests in several packages.
package testkit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"depot/internal/blob"
	"depot/internal/iso6346"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/testdir"
)

// Open returns a fresh database and photo store in a temporary folder.
func Open(t *testing.T) (*store.Store, *blob.Store) {
	t.Helper()
	dir := testdir.New(t)
	st, err := store.Open(filepath.Join(dir, "depot.db"), "local", "own-depot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bl, err := blob.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return st, bl
}

// SeedVerifiedMove stores a move with one photo, as if an engine had read it
// (reading) and a person then confirmed number. The photo's engine copy holds
// exactly aiContent, so a recognition.Fake can be scripted per photo.
func SeedVerifiedMove(t *testing.T, st *store.Store, bl *blob.Store, number, aiContent string, reading recognition.Reading, isTest bool) store.Move {
	t.Helper()
	ctx := context.Background()
	m, err := st.CreateMove(ctx, "1", isTest, "seed")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := bl.Put([]byte("\xff\xd8\xff\xe0original-" + aiContent))
	if err != nil {
		t.Fatal(err)
	}
	ai, err := bl.Put([]byte(aiContent))
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.AddPhoto(ctx, store.Photo{
		MoveID: m.ID, Role: "rear", SHA256: orig, AISHA256: ai,
		MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: int64(len(aiContent)),
	}, "seed")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddAttempt(ctx, store.Attempt{
		MoveID: m.ID, PhotoID: p.ID, Engine: "fake", Model: "fake-1",
		PromptVersion: recognition.PromptVersion, Reading: raw, Raw: string(raw),
	}); err != nil {
		t.Fatal(err)
	}
	engineNumber := ""
	if len(reading.Candidates) > 0 {
		engineNumber = iso6346.Normalize(reading.Candidates[0].Number)
	}
	if err := st.SetResult(ctx, m.ID, store.ReadOutcome{
		Status: store.StatusNeedsReview, Number: engineNumber, TypeCode: reading.TypeCode, Color: reading.Color,
	}); err != nil {
		t.Fatal(err)
	}
	update := store.ResolveUpdate{Status: store.StatusAccepted, Number: number, Verified: iso6346.IsValid(number), By: "seed"}
	if engineNumber != "" && engineNumber != number {
		update.CorrectedFrom = engineNumber
	}
	got, err := st.Resolve(ctx, m.ID, update)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

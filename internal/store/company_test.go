package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"depot/internal/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
)

func TestEveryStoreMethodRespectsCompany(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	p := addPhoto(t, s, m.ID)
	if _, err := s.AddAttempt(ctx, Attempt{MoveID: m.ID, PhotoID: p.ID, Engine: "fake"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(ctx, map[string]string{"k": "v"}, "admin"); err != nil {
		t.Fatal(err)
	}
	// A second photo without an attempt, so B would see it as pending if it leaked.
	addPhoto(t, s, m.ID)

	b := ctxOf("B")
	if _, err := s.GetMove(b, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMove = %v", err)
	}
	if got, err := s.ListMoves(b, ListFilter{}); err != nil || len(got) != 0 {
		t.Errorf("ListMoves = %v, %v", got, err)
	}
	if got, err := s.VerifiedMoves(b); err != nil || len(got) != 0 {
		t.Errorf("VerifiedMoves = %v, %v", got, err)
	}
	if err := s.SetStatus(b, m.ID, StatusRefused); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatus = %v", err)
	}
	if err := s.SetResult(b, m.ID, ReadOutcome{Status: StatusAccepted, Number: "X"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetResult = %v", err)
	}
	if _, err := s.Resolve(b, m.ID, ResolveUpdate{Status: StatusAccepted, By: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve = %v", err)
	}
	if _, err := s.AddPhoto(b, Photo{MoveID: m.ID, Role: "rear"}, "x"); err == nil {
		t.Error("AddPhoto on another company's move succeeded")
	}
	if got, err := s.ListPhotos(b, m.ID); err != nil || len(got) != 0 {
		t.Errorf("ListPhotos = %v, %v", got, err)
	}
	if _, err := s.GetPhoto(b, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPhoto = %v", err)
	}
	if err := s.MarkForReading(b, p.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkForReading = %v", err)
	}
	if got, err := s.PhotosWithoutAttempts(b); err != nil || len(got) != 0 {
		t.Errorf("PhotosWithoutAttempts = %v, %v", got, err)
	}
	if _, err := s.AddAttempt(b, Attempt{MoveID: m.ID, PhotoID: p.ID}); err == nil {
		t.Error("AddAttempt on another company's photo succeeded")
	}
	if got, err := s.ListAttempts(b, m.ID); err != nil || len(got) != 0 {
		t.Errorf("ListAttempts = %v, %v", got, err)
	}
	if got, err := s.Events(b, m.ID); err != nil || len(got) != 0 {
		t.Errorf("Events = %v, %v", got, err)
	}
	if got, err := s.Settings(b, ""); err != nil || len(got) != 0 {
		t.Errorf("Settings = %v, %v", got, err)
	}
	// A's data is untouched by B's attempts.
	got, err := s.GetMove(ctx, m.ID)
	if err != nil || got.Status != StatusCapturing {
		t.Errorf("A's move = %+v, %v", got, err)
	}
}

func TestNoScopeIsAnError(t *testing.T) {
	s, _ := openTest(t)
	bg := context.Background()
	if _, err := s.GetMove(bg, "x"); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("GetMove = %v", err)
	}
	if _, err := s.CreateMove(bg, "1", false, "c"); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("CreateMove = %v", err)
	}
	if _, err := s.Settings(bg, ""); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("Settings = %v", err)
	}
	if _, err := s.CompanyModules(bg); !errors.Is(err, tenancy.ErrNoScope) {
		t.Errorf("CompanyModules = %v", err)
	}
}

func TestOnlyReadEnabledPhotosArePending(t *testing.T) {
	s, _ := openTest(t)
	m, _ := s.CreateMove(ctx, "1", false, "clerk")
	off, err := s.AddPhoto(ctx, Photo{MoveID: m.ID, Role: "rear", MediaType: "image/jpeg", AIMediaType: "image/jpeg"}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	on := addPhoto(t, s, m.ID)
	got, err := s.PhotosWithoutAttempts(ctx)
	if err != nil || len(got) != 1 || got[0].ID != on.ID || got[0].ID == off.ID {
		t.Fatalf("pending = %+v, %v; want only the read-enabled photo", got, err)
	}
}

func TestPhotoByDigestStaysInCompany(t *testing.T) {
	s, _ := openTest(t)
	b := ctxOf("B")
	mb, _ := s.CreateMove(b, "1", false, "clerk")
	pb, err := s.AddPhoto(b, Photo{MoveID: mb.ID, Role: "rear", SHA256: "dig-b", AISHA256: "ai-b"}, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"dig-b", "ai-b"} {
		if got, err := s.PhotoByDigest(b, d); err != nil || got.ID != pb.ID {
			t.Errorf("B PhotoByDigest(%s) = %+v, %v", d, got, err)
		}
		if _, err := s.PhotoByDigest(ctx, d); !errors.Is(err, ErrNotFound) {
			t.Errorf("A PhotoByDigest(%s) = %v; want ErrNotFound", d, err)
		}
	}
}

func TestUsageCountsPerDay(t *testing.T) {
	s, pool := openTest(t)
	for i := 0; i < 2; i++ {
		if err := s.AddUsage(ctx, "ocr_read", 1); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	var day time.Time
	err := db.InCompany(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count, day FROM usage_counters WHERE kind = 'ocr_read'`).Scan(&n, &day)
	})
	if err != nil || n != 2 || day.Format("2006-01-02") != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("count = %d, day = %v, %v", n, day, err)
	}
}

func TestCompanyIDsAndModules(t *testing.T) {
	s, _ := openTest(t)
	ids, err := s.CompanyIDs(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != "A" || ids[1] != "B" {
		t.Fatalf("CompanyIDs = %v, %v", ids, err)
	}
	mods, err := s.CompanyModules(ctx)
	if err != nil || len(mods) != 1 || mods[0] != "ocr" {
		t.Fatalf("CompanyModules = %v, %v", mods, err)
	}
}

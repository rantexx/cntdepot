// Package testkit holds helpers shared by tests in several packages.
package testkit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"depot/internal/blob"
	"depot/internal/db"
	"depot/internal/iso6346"
	"depot/internal/pgtest"
	"depot/internal/recognition"
	"depot/internal/store"
	"depot/internal/tenancy"
	"depot/internal/testdir"

	"github.com/jackc/pgx/v5"
)

// Scope is company A's depot d1, the scope tests run under.
var Scope = tenancy.Scope{CompanyID: "A", DepotID: "d1", UserID: "u1", Role: tenancy.Owner}

// Ctx is a context scoped to Scope.
func Ctx() context.Context { return tenancy.With(context.Background(), Scope) }

// CtxOf is a context scoped to another company's depot d1.
func CtxOf(company string) context.Context {
	sc := Scope
	sc.CompanyID = company
	return tenancy.With(context.Background(), sc)
}

// Open returns a fresh database with company A (depot d1) and a photo store
// in a temporary folder. The package's TestMain must call pgtest.Main.
func Open(t *testing.T) (*store.Store, *blob.Store) {
	st, bl, _ := OpenCompanies(t, "A")
	return st, bl
}

// OpenCompanies is Open with one company per ID, each with depot d1. It also
// returns the database owner's URL, for tests that must change the schema.
func OpenCompanies(t *testing.T, companies ...string) (*store.Store, *blob.Store, string) {
	t.Helper()
	bg := context.Background()
	owner := pgtest.NewDB(t)
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "app-pw"}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(bg, db.AppURL(owner, "app-pw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, c := range companies {
		err := db.Global(bg, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO companies (id, name, modules) VALUES ($1, $1, '{ocr}')`, c)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.InCompany(CtxOf(c), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO depots (company_id, id, name) VALUES ($1, 'd1', 'Depot')`, c)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	bl, err := blob.Open(filepath.Join(testdir.New(t), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return store.New(pool), bl, owner
}

// SeedVerifiedMove stores a move with one photo, as if an engine had read it
// (reading) and a person then confirmed number. The photo's engine copy holds
// exactly aiContent, so a recognition.Fake can be scripted per photo.
func SeedVerifiedMove(t *testing.T, st *store.Store, bl *blob.Store, number, aiContent string, reading recognition.Reading, isTest bool) store.Move {
	t.Helper()
	ctx := Ctx()
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
		MediaType: "image/jpeg", AIMediaType: "image/jpeg", Bytes: int64(len(aiContent)), ReadEnabled: true,
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

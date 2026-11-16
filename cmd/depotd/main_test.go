package main

import (
	"context"
	"strings"

	"depot/internal/auth"
	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStills(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.JPG": "b", "a.jpeg": "a", "notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stills, err := loadStills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stills) != 2 || string(stills[0]) != "a" || string(stills[1]) != "b" {
		t.Fatalf("stills = %q, want the two photos in name order", stills)
	}
	if s, err := loadStills(""); s != nil || err != nil {
		t.Fatalf("no folder: got %v, %v", s, err)
	}
	if _, err := loadStills(t.TempDir()); err == nil {
		t.Fatal("a folder without photos should be an error")
	}
}

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func TestApiRefusesDevLoginInProduction(t *testing.T) {
	err := run([]string{"api", "-env", "production", "-dev-login"})
	if err == nil || !strings.Contains(err.Error(), "dev login") {
		t.Fatalf("err = %v, want a refusal that mentions dev login", err)
	}
}

func TestSeedHubIsRepeatableAndDevLoginFindsTheOwner(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.NewDB(t)
	if err := db.Migrate(ctx, owner, db.MigrateOptions{AppPassword: "pw"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := seedHub(ctx, owner, "Owner@Local"); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := db.Open(ctx, db.AppURL(owner, "pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := auth.NewSessions(pool, 0)
	id, err := auth.NewDevProvider().Exchange(ctx, "owner@local")
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := s.Login(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := s.Resolve(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur.Memberships) != 1 || cur.Memberships[0].CompanyID != hubCompany || cur.Memberships[0].Role != tenancy.Owner {
		t.Fatalf("memberships = %+v, want one owner of %s", cur.Memberships, hubCompany)
	}
}

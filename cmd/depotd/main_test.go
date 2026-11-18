package main

import (
	"bytes"
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

func TestHelpAndFlagErrorsNeverPrintSecrets(t *testing.T) {
	secrets := []string{"postgres://u:s3cretpw@h/d", "owner-url-s3cret", "app-pw-s3cret", "gemini-s3cret", "postmark-s3cret", "hook-s3cret"}
	t.Setenv("DATABASE_URL", secrets[0])
	t.Setenv("DATABASE_OWNER_URL", secrets[1])
	t.Setenv("DATABASE_APP_PASSWORD", secrets[2])
	t.Setenv("GEMINI_API_KEY", secrets[3])
	t.Setenv("POSTMARK_SERVER_TOKEN", secrets[4])
	t.Setenv("POSTMARK_INBOUND_SECRET", secrets[5])
	var buf bytes.Buffer
	old := flagOutput
	flagOutput = &buf
	defer func() { flagOutput = old }()
	for _, mode := range []string{"migrate", "api", "hub", "export-dataset", "bench"} {
		_ = run([]string{mode, "-h"})
		_ = run([]string{mode, "-no-such-flag"})
	}
	if buf.Len() == 0 {
		t.Fatal("expected usage output")
	}
	for _, s := range secrets {
		if strings.Contains(buf.String(), s) {
			t.Fatalf("output contains secret %q", s)
		}
	}
}

func TestHubListensOnLoopbackByDefault(t *testing.T) {
	var buf bytes.Buffer
	old := flagOutput
	flagOutput = &buf
	defer func() { flagOutput = old }()
	_ = run([]string{"hub", "-h"})
	if !strings.Contains(buf.String(), `(default "127.0.0.1:8080")`) || !strings.HasPrefix(hubDefaultAddr, "127.0.0.1:") {
		t.Fatalf("hub default address is not loopback: %s", buf.String())
	}
}

func TestApiRefusesAnUnknownEnv(t *testing.T) {
	for _, env := range []string{"staging", "prod", "Production", ""} {
		err := run([]string{"api", "-env", env, "-db", "postgres://unused"})
		if err == nil || !strings.Contains(err.Error(), "-env must be development or production") {
			t.Errorf("-env %q: err = %v, want a refusal", env, err)
		}
	}
}

func TestApiListensOnLoopbackWhileDevLoginIsOn(t *testing.T) {
	cases := []struct {
		addr     string
		devLogin bool
		want     string
	}{
		{"", true, "127.0.0.1:8080"},
		{"", false, ":8080"},
		{":9000", true, ":9000"},
	}
	for _, c := range cases {
		if got := apiAddr(c.addr, c.devLogin); got != c.want {
			t.Errorf("apiAddr(%q, %v) = %q; want %q", c.addr, c.devLogin, got, c.want)
		}
	}
}

func TestMailFlags(t *testing.T) {
	for _, k := range []string{"MAIL_DRIVER", "MAIL_DIR", "MAIL_FROM", "EDI_TICK", "POSTMARK_SERVER_TOKEN", "EDI_INBOUND_DOMAIN"} {
		t.Setenv(k, "")
	}
	// The hub defaults to the file driver in its data folder; api needs a folder.
	m, cfg, err := (&mailFlags{}).build(filepath.Join("data", "mail-out"))
	if err != nil || m.Name() != "file" || cfg.From != "depot@localhost" || cfg.Tick.String() != "30s" {
		t.Fatalf("hub default = %v %+v %v", m, cfg, err)
	}
	if _, _, err := (&mailFlags{}).build(""); err == nil || !strings.Contains(err.Error(), "-mail-dir") {
		t.Errorf("api without a mail dir: err = %v", err)
	}
	if _, _, err := (&mailFlags{driver: "postmark"}).build(""); err == nil || !strings.Contains(err.Error(), "POSTMARK_SERVER_TOKEN") {
		t.Errorf("postmark without a token: err = %v", err)
	}
	for _, f := range []mailFlags{{driver: "smtp", dir: "x"}, {dir: "x", tick: "0s"}, {dir: "x", tick: "soon"}} {
		if _, _, err := f.build(""); err == nil {
			t.Errorf("%+v: no error", f)
		}
	}
	t.Setenv("MAIL_DRIVER", "postmark")
	t.Setenv("POSTMARK_SERVER_TOKEN", "pm-token")
	t.Setenv("EDI_TICK", "1s")
	t.Setenv("EDI_INBOUND_DOMAIN", "in.example.com")
	m, cfg, err = (&mailFlags{from: "edi@example.com"}).build("")
	if err != nil || m.Name() != "postmark" || cfg.Tick.String() != "1s" || cfg.From != "edi@example.com" || cfg.InboundDomain != "in.example.com" {
		t.Fatalf("from env = %v %+v %v", m, cfg, err)
	}
}

package refdata_test

import (
	"context"
	"os"
	"testing"

	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/refdata"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

var bg = context.Background()

func migrated(t *testing.T) *pgx.Conn {
	t.Helper()
	owner := pgtest.NewDB(t)
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "pw"}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(bg, owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(bg) })
	return conn
}

func counts(t *testing.T, c *pgx.Conn) (liners, locodes int, versions string) {
	t.Helper()
	err := c.QueryRow(bg, `SELECT (SELECT count(*) FROM ref_smdg_liners), (SELECT count(*) FROM ref_locodes),
		(SELECT coalesce(string_agg(name || '=' || version, ',' ORDER BY name), '') FROM ref_versions)`).Scan(&liners, &locodes, &versions)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func loadFixtures(t *testing.T, c *pgx.Conn, smdg, locode string) {
	t.Helper()
	err := pgx.BeginFunc(bg, c, func(tx pgx.Tx) error { return refdata.LoadFrom(bg, tx, os.DirFS("testdata"), smdg, locode) })
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoadIsIdempotent(t *testing.T) {
	c := migrated(t)
	// Migrate already ran Load with the bundled (empty) lists.
	if l, p, v := counts(t, c); l != 0 || p != 0 || v != "ref_locodes=none,ref_smdg_liners=none" {
		t.Fatalf("after migrate: %d %d %q", l, p, v)
	}
	if err := pgx.BeginFunc(bg, c, func(tx pgx.Tx) error { return refdata.Load(bg, tx) }); err != nil {
		t.Fatal(err)
	}
	if l, p, _ := counts(t, c); l != 0 || p != 0 {
		t.Fatalf("second Load changed rows: %d %d", l, p)
	}
	for i := 0; i < 2; i++ {
		loadFixtures(t, c, "t1", "t1")
		if l, p, v := counts(t, c); l != 3 || p != 3 || v != "ref_locodes=t1,ref_smdg_liners=t1" {
			t.Fatalf("load %d: %d %d %q", i, l, p, v)
		}
	}
	// Same version: untouched even when a row was removed by hand.
	if _, err := c.Exec(bg, "DELETE FROM ref_locodes WHERE code = 'NLRTM'"); err != nil {
		t.Fatal(err)
	}
	loadFixtures(t, c, "t1", "t1")
	if _, p, _ := counts(t, c); p != 2 {
		t.Fatalf("equal version must not reload: %d locodes", p)
	}
	// New version: replaced.
	loadFixtures(t, c, "t1", "t2")
	if _, p, v := counts(t, c); p != 3 || v != "ref_locodes=t2,ref_smdg_liners=t1" {
		t.Fatalf("new version: %d %q", p, v)
	}
}

func TestKnownCodesPresent(t *testing.T) {
	c := migrated(t)
	loadFixtures(t, c, "t1", "t1")
	var liner, port, country, ist string
	if err := c.QueryRow(bg, "SELECT name FROM ref_smdg_liners WHERE code = 'MSK'").Scan(&liner); err != nil || liner != "Maersk" {
		t.Fatalf("MSK = %q, %v", liner, err)
	}
	if err := c.QueryRow(bg, "SELECT name, country FROM ref_locodes WHERE code = 'TRMER'").Scan(&port, &country); err != nil || port != "Mersin" || country != "TR" {
		t.Fatalf("TRMER = %q %q, %v", port, country, err)
	}
	if err := c.QueryRow(bg, "SELECT name FROM ref_locodes WHERE code = 'TRIST'").Scan(&ist); err != nil || ist != "İstanbul" {
		t.Fatalf("TRIST = %q, %v", ist, err)
	}
}

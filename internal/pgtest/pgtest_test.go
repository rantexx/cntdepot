package pgtest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func TestNewDBGivesIsolatedDatabases(t *testing.T) {
	a, b := NewDB(t), NewDB(t)
	if a == b {
		t.Fatal("same URL twice")
	}
	ctx := context.Background()
	ca, err := pgx.Connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer ca.Close(ctx)
	cb, err := pgx.Connect(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close(ctx)
	if _, err := ca.Exec(ctx, "CREATE TABLE x (id int)"); err != nil {
		t.Fatal(err)
	}
	for conn, want := range map[*pgx.Conn]bool{ca: false, cb: true} {
		var absent bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass('x') IS NULL").Scan(&absent); err != nil {
			t.Fatal(err)
		}
		if absent != want {
			t.Fatalf("absent=%v want %v", absent, want)
		}
	}
}

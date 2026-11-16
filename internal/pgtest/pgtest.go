// Package pgtest runs one embedded PostgreSQL per test binary and hands out
// a fresh database per test.
package pgtest

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

const password = "pgtest"

var adminURL string // superuser URL on the maintenance database

// Main starts PostgreSQL, runs the tests and stops the server.
func Main(m *testing.M) (code int) {
	cache, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	cache = filepath.Join(cache, "depot", "embedded-postgres")
	// Own dir per binary (distinct data and extracted binaries, so parallel
	// test binaries never collide); only the downloaded archive is shared.
	dir, err := os.MkdirTemp("", "depot-pg-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	port, err := freePort()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Port(uint32(port)).
		Username("postgres").Password(password).
		CachePath(cache).
		RuntimePath(filepath.Join(dir, "run")).
		BinariesPath(filepath.Join(dir, "bin")).
		DataPath(filepath.Join(dir, "data")).
		Logger(io.Discard).
		StartTimeout(2 * time.Minute))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest: start:", err)
		return 1
	}
	defer func() {
		if err := pg.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "pgtest: stop:", err)
		}
	}()
	adminURL = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/", password, port)
	return m.Run()
}

// NewDB creates a fresh database and returns its superuser URL. It is
// dropped when the test ends.
func NewDB(t *testing.T) string {
	t.Helper()
	if adminURL == "" {
		t.Fatal("pgtest.Main not running: add TestMain calling pgtest.Main")
	}
	name := "t_" + strings.ToLower(ulid.Make().String())
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL+"postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, adminURL+"postgres")
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	})
	return adminURL + name
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

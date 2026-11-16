// Package pgtest runs one embedded PostgreSQL per test binary and hands out
// a fresh database per test.
package pgtest

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	sweep(os.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pgtest:", err)
		return 1
	}
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
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if err := pg.Stop(); err != nil {
				fmt.Fprintln(os.Stderr, "pgtest: stop:", err)
			}
			os.RemoveAll(dir)
		})
	}
	defer cleanup()
	// Ctrl-C / SIGTERM skip defers; panics and -timeout are covered by sweep on the next run.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; cleanup(); os.Exit(1) }()
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

// sweep removes depot-pg-* dirs under root whose owner process is gone,
// stopping any postgres still running from them. Dirs of live owners
// (parallel test binaries) are never touched.
func sweep(root string) {
	dirs, _ := filepath.Glob(filepath.Join(root, "depot-pg-*"))
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "pid"))
		if err != nil {
			// No pid yet: owner may be mid-creation; only reap old dirs.
			if fi, e := os.Stat(d); e != nil || time.Since(fi.ModTime()) < time.Minute {
				continue
			}
		} else if pid, err := strconv.Atoi(string(b)); err == nil && alive(pid) {
			continue
		}
		ctl := filepath.Join(d, "bin", "bin", "pg_ctl")
		if runtime.GOOS == "windows" {
			ctl += ".exe"
		}
		if _, err := os.Stat(ctl); err == nil {
			exec.Command(ctl, "stop", "-D", filepath.Join(d, "data"), "-m", "immediate").Run()
		}
		os.RemoveAll(d)
	}
}

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" { // FindProcess fails for a gone process
		p.Release()
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}

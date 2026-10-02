// Package store is the hub's SQLite database. Every state change writes the
// record and an append-only event in one transaction (spec §7.3).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2.sql
var schemaV2 string

// migrations run in order. Append new versions; never edit a shipped one.
var migrations = []string{schemaV1, schemaV2}

// Move statuses.
const (
	StatusCapturing   = "capturing"
	StatusReading     = "reading"
	StatusNeedsReview = "needs_review"
	StatusAccepted    = "accepted"
	StatusRefused     = "refused"
	// StatusNoContainer is a camera visit whose photos showed no container
	// number, such as a car or forklift passing. It stays out of the review queue.
	StatusNoContainer = "no_container"
)

// Where a move's photos come from.
const (
	SourcePhone  = "phone"
	SourceCamera = "camera"
)

var ErrNotFound = errors.New("not found")

// Store is the hub database.
type Store struct {
	db     *sql.DB
	tenant string
	depot  string
	now    func() time.Time
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path, tenant, depot string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer and the hub's load is tiny.
	// Never call a store method while a *sql.Rows from this store is open.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, tenant: tenant, depot: depot, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	for v := current; v < len(migrations); v++ {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range strings.Split(migrations[v], ";") {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v+1)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) appendEvent(ctx context.Context, tx *sql.Tx, stream, kind, actor string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (id, tenant_id, stream, kind, actor, payload, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID(), s.tenant, stream, kind, actor, string(body), ts(s.now()))
	return err
}

type scanner interface{ Scan(dest ...any) error }

func newID() string { return ulid.Make().String() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

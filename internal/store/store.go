// Package store is the depot's PostgreSQL database. Every state change writes
// the record and an append-only event in one transaction (spec §7.3). Each
// call runs under the company in the context's tenancy.Scope, which row-level
// security enforces; without a scope it returns tenancy.ErrNoScope.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"depot/internal/db"
	sqlcdb "depot/internal/store/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

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

// Store is the depot database. The pool must connect as role depot_app.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New returns a store on pool. The caller owns and closes the pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// run executes fn in a transaction confined to ctx's company.
func (s *Store) run(ctx context.Context, fn func(q *sqlcdb.Queries, sc tenancy.Scope, tx pgx.Tx) error) error {
	sc, _ := tenancy.From(ctx)
	return db.InCompany(ctx, s.pool, func(tx pgx.Tx) error { return fn(sqlcdb.New(tx), sc, tx) })
}

func (s *Store) appendEvent(ctx context.Context, q *sqlcdb.Queries, sc tenancy.Scope, stream, kind, actor string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return q.InsertEvent(ctx, sqlcdb.InsertEventParams{
		ID: newID(), CompanyID: sc.CompanyID, Stream: stream, Kind: kind, Actor: actor,
		Payload: body, OccurredAt: s.now(),
	})
}

func newID() string { return ulid.Make().String() }

// notFound maps "no rows" to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func pgtypeDate(t time.Time) pgtype.Date {
	t = t.UTC()
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

// CompanyIDs lists every company. It reads the companies table, which is
// outside row-level security, so it needs no scope.
func (s *Store) CompanyIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) (err error) {
		ids, err = sqlcdb.New(tx).CompanyIDs(ctx)
		return err
	})
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

// CompanyModules returns the modules switched on for the scope's company.
func (s *Store) CompanyModules(ctx context.Context) ([]string, error) {
	var mods []string
	err := s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) (err error) {
		mods, err = q.CompanyModules(ctx, sc.CompanyID)
		return notFound(err)
	})
	return mods, err
}

// AddUsage adds n to today's (UTC) counter of kind for the scope's company.
func (s *Store) AddUsage(ctx context.Context, kind string, n int64) error {
	return s.run(ctx, func(q *sqlcdb.Queries, sc tenancy.Scope, _ pgx.Tx) error {
		return q.AddUsage(ctx, sqlcdb.AddUsageParams{
			CompanyID: sc.CompanyID, Kind: kind, Day: pgtypeDate(s.now()), N: n,
		})
	})
}

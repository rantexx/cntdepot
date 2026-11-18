// Package edi holds the shipping-line EDI: the stored messages, per-line
// settings and the company's EDI identity. Store calls run under the company
// in the context's tenancy.Scope, enforced by row-level security.
package edi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"depot/internal/db"
	edidb "depot/internal/edi/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

var ErrNotFound = errors.New("not found")

type CompanyEDI = edidb.CompanyEdi

// Store is the EDI part of the database. The pool must connect as depot_app.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) run(ctx context.Context, fn func(q *edidb.Queries, sc tenancy.Scope) error) error {
	sc, _ := tenancy.From(ctx)
	return db.InCompany(ctx, s.pool, func(tx pgx.Tx) error { return fn(edidb.New(tx), sc) })
}

func newID() string { return ulid.Make().String() }

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func newToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)] // bias is negligible for a 36-symbol alphabet over 256
	}
	return string(b), nil
}

// CompanyEDI returns the company's EDI row, creating it with a fresh
// inbound token on first use.
func (s *Store) CompanyEDI(ctx context.Context) (CompanyEDI, error) {
	var out CompanyEDI
	err := s.run(ctx, func(q *edidb.Queries, sc tenancy.Scope) error {
		if sc.CompanyID == "" {
			return tenancy.ErrNoScope
		}
		tok, err := newToken()
		if err != nil {
			return err
		}
		if _, err := q.InsertCompanyEDI(ctx, edidb.InsertCompanyEDIParams{CompanyID: sc.CompanyID, InboundToken: tok}); err != nil {
			return err
		}
		out, err = q.GetCompanyEDI(ctx, sc.CompanyID)
		return err
	})
	return out, err
}

// NextRef returns the company's next interchange reference, nine digits,
// inside the caller's transaction so a rolled-back send does not burn it.
func (s *Store) NextRef(ctx context.Context, q *edidb.Queries) (string, error) {
	sc, ok := tenancy.From(ctx)
	if !ok || sc.CompanyID == "" {
		return "", tenancy.ErrNoScope
	}
	n, err := q.TakeNextRef(ctx, sc.CompanyID)
	if err != nil {
		return "", notFound(err)
	}
	return fmt.Sprintf("%09d", n), nil
}

// InboundCompany returns the company of an inbound token, or "" and ErrNotFound.
func (s *Store) InboundCompany(ctx context.Context, token string) (string, error) {
	var id string
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) (err error) {
		id, err = edidb.New(tx).InboundCompany(ctx, token)
		return err
	})
	if err == nil && id == "" {
		err = ErrNotFound
	}
	return id, err
}

// Companies lists the companies that have EDI set up.
func (s *Store) Companies(ctx context.Context) ([]string, error) {
	var out []string
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = edidb.New(tx).EDICompanies(ctx)
		return err
	})
	return out, err
}

// Package platform is what platform admins do across companies: create a
// company and list them all. Every call is refused unless the signed-in user
// in the context is a platform admin.
package platform

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"depot/internal/auth"
	"depot/internal/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

var (
	ErrNotAdmin = errors.New("platform: platform admins only")
	ErrInvalid  = errors.New("platform: a company needs a name and a valid owner email")
)

// Company is one customer company of the platform.
type Company struct {
	ID, Name, Country string
	Modules           []string
}

func requireAdmin(ctx context.Context) (*auth.Current, error) {
	cur, ok := auth.CurrentFrom(ctx)
	if !ok || !cur.PlatformAdmin {
		return nil, ErrNotAdmin
	}
	return cur, nil
}

// CreateCompany creates a company with a default depot "main" and an owner
// invitation for ownerEmail, all in one transaction.
func CreateCompany(ctx context.Context, pool *pgxpool.Pool, name, country, ownerEmail string, modules []string) (string, error) {
	cur, err := requireAdmin(ctx)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	email := strings.ToLower(strings.TrimSpace(ownerEmail))
	if a, err := mail.ParseAddress(email); name == "" || err != nil || a.Address != email {
		return "", ErrInvalid
	}
	if modules == nil {
		modules = []string{}
	}
	id := ulid.Make().String()
	err = db.Global(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO companies (id, name, country, modules) VALUES ($1, $2, $3, $4)",
			id, name, strings.TrimSpace(country), modules); err != nil {
			return err
		}
		// From here on, row-level security confines the transaction to the new company.
		if _, err := tx.Exec(ctx, "SELECT set_config('app.company_id', $1, true)", id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "INSERT INTO depots (company_id, id, name, country) VALUES ($1, 'main', 'Main', $2)",
			id, strings.TrimSpace(country)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by)
			VALUES ($1, $2, $3, 'owner', NULL, $4)`, id, ulid.Make().String(), email, cur.User.ID)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("platform: create company: %w", err)
	}
	return id, nil
}

// ListCompanies returns every company by name.
func ListCompanies(ctx context.Context, pool *pgxpool.Pool) ([]Company, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	var list []Company
	err := db.Global(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT id, name, country, modules FROM companies ORDER BY name, id")
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (c Company, err error) {
			err = r.Scan(&c.ID, &c.Name, &c.Country, &c.Modules)
			return c, err
		})
		return err
	})
	return list, err
}

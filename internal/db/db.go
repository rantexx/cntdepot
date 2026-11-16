// Package db owns the PostgreSQL schema, its migrations and the app role.
// The app connects as depot_app, which row-level security confines to the
// company set by InCompany.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// AppRole is the role the app connects as: no superuser, no BYPASSRLS, owns nothing.
const AppRole = "depot_app"

type MigrateOptions struct {
	AppPassword        string
	PlatformAdminEmail string // added to platform_admins when non-empty
}

// Migrate connects as the database owner, creates or updates the app role
// and applies the embedded migrations not yet in schema_version. It is
// idempotent and safe to run concurrently against one database.
func Migrate(ctx context.Context, ownerURL string, o MigrateOptions) error {
	if o.AppPassword == "" {
		return errors.New("db: migrate: empty app password")
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	defer conn.Close(ctx)
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// Serializes migrators of this database.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7461001)"); err != nil {
			return err
		}
		// The role is cluster-wide: another database may create it at the same time.
		if _, err := tx.Exec(ctx, `DO $$ BEGIN
			CREATE ROLE depot_app;
		EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL;
		END $$`); err != nil {
			return fmt.Errorf("create role: %w", err)
		}
		var alter string
		if err := tx.QueryRow(ctx, "SELECT format('ALTER ROLE depot_app WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD %L', $1::text)", o.AppPassword).Scan(&alter); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, alter); err != nil {
			return fmt.Errorf("alter role: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
			version    INT PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, f := range files {
			name := f.Name()
			v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
			if err != nil {
				return fmt.Errorf("migration %s: no version prefix", name)
			}
			var done bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_version WHERE version = $1)", v).Scan(&done); err != nil {
				return err
			}
			if done {
				continue
			}
			sql, err := migrations.ReadFile(path.Join("migrations", name))
			if err != nil {
				return err
			}
			// No arguments: simple protocol, so a file may hold many statements.
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_version (version, name) VALUES ($1, $2)", v, name); err != nil {
				return err
			}
		}
		if o.PlatformAdminEmail != "" {
			if _, err := tx.Exec(ctx, "INSERT INTO platform_admins (email) VALUES (lower($1)) ON CONFLICT DO NOTHING", strings.TrimSpace(o.PlatformAdminEmail)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("db: migrate: %w", err)
	}
	return nil
}

// unusableURL is what AppURL returns for an unparseable owner URL. "" would
// let pgx fall back to PG* env vars and the OS user, possibly a superuser.
const unusableURL = "postgres://depot_app@%invalid-owner-url"

// AppURL returns ownerURL (a postgres:// URL) with the user set to depot_app.
// For an unparseable ownerURL it returns a URL that Open always refuses.
func AppURL(ownerURL, password string) string {
	u, err := url.Parse(ownerURL)
	if err != nil {
		return unusableURL
	}
	u.User = url.UserPassword(AppRole, password)
	return u.String()
}

// Open connects a pool as the app role and refuses any other role, since
// an owner or superuser would bypass row-level security.
func Open(ctx context.Context, appURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	var ok bool
	err = pool.QueryRow(ctx, "SELECT current_user = '"+AppRole+"' AND NOT rolsuper AND NOT rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&ok)
	if err == nil && !ok {
		err = errors.New("connected role is not " + AppRole + ", or can bypass row-level security")
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: open: %w", err)
	}
	return pool, nil
}

// InCompany runs fn in a transaction confined by RLS to the company in
// ctx's tenancy scope. Without one it returns tenancy.ErrNoScope.
func InCompany(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	s, ok := tenancy.From(ctx)
	if !ok || s.CompanyID == "" {
		return tenancy.ErrNoScope
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// is_local=true: the setting ends with the transaction, so pooled
		// connections never carry it over.
		if _, err := tx.Exec(ctx, "SELECT set_config('app.company_id', $1, true)", s.CompanyID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Global runs fn in a transaction with no company set, for the tables
// outside any company. Company tables show no rows there.
func Global(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}

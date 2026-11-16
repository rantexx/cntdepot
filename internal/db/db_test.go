package db_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

const appPassword = "app-secret"

var ctx = context.Background()

func scope(company string) context.Context {
	return tenancy.With(ctx, tenancy.Scope{CompanyID: company, Role: tenancy.Owner})
}

// setup migrates a fresh database with companies A and B and returns the
// owner URL and a depot_app pool.
func setup(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.NewDB(t)
	if err := db.Migrate(ctx, owner, db.MigrateOptions{AppPassword: appPassword}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, db.AppURL(owner, appPassword))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec(t, pool, db.Global, "INSERT INTO companies (id, name) VALUES ('A', 'Alpha'), ('B', 'Beta')")
	return owner, pool
}

func exec(t *testing.T, pool *pgxpool.Pool, run func(context.Context, *pgxpool.Pool, func(pgx.Tx) error) error, sql string, args ...any) {
	t.Helper()
	execIn(t, ctx, pool, run, sql, args...)
}

func execIn(t *testing.T, c context.Context, pool *pgxpool.Pool, run func(context.Context, *pgxpool.Pool, func(pgx.Tx) error) error, sql string, args ...any) {
	t.Helper()
	if err := run(c, pool, func(tx pgx.Tx) error { _, err := tx.Exec(c, sql, args...); return err }); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func count(t *testing.T, c context.Context, pool *pgxpool.Pool, run func(context.Context, *pgxpool.Pool, func(pgx.Tx) error) error, sql string) int {
	t.Helper()
	var n int
	if err := run(c, pool, func(tx pgx.Tx) error { return tx.QueryRow(c, sql).Scan(&n) }); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestMigrateIsIdempotent(t *testing.T) {
	owner := pgtest.NewDB(t)
	o := db.MigrateOptions{AppPassword: appPassword, PlatformAdminEmail: "Admin@Example.com"}
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, owner, o); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
	conn, err := pgx.Connect(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var versions int
	var admin string
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM schema_version), (SELECT string_agg(email, ',') FROM platform_admins)").Scan(&versions, &admin); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || admin != "admin@example.com" {
		t.Fatalf("schema_version rows %d, platform_admins %q", versions, admin)
	}
}

func TestAppRoleCannotBypassRLS(t *testing.T) {
	_, pool := setup(t)
	var bypass, super bool
	err := db.Global(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT rolbypassrls, rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&bypass, &super)
	})
	if err != nil || bypass || super {
		t.Fatalf("bypassrls=%v super=%v err=%v", bypass, super, err)
	}
}

func TestCompanyRowsInvisibleAcrossCompanies(t *testing.T) {
	_, pool := setup(t)
	a, b := scope("A"), scope("B")
	execIn(t, a, pool, db.InCompany, "INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'l1', 'MSC', 'MSC')")
	if n := count(t, a, pool, db.InCompany, "SELECT count(*) FROM shipping_lines"); n != 1 {
		t.Fatalf("company A sees %d rows, want 1", n)
	}
	if n := count(t, b, pool, db.InCompany, "SELECT count(*) FROM shipping_lines"); n != 0 {
		t.Fatalf("company B sees %d rows", n)
	}
	var updated int64
	if err := db.InCompany(b, pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(b, "UPDATE shipping_lines SET name = 'x'")
		updated = tag.RowsAffected()
		return err
	}); err != nil || updated != 0 {
		t.Fatalf("company B updated %d rows, err %v", updated, err)
	}
	err := db.InCompany(b, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(b, "INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'l2', 'CMA', 'CMA')")
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-company insert: %v, want 42501", err)
	}
}

func TestInCompanyNeedsScope(t *testing.T) {
	_, pool := setup(t)
	for _, c := range []context.Context{ctx, scope("")} {
		if err := db.InCompany(c, pool, func(pgx.Tx) error { return nil }); !errors.Is(err, tenancy.ErrNoScope) {
			t.Fatalf("got %v, want ErrNoScope", err)
		}
	}
}

func TestMissingScopeSeesNothing(t *testing.T) {
	_, pool := setup(t)
	execIn(t, scope("A"), pool, db.InCompany, "INSERT INTO shipping_lines (company_id, id, code, name) VALUES ('A', 'l1', 'MSC', 'MSC')")
	if n := count(t, ctx, pool, db.Global, "SELECT count(*) FROM shipping_lines"); n != 0 {
		t.Fatalf("no scope sees %d rows", n)
	}
}

func TestAcceptInvitationsByEmail(t *testing.T) {
	_, pool := setup(t)
	exec(t, pool, db.Global, "INSERT INTO users (id, email) VALUES ('u1', 'ali@example.com')")
	execIn(t, scope("A"), pool, db.InCompany, "INSERT INTO invitations (company_id, id, email, role, created_by) VALUES ('A', 'i1', 'Ali@Example.com', 'gate', 'u0')")
	var accepted int
	if err := db.Global(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT accept_invitations('u1', 'ali@example.com')").Scan(&accepted)
	}); err != nil || accepted != 1 {
		t.Fatalf("accepted %d, err %v", accepted, err)
	}
	var company, name, role string
	if err := db.Global(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT company_id, company_name, role FROM memberships_for_user('u1')").Scan(&company, &name, &role)
	}); err != nil || company != "A" || name != "Alpha" || role != "gate" {
		t.Fatalf("membership %s %s %s, err %v", company, name, role, err)
	}
	if n := count(t, ctx, pool, db.Global, "SELECT accept_invitations('u1', 'ali@example.com')"); n != 0 {
		t.Fatalf("invitation accepted twice: %d", n)
	}
}

// TestSecurityInvariants guards the schema as a whole, so a later
// migration cannot add a company table without RLS.
func TestSecurityInvariants(t *testing.T) {
	owner, _ := setup(t)
	conn, err := pgx.Connect(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var bad []string
	rows, err := conn.Query(ctx, `
		SELECT c.relname || ': ' || CASE
			WHEN pg_get_userbyid(c.relowner) = 'depot_app' THEN 'owned by depot_app'
			WHEN NOT c.relrowsecurity THEN 'RLS off'
			ELSE 'no policy' END
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		WHERE c.relkind = 'r' AND (
			pg_get_userbyid(c.relowner) = 'depot_app' OR
			(EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'company_id' AND NOT a.attisdropped)
			 AND c.relname <> 'sessions'
			 AND (NOT c.relrowsecurity OR NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid))))
		UNION ALL
		SELECT p.proname || ': ' || CASE
			WHEN p.proconfig IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(p.proconfig) s WHERE s LIKE 'search_path=%') THEN 'no search_path'
			ELSE 'PUBLIC can execute' END
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname = 'public'
		WHERE p.prosecdef AND (
			p.proconfig IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(p.proconfig) s WHERE s LIKE 'search_path=%')
			OR p.proacl IS NULL OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) x WHERE x.grantee = 0))`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		bad = append(bad, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatal(strings.Join(bad, "\n"))
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_proc WHERE prosecdef AND pronamespace = 'public'::regnamespace").Scan(&n); err != nil || n != 3 {
		t.Fatalf("security definer functions %d, err %v", n, err)
	}
}

func TestSetModulesOnlyOwnCompany(t *testing.T) {
	_, pool := setup(t)
	var n int
	err := db.InCompany(scope("A"), pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT set_company_modules('{ocr}')").Scan(&n)
	})
	if err != nil || n != 1 {
		t.Fatalf("set_company_modules under A = %d, %v; want 1 row", n, err)
	}
	if got := count(t, ctx, pool, db.Global, "SELECT count(*) FROM companies WHERE modules = '{ocr}'"); got != 1 {
		t.Fatalf("%d companies have ocr; want only A", got)
	}
	// Without a scope it changes nothing.
	err = db.Global(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT set_company_modules('{x}')").Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("set_company_modules without scope = %d, %v; want 0 rows", n, err)
	}
	// depot_app itself may not update companies.
	err = db.InCompany(scope("A"), pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE companies SET modules = '{}' WHERE id = 'B'")
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("direct UPDATE companies = %v; want permission denied", err)
	}
}

func TestOpenRefusesNonAppRole(t *testing.T) {
	owner := pgtest.NewDB(t)
	pool, err := db.Open(ctx, owner)
	if err == nil {
		pool.Close()
		t.Fatal("Open accepted the superuser URL")
	}
	if !strings.Contains(err.Error(), "depot_app") {
		t.Fatalf("error %q does not mention depot_app", err)
	}
}

func TestAppURLUnparseableNeverConnects(t *testing.T) {
	app := db.AppURL("postgres://postgres:pa%ss@127.0.0.1:5432/x", appPassword)
	if app == "" {
		t.Fatal("AppURL returned an empty URL, which pgx reads as PG* env defaults")
	}
	pool, err := db.Open(ctx, app)
	if err == nil {
		pool.Close()
		t.Fatalf("Open(%q) connected", app)
	}
}

func TestOpenRefusesAnOverPoweredAppRole(t *testing.T) {
	owner := pgtest.NewDB(t)
	if err := db.Migrate(ctx, owner, db.MigrateOptions{AppPassword: appPassword}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// The role is cluster-wide: leave it as the other tests expect.
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "ALTER ROLE depot_app NOCREATEROLE LOGIN") })
	// A role left behind with extra power and without LOGIN.
	if _, err := conn.Exec(ctx, "ALTER ROLE depot_app CREATEROLE NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, owner, db.MigrateOptions{AppPassword: appPassword}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(ctx, db.AppURL(owner, appPassword))
	if err == nil {
		pool.Close()
		t.Fatal("Open accepted a depot_app role with CREATEROLE")
	}
	if !strings.Contains(err.Error(), "more power") {
		t.Fatalf("Open failed for another reason (LOGIN not restored?): %v", err)
	}
}

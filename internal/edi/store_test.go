package edi

import (
	"context"
	"errors"
	"os"
	"testing"

	"depot/internal/db"
	edidb "depot/internal/edi/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func ctxOf(c string) context.Context {
	return tenancy.With(context.Background(), tenancy.Scope{CompanyID: c, DepotID: "d1", UserID: "u1", Role: tenancy.Owner})
}

// openTest returns a store on a fresh database with companies A and B, each
// with depot d1 and a shipping line (lA, lB).
func openTest(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.NewDB(t)
	bg := context.Background()
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "app-pw"}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(bg, db.AppURL(owner, "app-pw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, c := range []string{"A", "B"} {
		if err := db.Global(bg, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO companies (id, name) VALUES ($1, $1)`, c)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.InCompany(ctxOf(c), pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(bg, `INSERT INTO depots (company_id, id, name) VALUES ($1, 'd1', 'D1')`, c); err != nil {
				return err
			}
			_, err := tx.Exec(bg, `INSERT INTO shipping_lines (company_id, id, code, name) VALUES ($1, 'l'||$1, 'MSC', 'MSC')`, c)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(pool), pool
}

func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) (n int) {
	t.Helper()
	if err := db.InCompany(ctx, pool, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEDITablesAreCompanyIsolated(t *testing.T) {
	s, pool := openTest(t)
	ctx := ctxOf("A")
	if _, err := s.CompanyEDI(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.InCompany(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO line_edi (company_id, line_id, mode) VALUES ('A', 'lA', 'edi')`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO edi_messages (company_id, id, direction, kind, status) VALUES ('A', 'm1', 'in', 'coparn', 'received')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"edi_messages", "line_edi", "company_edi"} {
		if n := count(t, ctx, pool, "SELECT count(*) FROM "+tbl); n != 1 {
			t.Fatalf("A sees %d %s rows, want 1", n, tbl)
		}
		if n := count(t, ctxOf("B"), pool, "SELECT count(*) FROM "+tbl); n != 0 {
			t.Fatalf("B sees %d %s rows of A", n, tbl)
		}
	}
}

func TestInboundTokenFindsOnlyItsCompany(t *testing.T) {
	s, _ := openTest(t)
	bg := context.Background()
	a, err := s.CompanyEDI(ctxOf("A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CompanyEDI(ctxOf("B"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.InboundToken) != 20 || a.InboundToken == b.InboundToken {
		t.Fatalf("tokens %q %q", a.InboundToken, b.InboundToken)
	}
	if again, _ := s.CompanyEDI(ctxOf("A")); again.InboundToken != a.InboundToken {
		t.Fatal("token changed on second call")
	}
	for c, tok := range map[string]string{"A": a.InboundToken, "B": b.InboundToken} {
		if got, err := s.InboundCompany(bg, tok); err != nil || got != c {
			t.Fatalf("token of %s gave %q, %v", c, got, err)
		}
	}
	if got, err := s.InboundCompany(bg, "nope"); got != "" || !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token gave %q, %v", got, err)
	}
	if cs, err := s.Companies(bg); err != nil || len(cs) != 2 {
		t.Fatalf("companies %v, %v", cs, err)
	}
}

func TestNextRefIsSequentialPerCompany(t *testing.T) {
	s, pool := openTest(t)
	next := func(c string) string {
		t.Helper()
		ctx := ctxOf(c)
		if _, err := s.CompanyEDI(ctx); err != nil {
			t.Fatal(err)
		}
		var ref string
		if err := db.InCompany(ctx, pool, func(tx pgx.Tx) (err error) {
			ref, err = s.NextRef(ctx, edidb.New(tx))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	if a1, a2, b1 := next("A"), next("A"), next("B"); a1 != "000000001" || a2 != "000000002" || b1 != "000000001" {
		t.Fatalf("refs %s %s %s", a1, a2, b1)
	}
}

func TestOneEDIOrderPerLineKindReference(t *testing.T) {
	_, pool := openTest(t)
	ctx := ctxOf("A")
	insert := func(id, source string) error {
		return db.InCompany(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO release_orders (company_id, id, depot_id, kind, reference, line_id, created_by, source)
				VALUES ('A', $1, 'd1', 'release', 'BKG1', 'lA', 'u1', $2)`, id, source)
			return err
		})
	}
	if err := insert("o1", "edi"); err != nil {
		t.Fatal(err)
	}
	var pg *pgconn.PgError
	if err := insert("o2", "edi"); !errors.As(err, &pg) || pg.Code != "23505" {
		t.Fatalf("second edi order: %v, want unique violation", err)
	}
	if err := insert("o3", "manual"); err != nil {
		t.Fatalf("manual order with the same reference: %v", err)
	}
}

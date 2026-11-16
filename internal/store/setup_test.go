package store

import (
	"context"
	"os"
	"testing"

	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// ctx is scoped to company A, depot d1.
var ctx = tenancy.With(context.Background(), scopeOf("A"))

func scopeOf(company string) tenancy.Scope {
	return tenancy.Scope{CompanyID: company, DepotID: "d1", UserID: "u1", Role: tenancy.Owner}
}

// ctxOf returns a context scoped to another company.
func ctxOf(company string) context.Context {
	return tenancy.With(context.Background(), scopeOf(company))
}

// openTest returns a store on a fresh database, with companies A and B (each with depot d1).
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
		err := db.Global(bg, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO companies (id, name, modules) VALUES ($1, $1, '{ocr}')`, c)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.InCompany(ctxOf(c), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, `INSERT INTO depots (company_id, id, name) VALUES ($1, 'd1', 'Depot')`, c)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return New(pool), pool
}

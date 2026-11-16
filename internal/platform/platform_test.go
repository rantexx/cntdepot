package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"depot/internal/auth"
	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

var bg = context.Background()

func open(t *testing.T) (*pgxpool.Pool, *auth.Sessions) {
	t.Helper()
	owner := pgtest.NewDB(t)
	if err := db.Migrate(bg, owner, db.MigrateOptions{AppPassword: "app-pw", PlatformAdminEmail: "admin@x.com"}); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Open(bg, db.AppURL(owner, "app-pw"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, auth.NewSessions(pool, 0)
}

// as returns a context carrying the signed-in user with that verified email.
func as(t *testing.T, s *auth.Sessions, email string) context.Context {
	t.Helper()
	cookie, err := s.Login(bg, auth.Identity{Subject: "t|" + email, Email: email, EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	// The session middleware is the only way to put a signed-in user in a context.
	var ctx context.Context
	r := httptest.NewRequest("GET", "/x", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	s.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	if ctx == nil {
		t.Fatal("session did not resolve")
	}
	return ctx
}

func TestCreateCompanyMakesMainDepotAndInvitesOwner(t *testing.T) {
	pool, s := open(t)
	id, err := CreateCompany(as(t, s, "admin@x.com"), pool, "Acme Depots", "TR", " Boss@X.com ", []string{"ocr"})
	if err != nil {
		t.Fatal(err)
	}
	var depot, email, role string
	var depots []string
	err = db.InCompany(tenancy.With(bg, tenancy.Scope{CompanyID: id}), pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(bg, "SELECT id FROM depots").Scan(&depot); err != nil {
			return err
		}
		return tx.QueryRow(bg, "SELECT email, role, depot_ids FROM invitations WHERE used_at IS NULL").Scan(&email, &role, &depots)
	})
	if err != nil {
		t.Fatal(err)
	}
	if depot != "main" || email != "boss@x.com" || role != "owner" || depots != nil {
		t.Fatalf("depot %q, invitation %q %q %v", depot, email, role, depots)
	}
	list, err := ListCompanies(as(t, s, "admin@x.com"), pool)
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Name != "Acme Depots" || list[0].Country != "TR" || len(list[0].Modules) != 1 {
		t.Fatalf("companies = %+v, %v", list, err)
	}
}

func TestOnlyPlatformAdminsManageCompanies(t *testing.T) {
	pool, s := open(t)
	for _, ctx := range []context.Context{bg, as(t, s, "someone@x.com")} {
		if _, err := CreateCompany(ctx, pool, "X", "TR", "boss@x.com", nil); !errors.Is(err, ErrNotAdmin) {
			t.Errorf("CreateCompany err = %v; want ErrNotAdmin", err)
		}
		if _, err := ListCompanies(ctx, pool); !errors.Is(err, ErrNotAdmin) {
			t.Errorf("ListCompanies err = %v; want ErrNotAdmin", err)
		}
	}
}

func TestCreateCompanyChecksInput(t *testing.T) {
	pool, s := open(t)
	ctx := as(t, s, "admin@x.com")
	for _, tc := range []struct{ name, email string }{{" ", "boss@x.com"}, {"X", "not an email"}} {
		if _, err := CreateCompany(ctx, pool, tc.name, "TR", tc.email, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateCompany(%q, %q) err = %v; want ErrInvalid", tc.name, tc.email, err)
		}
	}
}

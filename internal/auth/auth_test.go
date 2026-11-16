package auth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"depot/internal/db"
	"depot/internal/pgtest"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

var bg = context.Background()

// setup returns sessions on a fresh database with companies A and B (each
// with depots d1, d2) and admin@x.com as platform admin.
func setup(t *testing.T) (*Sessions, *pgxpool.Pool) {
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
	for _, c := range []string{"A", "B"} {
		exec(t, pool, "", `INSERT INTO companies (id, name) VALUES ($1, $1)`, c)
		exec(t, pool, c, `INSERT INTO depots (company_id, id, name) VALUES ($1, 'd1', 'D1'), ($1, 'd2', 'D2')`, c)
	}
	return NewSessions(pool, 0), pool
}

// exec runs sql in company (or globally when company is "").
func exec(t *testing.T, pool *pgxpool.Pool, company, sql string, args ...any) {
	t.Helper()
	fn := func(tx pgx.Tx) error { _, err := tx.Exec(bg, sql, args...); return err }
	var err error
	if company == "" {
		err = db.Global(bg, pool, fn)
	} else {
		err = db.InCompany(tenancy.With(bg, tenancy.Scope{CompanyID: company}), pool, fn)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// invite adds an open invitation; depots nil = all depots.
func invite(t *testing.T, pool *pgxpool.Pool, company, email, role string, depots []string) {
	t.Helper()
	exec(t, pool, company, `INSERT INTO invitations (company_id, id, email, role, depot_ids, created_by)
		VALUES ($1, $2, $3, $4, $5, 'test')`, company, email+role, email, role, depots)
}

func login(t *testing.T, s *Sessions, id Identity) string {
	t.Helper()
	c, err := s.Login(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func resolve(t *testing.T, s *Sessions, cookie string) Current {
	t.Helper()
	cur, err := s.Resolve(bg, cookie)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func verified(subject, email string) Identity {
	return Identity{Subject: subject, Email: email, EmailVerified: true}
}

// get sends GET path through Middleware(h) with the session cookie.
func get(s *Sessions, h http.Handler, path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	s.Middleware(h).ServeHTTP(w, r)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error struct{ Code string } }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return body.Error.Code
}

var ok = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

func TestInvitationMatchesNormalisedVerifiedEmailOnly(t *testing.T) {
	s, pool := setup(t)
	invite(t, pool, "A", "ali@example.com", "office", nil)
	cur := resolve(t, s, login(t, s, verified("x|ali", " Ali@Example.COM ")))
	if cur.User.Email != "ali@example.com" {
		t.Errorf("email = %q", cur.User.Email)
	}
	if len(cur.Memberships) != 1 || cur.Memberships[0].CompanyID != "A" || cur.Memberships[0].Role != tenancy.Office {
		t.Fatalf("memberships = %+v", cur.Memberships)
	}

	invite(t, pool, "B", "veli@example.com", "owner", nil)
	cur = resolve(t, s, login(t, s, Identity{Subject: "x|veli", Email: "veli@example.com"}))
	if len(cur.Memberships) != 0 {
		t.Fatalf("unverified email joined: %+v", cur.Memberships)
	}

	// An unverified email grants nothing else either, and does not block the real owner.
	if cur := resolve(t, s, login(t, s, Identity{Subject: "x|fake", Email: "admin@x.com"})); cur.PlatformAdmin {
		t.Error("unverified email is platform admin")
	}
	if cur := resolve(t, s, login(t, s, verified("x|admin", "Admin@X.com"))); !cur.PlatformAdmin {
		t.Error("verified admin email is not platform admin")
	}
}

func TestRemovedMemberLosesAccessOnNextRequest(t *testing.T) {
	s, pool := setup(t)
	invite(t, pool, "A", "g@x.com", "gate", nil)
	c := login(t, s, verified("x|g", "g@x.com"))
	if err := s.SetScope(bg, c, "A", "d1"); err != nil {
		t.Fatal(err)
	}
	h := Require(PermGate, ok)
	if w := get(s, h, "/api/moves", c); w.Code != http.StatusOK {
		t.Fatalf("member: %d %s", w.Code, w.Body)
	}
	exec(t, pool, "A", `DELETE FROM memberships`)
	w := get(s, h, "/api/moves", c)
	if w.Code != http.StatusConflict || errCode(t, w) != "E4090" {
		t.Fatalf("removed member: %d %s", w.Code, w.Body)
	}
}

func TestDemotedMemberLosesOwnerRights(t *testing.T) {
	s, pool := setup(t)
	invite(t, pool, "A", "o@x.com", "owner", nil)
	c := login(t, s, verified("x|o", "o@x.com"))
	if err := s.SetScope(bg, c, "A", "d1"); err != nil {
		t.Fatal(err)
	}
	h := Require(PermOwner, ok)
	if w := get(s, h, "/api/members", c); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
	exec(t, pool, "A", `UPDATE memberships SET role = 'gate'`)
	w := get(s, h, "/api/members", c)
	if w.Code != http.StatusForbidden || errCode(t, w) != "E4030" {
		t.Fatalf("demoted: %d %s", w.Code, w.Body)
	}
	if cur := resolve(t, s, c); cur.Scope == nil || cur.Scope.Role != tenancy.Gate {
		t.Errorf("scope = %+v", cur.Scope)
	}
}

func TestSetScopeRejectsForeignCompanyAndDepot(t *testing.T) {
	s, pool := setup(t)
	invite(t, pool, "A", "d1@x.com", "office", []string{"d1"})
	c := login(t, s, verified("x|d1", "d1@x.com"))
	for _, bad := range [][2]string{{"B", "d1"}, {"B", ""}, {"A", "d2"}, {"A", ""}, {"", ""}} {
		if err := s.SetScope(bg, c, bad[0], bad[1]); !errors.Is(err, ErrNotMember) {
			t.Errorf("SetScope(%q, %q) = %v", bad[0], bad[1], err)
		}
	}
	if err := s.SetScope(bg, c, "A", "d1"); err != nil {
		t.Fatal(err)
	}
	want := tenancy.Scope{CompanyID: "A", DepotID: "d1", UserID: resolve(t, s, c).User.ID, Role: tenancy.Office}
	if cur := resolve(t, s, c); cur.Scope == nil || *cur.Scope != want {
		t.Errorf("scope = %+v, want %+v", cur.Scope, want)
	}

	// All depots: any depot of the company, or none, but not a missing one.
	invite(t, pool, "A", "all@x.com", "office", nil)
	c = login(t, s, verified("x|all", "all@x.com"))
	for _, d := range []string{"d2", ""} {
		if err := s.SetScope(bg, c, "A", d); err != nil {
			t.Errorf("SetScope(A, %q) = %v", d, err)
		}
	}
	if err := s.SetScope(bg, c, "A", "nope"); !errors.Is(err, ErrNotMember) {
		t.Errorf("SetScope(A, nope) = %v", err)
	}
	if err := s.SetScope(bg, "not-a-session", "A", "d1"); !errors.Is(err, ErrNoSession) {
		t.Errorf("SetScope without session = %v", err)
	}
}

func TestSessionCookieIsStoredHashed(t *testing.T) {
	s, pool := setup(t)
	c := login(t, s, verified("x|h", "h@x.com"))
	if raw, err := base64.RawURLEncoding.DecodeString(c); err != nil || len(raw) != 32 {
		t.Fatalf("cookie %q: %d bytes, %v", c, len(raw), err)
	}
	var rows []string
	err := db.Global(bg, pool, func(tx pgx.Tx) error {
		r, err := tx.Query(bg, `SELECT row_to_json(s)::text FROM sessions s`)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(r, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(c))
	if len(rows) != 1 || strings.Contains(rows[0], c) || !strings.Contains(rows[0], hex.EncodeToString(sum[:])) {
		t.Fatalf("sessions = %v", rows)
	}

	w := httptest.NewRecorder()
	s.SetCookie(w, httptest.NewRequest(http.MethodGet, "/", nil), c)
	ck := w.Result().Cookies()[0]
	if ck.Name != "depot_session" || ck.Value != c || !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode || ck.Secure || ck.MaxAge <= 0 {
		t.Errorf("cookie = %+v", ck)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{}
	w = httptest.NewRecorder()
	s.SetCookie(w, r, "")
	if ck := w.Result().Cookies()[0]; !ck.Secure || ck.MaxAge >= 0 {
		t.Errorf("clearing cookie over HTTPS = %+v", ck)
	}
}

func TestSecureCookiesForcedWhenConfigured(t *testing.T) {
	s := NewSessions(nil, 0)
	cookie := func(value string) *http.Cookie {
		w := httptest.NewRecorder()
		s.SetCookie(w, httptest.NewRequest(http.MethodGet, "http://depot/", nil), value)
		return w.Result().Cookies()[0]
	}
	if cookie("v").Secure || cookie("").Secure {
		t.Error("default: Secure over plain http")
	}
	s.SetSecureCookies(true)
	if !cookie("v").Secure || !cookie("").Secure {
		t.Error("SetSecureCookies(true): set or clearing cookie not Secure over plain http")
	}
}

func TestMiddlewareNeedsLiveSessionForApi(t *testing.T) {
	s, pool := setup(t)
	for _, p := range []string{"/api/moves", "/api"} {
		if w := get(s, ok, p, ""); w.Code != http.StatusUnauthorized || errCode(t, w) != "E4010" {
			t.Errorf("%s without cookie: %d %s", p, w.Code, w.Body)
		}
	}
	for _, p := range []string{"/api/health", "/", "/auth/login"} {
		if w := get(s, ok, p, "bogus"); w.Code != http.StatusOK {
			t.Errorf("%s: %d", p, w.Code)
		}
	}

	c := login(t, s, verified("x|m", "m@x.com"))
	var seen *Current
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur, _ := CurrentFrom(r.Context())
		seen = cur
		if _, ok := tenancy.From(r.Context()); ok {
			t.Error("scope without a company")
		}
	})
	if w := get(s, h, "/api/session", c); w.Code != http.StatusOK || seen == nil || seen.User.Email != "m@x.com" {
		t.Fatalf("logged in: %d %+v", w.Code, seen)
	}
	// Logged in without a company: Require asks to choose one.
	if w := get(s, Require(PermGate, ok), "/api/moves", c); w.Code != http.StatusConflict {
		t.Errorf("no company: %d", w.Code)
	}

	exec(t, pool, "", `UPDATE sessions SET expires_at = now() - interval '1 second'`)
	if w := get(s, ok, "/api/session", c); w.Code != http.StatusUnauthorized {
		t.Errorf("expired: %d", w.Code)
	}

	c = login(t, s, verified("x|m", "m@x.com"))
	if err := s.Logout(bg, c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(bg, c); !errors.Is(err, ErrNoSession) {
		t.Errorf("after logout: %v", err)
	}
}

func TestCanMatrix(t *testing.T) {
	want := map[tenancy.Role][]Perm{
		tenancy.Owner:   {PermGate, PermOffice, PermManage, PermOwner},
		tenancy.Manager: {PermGate, PermOffice, PermManage},
		tenancy.Office:  {PermGate, PermOffice},
		tenancy.Gate:    {PermGate},
		"":              {},
	}
	for role, allowed := range want {
		for _, p := range []Perm{PermGate, PermOffice, PermManage, PermOwner} {
			exp := false
			for _, a := range allowed {
				exp = exp || a == p
			}
			if got := Can(role, p); got != exp {
				t.Errorf("Can(%q, %q) = %v, want %v", role, p, got, exp)
			}
		}
	}
}

func TestDevLoginRefusedInProduction(t *testing.T) {
	if err := CheckDevLogin("production", true); err == nil || !strings.Contains(err.Error(), "dev login") {
		t.Errorf("production + dev login = %v", err)
	}
	if err := CheckDevLogin("production", false); err != nil {
		t.Error(err)
	}
	if err := CheckDevLogin("dev", true); err != nil {
		t.Error(err)
	}
}

func TestDevProvider(t *testing.T) {
	p := NewDevProvider()
	if u := p.LoginURL("a b&c"); u != "/auth/dev?state=a+b%26c" {
		t.Errorf("LoginURL = %q", u)
	}
	id, err := p.Exchange(bg, " Ali@Example.com ")
	if err != nil || id != (Identity{Subject: "dev|ali@example.com", Email: "ali@example.com", EmailVerified: true}) {
		t.Errorf("Exchange = %+v, %v", id, err)
	}
	for _, bad := range []string{"", "ali", "Ali <ali@x.com>", "a@b@c"} {
		if _, err := p.Exchange(bg, bad); err == nil {
			t.Errorf("Exchange(%q) accepted", bad)
		}
	}
}

func TestSignInClearsExpiredSessions(t *testing.T) {
	s, pool := setup(t)
	login(t, s, verified("x|old", "old@x.com"))
	exec(t, pool, "", `UPDATE sessions SET expires_at = now() - interval '1 second'`)
	login(t, s, verified("x|new", "new@x.com"))
	var n int
	if err := db.Global(bg, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(bg, `SELECT count(*) FROM sessions`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sessions after sign-in = %d; want only the new one", n)
	}
}

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"depot/internal/db"
	"depot/internal/tenancy"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// CookieName is the session cookie.
const CookieName = "depot_session"

var (
	ErrNoSession = errors.New("auth: no valid session")
	ErrNotMember = errors.New("auth: not a member of that company or depot")
)

type User struct{ ID, Email, Name string }

// Membership is the user's role in one company; DepotIDs nil means all depots.
type Membership struct {
	CompanyID, CompanyName string
	Role                   tenancy.Role
	DepotIDs               []string
}

// Current is the signed-in user as of this request. Scope is nil until a
// company is chosen, or when the user lost that company or depot.
type Current struct {
	User          User
	Memberships   []Membership
	Scope         *tenancy.Scope
	PlatformAdmin bool
}

// Sessions stores sessions by the SHA-256 of the cookie value, never the value.
type Sessions struct {
	pool   *pgxpool.Pool
	ttl    time.Duration
	secure bool // always mark cookies Secure (behind a TLS-terminating proxy r.TLS is nil)
}

// SetSecureCookies makes every cookie Secure even on plain-http requests;
// turn it on in production. Off by default so local dev over http works.
func (s *Sessions) SetSecureCookies(on bool) { s.secure = on }

// Secure reports whether cookies set in answer to r are marked Secure.
func (s *Sessions) Secure(r *http.Request) bool { return s.secure || r.TLS != nil }

// unverifiedPrefix starts the users.email placeholder of an unverified login.
const unverifiedPrefix = "unverified|"

// NewSessions keeps sessions for ttl (30 days when ttl <= 0).
func NewSessions(pool *pgxpool.Pool, ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &Sessions{pool: pool, ttl: ttl}
}

// Login records the user by provider subject, turns open invitations for a
// verified email into memberships, and returns a new session's cookie value.
func (s *Sessions) Login(ctx context.Context, id Identity) (string, error) {
	if id.Subject == "" {
		return "", errors.New("auth: login: identity has no subject")
	}
	email := normEmail(id.Email)
	verified := id.EmailVerified && email != ""
	stored := email
	if !verified {
		// users.email is what invitations and platform_admins match, so it only
		// ever holds a verified email; '|' keeps the placeholder off real addresses.
		stored = unverifiedPrefix + id.Subject
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	cookie := base64.RawURLEncoding.EncodeToString(raw)
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `INSERT INTO users (id, email, name, idp_subject) VALUES ($1, $2, $3, $4)
			ON CONFLICT (idp_subject) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name
			RETURNING id`, ulid.Make().String(), stored, id.Name, id.Subject).Scan(&userID)
		if err != nil {
			return err
		}
		if verified {
			if _, err := tx.Exec(ctx, "SELECT accept_invitations($1, $2)", userID, email); err != nil {
				return err
			}
		}
		// Each sign-in also clears out expired sessions, so the table cannot grow without bound.
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE expires_at < now()"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO sessions (id_hash, user_id, expires_at) VALUES ($1, $2, $3)",
			hashCookie(cookie), userID, time.Now().Add(s.ttl))
		return err
	})
	if err != nil {
		return "", fmt.Errorf("auth: login: %w", err)
	}
	return cookie, nil
}

// Resolve reads the session's user, memberships and scope afresh, so a
// removed or demoted member loses access on the next request.
func (s *Sessions) Resolve(ctx context.Context, cookie string) (Current, error) {
	var cur Current
	if cookie == "" {
		return cur, ErrNoSession
	}
	var company, depot string
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT u.id, u.email, u.name, coalesce(s.company_id, ''), coalesce(s.depot_id, ''),
				EXISTS (SELECT 1 FROM platform_admins p WHERE p.email = u.email)
			FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE s.id_hash = $1 AND s.expires_at > now()`, hashCookie(cookie)).
			Scan(&cur.User.ID, &cur.User.Email, &cur.User.Name, &company, &depot, &cur.PlatformAdmin)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSession
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT company_id, company_name, role, depot_ids FROM memberships_for_user($1)", cur.User.ID)
		if err != nil {
			return err
		}
		cur.Memberships, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Membership, error) {
			var m Membership
			var role string
			err := r.Scan(&m.CompanyID, &m.CompanyName, &role, &m.DepotIDs)
			m.Role = tenancy.Role(role)
			return m, err
		})
		return err
	})
	if err != nil {
		return Current{}, err
	}
	if m, ok := cur.membership(company, depot); ok {
		cur.Scope = &tenancy.Scope{CompanyID: company, DepotID: depot, UserID: cur.User.ID, Role: m.Role}
	}
	return cur, nil
}

// AcceptInvitations turns the open invitations for cur's email into
// memberships, so an invitation sent after sign-in counts without signing in
// again. It reports how many were accepted.
func (s *Sessions) AcceptInvitations(ctx context.Context, cur *Current) (int, error) {
	if strings.HasPrefix(cur.User.Email, unverifiedPrefix) { // users.email holds only verified emails otherwise
		return 0, nil
	}
	var n int
	err := db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT accept_invitations($1, $2)", cur.User.ID, cur.User.Email).Scan(&n)
	})
	return n, err
}

// SetScope chooses the session's company and depot ("" = all depots, only
// for members of all depots). The user must be a member there.
func (s *Sessions) SetScope(ctx context.Context, cookie, companyID, depotID string) error {
	cur, err := s.Resolve(ctx, cookie)
	if err != nil {
		return err
	}
	if _, ok := cur.membership(companyID, depotID); !ok {
		return ErrNotMember
	}
	if depotID != "" {
		var exists bool
		sc := tenancy.With(ctx, tenancy.Scope{CompanyID: companyID})
		err := db.InCompany(sc, s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM depots WHERE id = $1)", depotID).Scan(&exists)
		})
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotMember
		}
	}
	return db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, `UPDATE sessions SET company_id = $2, depot_id = NULLIF($3, '')
			WHERE id_hash = $1 AND expires_at > now()`, hashCookie(cookie), companyID, depotID)
		if err == nil && t.RowsAffected() == 0 {
			err = ErrNoSession
		}
		return err
	})
}

// Logout deletes the session.
func (s *Sessions) Logout(ctx context.Context, cookie string) error {
	return db.Global(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "DELETE FROM sessions WHERE id_hash = $1", hashCookie(cookie))
		return err
	})
}

// SetCookie sets the session cookie, or clears it when value is "".
func (s *Sessions) SetCookie(w http.ResponseWriter, r *http.Request, value string) {
	maxAge := int(s.ttl / time.Second)
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Secure(r),
	})
}

// membership returns the user's membership in company if it covers depot.
func (c *Current) membership(company, depot string) (Membership, bool) {
	for _, m := range c.Memberships {
		if m.CompanyID == company && company != "" {
			return m, m.DepotIDs == nil || slices.Contains(m.DepotIDs, depot)
		}
	}
	return Membership{}, false
}

func hashCookie(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

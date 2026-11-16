package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"depot/internal/tenancy"
)

type currentKey struct{}

// CurrentFrom returns the signed-in user Middleware put in ctx, if any.
func CurrentFrom(ctx context.Context) (*Current, bool) {
	c, ok := ctx.Value(currentKey{}).(*Current)
	return c, ok
}

// Middleware resolves the session cookie on every request and puts the
// Current user and, when chosen, the scope into the context. Without a
// valid session, /api/* (except /api/health) answers 401.
func (s *Sessions) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cur Current
		err := ErrNoSession
		if c, e := r.Cookie(CookieName); e == nil {
			cur, err = s.Resolve(r.Context(), c.Value)
		}
		if err == nil {
			ctx := context.WithValue(r.Context(), currentKey{}, &cur)
			if cur.Scope != nil {
				ctx = tenancy.With(ctx, *cur.Scope)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		api := (r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/")) && r.URL.Path != "/api/health"
		switch {
		case !errors.Is(err, ErrNoSession):
			log.Printf("auth: resolve session: %v", err)
			writeError(w, http.StatusInternalServerError, "E9000", "internal error; see the hub log")
		case api:
			writeError(w, http.StatusUnauthorized, "E4010", "sign in first")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// Require lets the request through only when the role in the current
// company has permission p.
func Require(p Perm, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "E4010", "sign in first")
			return
		}
		sc, ok := tenancy.From(r.Context())
		if !ok {
			writeError(w, http.StatusConflict, "E4090", "choose a company")
			return
		}
		if !Can(sc.Role, p) {
			writeError(w, http.StatusForbidden, "E4030", "your role does not allow this")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeError answers in the API's error shape, {"error": {"code", "message"}}.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]map[string]string{"error": {"code": code, "message": message}})
}

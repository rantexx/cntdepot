// Package tenancy carries the current company, depot, user and role in the
// request context. The scope comes from the session, never from the client.
package tenancy

import (
	"context"
	"errors"
)

// Role is a user's role within one company.
type Role string

const (
	Owner   Role = "owner"
	Manager Role = "manager"
	Office  Role = "office"
	Gate    Role = "gate"
)

// Scope is who is acting and in which company and depot.
type Scope struct {
	CompanyID, DepotID, UserID string
	Role                       Role
}

var ErrNoScope = errors.New("tenancy: no company in context")

type key struct{}

// With returns ctx carrying s.
func With(ctx context.Context, s Scope) context.Context { return context.WithValue(ctx, key{}, s) }

// From returns the scope carried by ctx, if any.
func From(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(key{}).(Scope)
	return s, ok
}

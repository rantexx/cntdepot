// Package auth signs users in through an identity provider, keeps their
// sessions and decides what their role in the current company allows.
package auth

import "context"

// Identity is who the identity provider says signed in.
type Identity struct {
	Subject, Email, Name string
	EmailVerified        bool
}

// Provider is an identity provider: LoginURL sends the browser to sign in
// and Exchange turns the code it returns into an Identity.
type Provider interface {
	LoginURL(state string) string
	Exchange(ctx context.Context, code string) (Identity, error)
}

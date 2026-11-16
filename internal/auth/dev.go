package auth

import (
	"context"
	"errors"
	"net/mail"
	"net/url"
	"strings"
)

// DevProvider signs in anyone who types an email on its login page. It is
// for development only: CheckDevLogin refuses it in production, and the
// server mounts its page only for this type.
type DevProvider struct{}

// NewDevProvider returns the development identity provider.
func NewDevProvider() Provider { return DevProvider{} }

// LoginURL is the dev login page, which posts an email back as the code.
func (DevProvider) LoginURL(state string) string {
	return "/auth/dev?" + url.Values{"state": {state}}.Encode()
}

// Exchange takes the posted email as the code; the email counts as verified.
func (DevProvider) Exchange(_ context.Context, code string) (Identity, error) {
	email := normEmail(code)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return Identity{}, errors.New("auth: dev login needs a plain email address")
	}
	return Identity{Subject: "dev|" + email, Email: email, EmailVerified: true}, nil
}

// CheckDevLogin refuses dev login in production; call it at startup.
func CheckDevLogin(env string, devLogin bool) error {
	if devLogin && strings.EqualFold(strings.TrimSpace(env), "production") {
		return errors.New("auth: dev login is not allowed in production")
	}
	return nil
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

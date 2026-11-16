package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	"depot/internal/auth"
	"depot/internal/blob"
	"depot/internal/store"
	"depot/internal/thumb"
)

// previewWidth reads the optional ?w= preview width (0 when absent). ok is
// false when the value is not allowed; the error answer has been written.
func previewWidth(w http.ResponseWriter, r *http.Request) (width int, ok bool) {
	q := r.URL.Query().Get("w")
	if q == "" {
		return 0, true
	}
	n, err := strconv.Atoi(q)
	if err != nil || !thumb.Allowed(n) {
		writeError(w, http.StatusBadRequest, "E4006", "w must be one of 160, 320, 640, 1280")
		return 0, false
	}
	return n, true
}

// getBlob serves a stored photo, only when it belongs to a photo of the
// session's company (and depot, when one is chosen).
func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	// Check ?w= first: a bad width costs no lookup and is refused the same
	// way whether or not the blob exists.
	width, ok := previewWidth(w, r)
	if !ok {
		return
	}
	digest := r.PathValue("digest")
	if _, err := s.opts.Store.PhotoByDigest(r.Context(), digest); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "E4042", "no such photo")
			return
		}
		writeServiceError(w, err)
		return
	}
	s.serveBlob(w, digest, width)
}

// serveBlob writes a blob the caller is allowed to see, scaled to width when > 0.
func (s *Server) serveBlob(w http.ResponseWriter, digest string, width int) {
	data, err := s.opts.Blobs.Get(digest)
	switch {
	case errors.Is(err, blob.ErrBadDigest):
		writeError(w, http.StatusBadRequest, "E4005", err.Error())
		return
	case errors.Is(err, blob.ErrNotFound):
		writeError(w, http.StatusNotFound, "E4042", err.Error())
		return
	case err != nil:
		writeServiceError(w, err)
		return
	}
	if width > 0 {
		// Any Scale error, such as an image too large to decode, serves the
		// original: the browser decodes it, not the server.
		if small, err := thumb.Scale(data, width); err == nil {
			data = small
		}
	}
	// Originals are stored exactly as uploaded. Only images are shown inline;
	// anything else downloads, so an uploaded file can never run as a page.
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// ---- sign-in ----

// stateCookie holds the random state of a sign-in in progress, so a callback
// is accepted only in the browser that started it.
const stateCookie = "depot_login_state"

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "sign-in is unavailable", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: "/auth/", MaxAge: 600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	http.Redirect(w, r, s.opts.Provider.LoginURL(state), http.StatusFound)
}

func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.finishLogin(w, r, q.Get("code"), q.Get("state"))
}

// finishLogin checks state, exchanges code for an identity and starts a new
// session in place of the browser's current one.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, code, state string) {
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		http.Error(w, "This sign-in was not started in this browser or has expired. Sign in again.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	id, err := s.opts.Provider.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("httpapi: sign-in exchange: %v", err)
		http.Error(w, "The sign-in was not confirmed. Sign in again.", http.StatusUnauthorized)
		return
	}
	// One browser, one session: the old one ends here, whoever signs in next.
	if old, err := r.Cookie(auth.CookieName); err == nil {
		if err := s.opts.Sessions.Logout(r.Context(), old.Value); err != nil {
			log.Printf("httpapi: sign-in: end old session: %v", err)
			http.Error(w, "Sign-in failed; see the server log.", http.StatusInternalServerError)
			return
		}
	}
	cookie, err := s.opts.Sessions.Login(r.Context(), id)
	switch {
	case isUniqueViolation(err):
		http.Error(w, "This email address is already linked to another sign-in. Ask your administrator to sort it out.", http.StatusConflict)
		return
	case err != nil:
		log.Printf("httpapi: sign-in: %v", err)
		http.Error(w, "Sign-in failed; see the server log.", http.StatusInternalServerError)
		return
	}
	s.opts.Sessions.SetCookie(w, r, cookie)
	http.Redirect(w, r, "/", http.StatusFound)
}

var devPageHTML = template.Must(template.New("dev").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Development sign-in</title>
<form method="post" action="/auth/dev" style="font: 16px system-ui; max-width: 22rem; margin: 4rem auto">
<h1 style="font-size: 1.25rem">Development sign-in</h1>
<p>Any email address signs in. This page exists only in development.</p>
<label>Email<br><input name="email" type="email" required autofocus style="width: 100%; font: inherit"></label>
<input type="hidden" name="state" value="{{.}}">
<p><button style="font: inherit">Sign in</button></p>
</form></html>
`))

// devPage is the dev provider's login page: one email field.
func (s *Server) devPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = devPageHTML.Execute(w, r.URL.Query().Get("state"))
}

// devLogin takes the posted email as the dev provider's code.
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	s.finishLogin(w, r, r.PostFormValue("email"), r.PostFormValue("state"))
}

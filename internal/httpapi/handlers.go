package httpapi

import (
	"crypto/subtle"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/iso6346"
	"depot/internal/store"
	"depot/internal/thumb"
)

const maxUpload = 60 << 20 // one original phone photo plus its AI copy

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	name, model := s.opts.Service.Engine()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": s.opts.Version, "engine": name, "model": model, "auth_required": s.token != "",
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if s.token == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	client := clientAddr(r)
	if wait := s.lockout.begin(client, s.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "E5003", "too many wrong codes; wait a minute")
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Code), []byte(s.opts.AccessCode)) != 1 {
		writeError(w, http.StatusUnauthorized, "E5002", "wrong access code")
		return
	}
	s.lockout.clear(client)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) createMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lane   string `json:"lane"`
		IsTest bool   `json:"is_test"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	lane := strings.TrimSpace(body.Lane)
	if lane == "" {
		lane = "1"
	}
	if len([]rune(lane)) > 32 {
		writeError(w, http.StatusBadRequest, "E4002", "lane name too long")
		return
	}
	m, err := s.opts.Service.NewMove(r.Context(), lane, body.IsTest, "clerk")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s.broker.Publish(m.ID)
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) listMoves(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	moves, err := s.opts.Store.ListMoves(r.Context(), store.ListFilter{
		Status: q.Get("status"), Source: q.Get("source"), Limit: limit, Before: q.Get("before"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moves": moves})
}

type moveDetail struct {
	store.Move
	Attempts []store.Attempt `json:"attempts"`
}

func (s *Server) getMove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.opts.Store.GetMove(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	attempts, err := s.opts.Store.ListAttempts(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, moveDetail{Move: m, Attempts: attempts})
}

func (s *Server) addPhoto(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
		return
	}
	original, originalType, err := formFile(r, "original")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing original photo")
		return
	}
	ai, aiType, err := formFile(r, "ai")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing AI copy of the photo")
		return
	}
	width, _ := strconv.Atoi(r.FormValue("width"))
	height, _ := strconv.Atoi(r.FormValue("height"))
	p, err := s.opts.Service.AddPhoto(r.Context(), gate.PhotoInput{
		MoveID: r.PathValue("id"), Role: r.FormValue("role"), Actor: "clerk",
		Original: original, OriginalType: originalType, AI: ai, AIType: aiType, Width: width, Height: height,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, p)
}

// formFile reads one uploaded file and sniffs its type from the bytes,
// never trusting the browser's label.
func formFile(r *http.Request, field string) ([]byte, string, error) {
	f, _, err := r.FormFile(field)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", err
	}
	return data, http.DetectContentType(data), nil
}

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

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	var body gate.Resolution
	if !decodeJSON(w, r, &body) {
		return
	}
	m, err := s.opts.Service.Resolve(r.Context(), r.PathValue("id"), body)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) reread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.opts.Service.Reread(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	m, err := s.opts.Store.GetMove(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, m)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	data, err := s.opts.Blobs.Get(r.PathValue("digest"))
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
	width, ok := previewWidth(w, r)
	if !ok {
		return
	}
	if width > 0 {
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

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, iso6346.Validate(r.PathValue("number")))
}

func (s *Server) testRead(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "E4003", "invalid upload: "+err.Error())
		return
	}
	ai, aiType, err := formFile(r, "ai")
	if err != nil {
		writeError(w, http.StatusBadRequest, "E4004", "missing photo")
		return
	}
	res, err := s.opts.Service.TestRead(r.Context(), ai, aiType)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

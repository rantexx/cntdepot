// Package httpapi serves the hub's JSON API, live updates and the web app.
package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/store"
)

// Options wires the server to the rest of the hub.
type Options struct {
	Service    *gate.Service
	Store      *store.Store
	Blobs      *blob.Store
	Static     fs.FS  // built web app; nil serves a "not built" notice
	AccessCode string // empty: no code required
	Version    string
}

// Server is the hub's HTTP front door.
type Server struct {
	opts   Options
	broker *Broker
	mux    *http.ServeMux
	token  string // cookie value proving the access code was entered
}

const cookieName = "depot_access"

// New builds the server. broker must be the one the gate service notifies.
func New(opts Options, broker *Broker) *Server {
	s := &Server{opts: opts, broker: broker, mux: http.NewServeMux()}
	if opts.AccessCode != "" {
		sum := sha256.Sum256([]byte("depot-kit-v1:" + opts.AccessCode))
		s.token = hex.EncodeToString(sum[:])
	}
	s.routes()
	return s
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler { return s.requireAccess(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("POST /api/login", s.login)
	s.mux.HandleFunc("POST /api/moves", s.createMove)
	s.mux.HandleFunc("GET /api/moves", s.listMoves)
	s.mux.HandleFunc("GET /api/moves/{id}", s.getMove)
	s.mux.HandleFunc("POST /api/moves/{id}/photos", s.addPhoto)
	s.mux.HandleFunc("POST /api/moves/{id}/resolve", s.resolve)
	s.mux.HandleFunc("POST /api/moves/{id}/reread", s.reread)
	s.mux.HandleFunc("GET /api/blobs/{digest}", s.getBlob)
	s.mux.HandleFunc("GET /api/validate/{number}", s.validate)
	s.mux.HandleFunc("POST /api/test-read", s.testRead)
	s.mux.HandleFunc("GET /api/stream", s.stream)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "E4040", "no such API endpoint")
	})
	s.mux.Handle("/", s.static())
}

// requireAccess enforces the optional access code on the API. The app shell,
// health and login stay open so the login screen can load.
func (s *Server) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if s.token == "" || !strings.HasPrefix(p, "/api/") || p == "/api/login" || p == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "E5001", "access code required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// static serves the built web app; unknown paths get index.html because the
// app routes on the URL hash.
func (s *Server) static() http.Handler {
	if s.opts.Static == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The web app is not built yet. Run: npm --prefix web run build, then rebuild depotd.\n"))
		})
	}
	files := s.opts.Static
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(files, name); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		r = r.Clone(r.Context())
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

// writeServiceError maps service errors onto HTTP answers.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gate.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "E4041", "move not found")
	case errors.Is(err, gate.ErrClosed):
		writeError(w, http.StatusConflict, "E4090", err.Error())
	case errors.Is(err, gate.ErrBadAction), errors.Is(err, gate.ErrNumberRequired), errors.Is(err, gate.ErrBadPhoto):
		writeError(w, http.StatusBadRequest, "E4000", err.Error())
	default:
		log.Printf("httpapi: %v", err)
		writeError(w, http.StatusInternalServerError, "E9000", "internal error; see the hub log")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "E4001", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// Package httpapi serves the JSON API, live updates, sign-in and the web app.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"depot/internal/api"
	"depot/internal/auth"
	"depot/internal/blob"
	"depot/internal/gate"
	"depot/internal/gatecam"
	"depot/internal/live"
	"depot/internal/store"
	"depot/internal/tenancy"
	"depot/internal/yard"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options wires the server to the rest of the program.
type Options struct {
	Service  *gate.Service
	Store    *store.Store
	Blobs    *blob.Store
	Static   fs.FS // built web app; nil serves a "not built" notice
	Version  string
	Camera   *gatecam.Manager // nil: no camera support (camera endpoints answer 503)
	Live     *live.Supervisor // nil: no live video
	Sessions *auth.Sessions
	Provider auth.Provider
	Pool     *pgxpool.Pool
	Yard     *yard.Service // nil: no public EIR page, and the gate and yard operations must not be called
	Hub      bool          // the on-site box: camera routes are mounted only here
	// PublicHosts are the domain names the cloud server is reached by (for
	// example app.example.com); the API also answers to them.
	PublicHosts []string
}

// Server is the HTTP front door.
type Server struct {
	opts    Options
	broker  *Broker
	mux     *http.ServeMux
	machine string // this computer's name; the API answers to it as a Host

	stillsMu sync.Mutex
	stills   map[string]bool // digests of camera stills taken by "read this frame"
}

const (
	// Every API request that is not a GET must carry this header. Browsers
	// only let a page add it to same-origin requests, so another website
	// cannot make a visitor's browser change gate records.
	clientHeader = "X-Depot-Client"
	clientValue  = "web"
	// scopeHeader is "<companyId>:<depotId>" as the sending tab shows it. It
	// only checks that the tab is up to date; the scope comes from the session.
	scopeHeader = "X-Depot-Scope"
)

// New builds the server. broker must be the one the gate service notifies.
func New(opts Options, broker *Broker) *Server {
	s := &Server{opts: opts, broker: broker, mux: http.NewServeMux(), stills: map[string]bool{}}
	s.machine, _ = os.Hostname()
	s.routes()
	return s
}

// Handler returns the full HTTP handler.
func (s *Server) Handler() http.Handler { return s.guardAPI(s.mux) }

func (s *Server) routes() {
	apiMux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(s, []api.StrictMiddlewareFunc{s.access}, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if tooBig := new(http.MaxBytesError); errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge, "E4130", fmt.Sprintf("the request body is over %d bytes", tooBig.Limit))
				return
			}
			writeError(w, http.StatusBadRequest, "E4001", "invalid request: "+err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) { writeServiceError(w, err) },
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:  apiMux,
		Middlewares: []api.MiddlewareFunc{checkBody},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, http.StatusBadRequest, "E4001", "invalid parameter: "+err.Error())
		},
	})
	gateOnly := func(h http.HandlerFunc) http.Handler { return auth.Require(auth.PermGate, h) }
	manage := func(h http.HandlerFunc) http.Handler { return auth.Require(auth.PermManage, h) }
	apiMux.Handle("GET /api/stream", gateOnly(s.stream))
	apiMux.Handle("GET /api/blobs/{digest}", gateOnly(s.getBlob))
	if s.opts.Hub {
		apiMux.Handle("GET /api/camera", manage(s.getCamera))
		apiMux.Handle("PUT /api/camera", manage(s.putCamera))
		apiMux.Handle("POST /api/camera/test", manage(s.testCamera))
		apiMux.Handle("POST /api/camera/read", manage(s.readCamera))
		apiMux.Handle("POST /api/camera/capture", manage(s.captureCamera))
		apiMux.Handle("GET /api/camera/still", manage(s.cameraStill))
		apiMux.Handle("GET /api/camera/stills/{digest}", manage(s.cameraStillByDigest))
		apiMux.Handle("GET /api/live/ws", manage(s.liveWS))
	} else {
		apiMux.Handle("GET /api/camera", manage(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "reason": "needs_onsite_box"})
		}))
	}
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "E4040", "no such API endpoint")
	})
	apiMux.HandleFunc("GET /auth/login", s.authLogin)
	apiMux.HandleFunc("GET /auth/callback", s.authCallback)
	if _, dev := s.opts.Provider.(auth.DevProvider); dev {
		apiMux.HandleFunc("GET /auth/dev", s.devPage)
		apiMux.HandleFunc("POST /auth/dev", s.devLogin)
	}

	// Only the API and sign-in need sessions (and so the database): the app
	// shell must load while the database is down.
	withSession := s.opts.Sessions.Middleware(checkScope(apiMux))
	s.mux.Handle("/api/", withSession)
	s.mux.Handle("/auth/", withSession)
	if s.opts.Yard != nil { // public: the link token is the only credential, so no session and no API guard
		s.mux.HandleFunc("GET /eir/{token}", s.eirPage)
		s.mux.HandleFunc("GET /eir/{token}/photo/{photoId}", s.eirPhoto)
	}
	s.mux.Handle("/", s.static())
}

// guardAPI protects the API from other websites open in a visitor's browser:
// requests must be addressed to this computer (a DNS-rebinding page arrives
// under its own name) and writes must carry the web app's client header (a
// cross-site form or fetch cannot add it). The app files are not restricted.
func (s *Server) guardAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !allowedHost(r.Host, s.machine) && !publicHost(r.Host, s.opts.PublicHosts) {
			writeError(w, http.StatusForbidden, "E5005", "unknown host")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get(clientHeader) != clientValue {
			writeError(w, http.StatusForbidden, "E5004", "missing client header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkScope refuses a request from a tab that still shows a company or depot
// the session has since left (another tab of the browser switched it), so its
// writes cannot land in the wrong depot. Requests without the header (other
// clients) and the session's own endpoints are not checked.
func checkScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shown := r.Header.Get(scopeHeader)
		if shown != "" && r.URL.Path != "/api/session" && !strings.HasPrefix(r.URL.Path, "/api/session/") {
			sc, _ := tenancy.From(r.Context())
			if shown != sc.CompanyID+":"+sc.DepotID {
				writeError(w, http.StatusConflict, "E4097", "your company or depot changed in another tab")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// publicHost reports whether a Host header names one of the configured
// public domain names (any case, with or without a port or trailing dot).
func publicHost(hostHeader string, public []string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, p := range public {
		if p = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(p)), "."); p != "" && host == p {
			return true
		}
	}
	return false
}

// allowedHost reports whether a Host header names this computer: localhost,
// an IP address, or the machine's own name (any case), either exactly as the
// system reports it or followed by a local-network suffix such as .local.
// Arbitrary domains after the name are refused: an attacker controls those
// (DNS rebinding).
func allowedHost(hostHeader, machine string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || net.ParseIP(host) != nil {
		return true
	}
	full := strings.ToLower(machine)
	short, _, _ := strings.Cut(full, ".")
	if short == "" {
		return false
	}
	if host == full || host == short {
		return true
	}
	for _, suffix := range localSuffixes {
		if host == short+suffix {
			return true
		}
	}
	return false
}

// localSuffixes are the names home and office routers give LAN computers.
var localSuffixes = []string{".local", ".lan", ".home", ".localdomain", ".home.arpa"}

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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

// apiError is an answer a handler chose; writeServiceError writes it as is.
type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string { return e.message }

func fail(status int, code, message string) error {
	return &apiError{status: status, code: code, message: message}
}

// writeServiceError maps errors onto HTTP answers.
func writeServiceError(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		writeError(w, ae.status, ae.code, ae.message)
	case errors.Is(err, tenancy.ErrNoScope):
		writeError(w, http.StatusConflict, "E4090", "choose a depot first")
	case errors.Is(err, gate.ErrOCROff):
		writeError(w, http.StatusConflict, "E4094", "the OCR module is off for this company: type the container number instead")
	case errors.Is(err, gate.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "E4041", "move not found")
	case errors.Is(err, gate.ErrClosed):
		writeError(w, http.StatusConflict, "E4090", err.Error())
	case errors.Is(err, gate.ErrBadAction), errors.Is(err, gate.ErrNumberRequired), errors.Is(err, gate.ErrBadPhoto):
		writeError(w, http.StatusBadRequest, "E4000", err.Error())
	case yardError(err) != nil:
		ae = yardError(err)
		writeError(w, ae.status, ae.code, ae.message)
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

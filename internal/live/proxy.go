package live

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// Handler proxies the browser's live-video WebSocket to go2rtc. The path and
// stream name are fixed here, so no other part of go2rtc's API can be reached
// through the hub. Host and Origin are rewritten to go2rtc's own address so
// its WebSocket origin check passes. go2rtc's password is added, and the
// hub's cookie is not passed on.
//
// Because Origin is rewritten, the check that go2rtc would have made happens
// here: browsers do not apply CORS to WebSockets, so without it any website
// open on a phone or PC that can reach the hub could watch the camera.
func (s *Supervisor) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "E5006", "cross-site live video request refused")
			return
		}
		addr, user, pass := s.target()
		if addr == "" {
			writeNotRunning(w)
			return
		}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL = &url.URL{Scheme: "http", Host: addr, Path: "/api/ws", RawQuery: "src=gate"}
				pr.Out.Host = addr
				pr.Out.Header.Set("Origin", "http://"+addr)
				pr.Out.Header.Del("Cookie")
				pr.Out.SetBasicAuth(user, pass)
			},
			ModifyResponse: func(res *http.Response) error {
				// go2rtc's password is the hub's business: a browser must
				// never offer a login prompt for it.
				res.Header.Del("Www-Authenticate")
				return nil
			},
			// go2rtc stopped or is restarting. The player retries, so this is
			// not logged.
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { writeNotRunning(w) },
		}
		proxy.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether a request comes from the hub's own web app: a
// browser names the page's origin, which must be the address the request was
// sent to. Requests without Origin come from programs, not web pages.
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) > 1 {
		return false
	}
	u, err := url.Parse(origins[0])
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func writeNotRunning(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "E5030", "live video is not running")
}

// writeError answers in the hub's API error format.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]map[string]string{"error": {"code": code, "message": message}})
}

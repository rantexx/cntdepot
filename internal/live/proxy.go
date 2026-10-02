package live

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// Handler proxies the browser's live-video WebSocket to go2rtc. The path and
// stream name are fixed here, so no other part of go2rtc's API can be reached
// through the hub. Host and Origin are rewritten to go2rtc's own address so
// its WebSocket origin check passes. go2rtc's password is added, and the
// hub's cookie is not passed on.
func (s *Supervisor) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, user, pass := s.target()
		if addr == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"E5030","message":"live video is not running"}}` + "\n"))
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
		}
		proxy.ServeHTTP(w, r)
	})
}

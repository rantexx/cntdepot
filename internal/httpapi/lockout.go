package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginMaxTries = 5                // wrong codes allowed per window
	loginWindow   = 10 * time.Minute // how long a wrong code is remembered
	loginLockout  = time.Minute      // how long a client waits after too many
)

// lockout slows down guessing of the access code: after loginMaxTries tries
// within loginWindow a client address is refused for loginLockout, even with
// the right code. A right code clears the client's count.
type lockout struct {
	mu      sync.Mutex
	clients map[string]*clientTries
}

type clientTries struct {
	tries []time.Time // tries inside the window, oldest first
	until time.Time   // refused until then
}

func newLockout() *lockout { return &lockout{clients: map[string]*clientTries{}} }

// begin counts a try by client and returns how long it must wait instead, or
// zero when the try may go ahead. Counting before the code is checked keeps a
// burst of parallel guesses from slipping past the limit.
func (l *lockout) begin(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, c := range l.clients { // forget clients that went quiet
		if !now.Before(c.until) && (len(c.tries) == 0 || now.Sub(c.tries[len(c.tries)-1]) >= loginWindow) {
			delete(l.clients, k)
		}
	}
	c := l.clients[client]
	if c == nil {
		c = &clientTries{}
		l.clients[client] = c
	}
	if now.Before(c.until) {
		return c.until.Sub(now)
	}
	recent := c.tries[:0]
	for _, t := range c.tries {
		if now.Sub(t) < loginWindow {
			recent = append(recent, t)
		}
	}
	c.tries = append(recent, now)
	if len(c.tries) >= loginMaxTries {
		c.tries, c.until = nil, now.Add(loginLockout)
	}
	return 0
}

// clear forgets client after it entered the right code.
func (l *lockout) clear(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, client)
}

// clientAddr is the peer's IP address without the port.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

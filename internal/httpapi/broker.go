package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"depot/internal/auth"
)

// notice is one live update: a changed "move" or "approval" and its id.
type notice struct{ kind, id string }

// subscriber is an open screen: its company and whether its role may manage.
type subscriber struct {
	company string
	manager bool
}

// Broker fans change notices out to the open screens of the company: moves
// to all of them, approvals to managers only.
type Broker struct {
	mu   sync.Mutex
	subs map[chan notice]subscriber
}

func NewBroker() *Broker { return &Broker{subs: map[chan notice]subscriber{}} }

// Subscribe returns a channel of the company's notices and a function to stop.
// manager says whether approval notices are sent too.
func (b *Broker) Subscribe(companyID string, manager bool) (<-chan notice, func()) {
	ch := make(chan notice, 16)
	b.mu.Lock()
	b.subs[ch] = subscriber{companyID, manager}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish announces that a move of a company changed, to that company's
// screens only.
func (b *Broker) Publish(companyID, moveID string) { b.Notify(companyID, "move", moveID) }

// Notify announces a changed "move" (to the company's screens) or "approval"
// (to the company's managers). It never blocks: a slow screen misses the
// notice and catches up on the next one.
func (b *Broker) Notify(companyID, kind, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch, sub := range b.subs {
		if sub.company != companyID || (kind == "approval" && !sub.manager) {
			continue
		}
		select {
		case ch <- notice{kind, id}:
		default:
		}
	}
}

// stream serves Server-Sent Events until the client goes away.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "E9001", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	sc := scopeOf(r.Context())
	ch, stop := s.broker.Subscribe(sc.CompanyID, auth.Can(sc.Role, auth.PermManage))
	defer stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case n := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: {\"id\":%q}\n\n", n.kind, n.id)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

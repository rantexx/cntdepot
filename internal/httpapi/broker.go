package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Broker fans move-change notices out to the open screens of the move's company.
type Broker struct {
	mu   sync.Mutex
	subs map[chan string]string // subscriber -> its company
}

func NewBroker() *Broker { return &Broker{subs: map[chan string]string{}} }

// Subscribe returns a channel of the company's changed move IDs and a function to stop.
func (b *Broker) Subscribe(companyID string) (<-chan string, func()) {
	ch := make(chan string, 16)
	b.mu.Lock()
	b.subs[ch] = companyID
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish announces that a move of a company changed, to that company's
// screens only. It never blocks: a slow screen misses the notice and catches
// up on the next one.
func (b *Broker) Publish(companyID, moveID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch, company := range b.subs {
		if company != companyID {
			continue
		}
		select {
		case ch <- moveID:
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
	ch, stop := s.broker.Subscribe(scopeOf(r.Context()).CompanyID)
	defer stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case id := <-ch:
			fmt.Fprintf(w, "event: move\ndata: {\"id\":%q}\n\n", id)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

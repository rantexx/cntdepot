package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Broker fans move-change notices out to every open screen.
type Broker struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan string]struct{}{}} }

// Subscribe returns a channel of changed move IDs and a function to stop.
func (b *Broker) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish announces that a move changed. It never blocks: a slow screen
// misses the notice and catches up on the next one.
func (b *Broker) Publish(moveID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
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
	ch, stop := s.broker.Subscribe()
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

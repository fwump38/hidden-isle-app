// Package live fans out campaign events to open browsers (Server-Sent Events): "changed" when a
// record changes, so pages refresh, and "handout" when the Seer pushes something to players.
package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Message is one event. SeerOnly messages go only to the Seer's connections; To limits a
// message to one user (0 = everyone allowed to see it).
type Message struct {
	Type     string `json:"type"` // changed | handout
	Data     any    `json:"data,omitempty"`
	SeerOnly bool   `json:"-"`
	To       uint   `json:"-"`
}

type sub struct {
	user uint
	seer bool
	ch   chan Message
}

type Hub struct {
	mu   sync.Mutex
	subs map[uint]map[*sub]struct{} // campaign → connections
}

func New() *Hub { return &Hub{subs: map[uint]map[*sub]struct{}{}} }

// Publish sends m to the campaign's connections that may see it. It never blocks: a slow
// connection just misses the message (the next one refreshes it anyway).
func (h *Hub) Publish(campaignID uint, m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[campaignID] {
		if m.SeerOnly && !s.seer {
			continue
		}
		if m.To != 0 && m.To != s.user && !s.seer {
			continue
		}
		select {
		case s.ch <- m:
		default:
		}
	}
}

// Subscribers returns how many connections a campaign has (for the dashboard).
func (h *Hub) Subscribers(campaignID uint) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[campaignID])
}

// Serve streams a campaign's events to one browser until it disconnects. user 0 with seer
// false is a read-only viewer (the TV), which gets only public messages.
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, campaignID, user uint, seer bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	s := &sub{user: user, seer: seer, ch: make(chan Message, 16)}
	h.mu.Lock()
	if h.subs[campaignID] == nil {
		h.subs[campaignID] = map[*sub]struct{}{}
	}
	h.subs[campaignID][s] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs[campaignID], s)
		h.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // don't let a proxy buffer the stream
	fmt.Fprint(w, "retry: 5000\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second) // keeps tunnels and proxies from closing idle streams
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case m := <-s.ch:
			b, _ := json.Marshal(m.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, b)
			fl.Flush()
		}
	}
}

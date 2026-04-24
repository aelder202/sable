package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aelder202/sable/internal/session"
)

// terminalStreamHandler streams task outputs as Server-Sent Events for a given agent.
// The client connects once and receives a push notification for each new output
// without polling, giving near-real-time response in interactive mode.
func terminalStreamHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// SSE connections live indefinitely; remove the server-level write deadline
		// so the 10s WriteTimeout on the http.Server doesn't kill the stream.
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			http.Error(w, "cannot set stream deadline", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		ch := store.Subscribe(agentID)
		defer store.Unsubscribe(agentID, ch)

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		sent := make(map[string]bool)

		// Flush existing outputs immediately so the client sees history.
		flushPending(w, flusher, store, agentID, sent)

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case <-ch:
				flushPending(w, flusher, store, agentID, sent)
			}
		}
	}
}

func flushPending(w http.ResponseWriter, flusher http.Flusher, store *session.Store, agentID string, sent map[string]bool) {
	for _, o := range store.GetOutputs(agentID) {
		if sent[o.TaskID] {
			continue
		}
		sent[o.TaskID] = true
		data, err := json.Marshal(o)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()
}

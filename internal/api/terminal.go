package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/aelder202/sable/internal/session"
)

const sseWriteTimeout = 5 * time.Second

// terminalStreamHandler streams task outputs as Server-Sent Events for a given agent.
// The client connects once and receives a push notification for each new output
// without polling, giving near-real-time response in interactive and path
// suggestion flows.
func terminalStreamHandler(store *session.Store, agentID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if _, ok := w.(http.Flusher); !ok {
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
		if err := flushPending(w, rc, store, agentID, sent); err != nil {
			return
		}

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if err := writeSSE(w, rc, []byte(": keepalive\n\n")); err != nil {
					return
				}
			case <-ch:
				if err := flushPending(w, rc, store, agentID, sent); err != nil {
					return
				}
			}
		}
	}
}

func flushPending(w http.ResponseWriter, rc *http.ResponseController, store *session.Store, agentID string, sent map[string]bool) error {
	for _, o := range store.GetOutputs(agentID) {
		if sent[o.TaskID] {
			continue
		}
		data, err := json.Marshal(o)
		if err != nil {
			continue
		}
		frame := make([]byte, 0, len(data)+8)
		frame = append(frame, "data: "...)
		frame = append(frame, data...)
		frame = append(frame, '\n', '\n')
		if err := writeSSE(w, rc, frame); err != nil {
			return err
		}
		sent[o.TaskID] = true
	}
	return nil
}

func writeSSE(w http.ResponseWriter, rc *http.ResponseController, frame []byte) error {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil {
		return err
	}
	defer rc.SetWriteDeadline(time.Time{}) //nolint:errcheck
	if _, err := w.Write(frame); err != nil {
		return err
	}
	return rc.Flush()
}

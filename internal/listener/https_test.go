package listener_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
)

var testSecret = []byte("32-byte-test-secret-padding-here")

func newTestSetup(t *testing.T) (*listener.HTTPSHandler, *session.Store) {
	t.Helper()
	store := session.NewStore()
	store.Register(&session.Agent{
		ID:       "agent-1",
		Secret:   testSecret,
		Hostname: "victim",
		OS:       "linux",
		Arch:     "amd64",
	})
	nc := nonce.NewCache(5 * time.Minute)
	return listener.NewHTTPSHandler(store, nc), store
}

func makeBeacon(t *testing.T, agentID string, secret []byte, timestamp int64) []byte {
	t.Helper()
	n, err := protocol.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	b := &protocol.Beacon{
		AgentID:   agentID,
		Timestamp: timestamp,
		Nonce:     n,
		Hostname:  "victim",
		OS:        "linux",
		Arch:      "amd64",
	}
	encoded, err := protocol.EncodeBeacon(b, secret)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func postBeacon(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/cdn/static/update", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestValidBeaconReturns200(t *testing.T) {
	h, _ := newTestSetup(t)
	body := makeBeacon(t, "agent-1", testSecret, time.Now().Unix())
	w := postBeacon(t, h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestInvalidHMACReturns404(t *testing.T) {
	h, _ := newTestSetup(t)
	body := makeBeacon(t, "agent-1", testSecret, time.Now().Unix())
	body[len(body)-2] ^= 0xff // tamper
	w := postBeacon(t, h, body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on tampered beacon, got %d", w.Code)
	}
}

func TestReplayAttackReturns404(t *testing.T) {
	h, _ := newTestSetup(t)
	body := makeBeacon(t, "agent-1", testSecret, time.Now().Unix())
	w1 := postBeacon(t, h, body)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request should be 200, got %d", w1.Code)
	}
	w2 := postBeacon(t, h, body)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("replay should be 404, got %d", w2.Code)
	}
}

func TestUnknownAgentIDReturns404(t *testing.T) {
	h, _ := newTestSetup(t)
	body := makeBeacon(t, "unknown-agent", testSecret, time.Now().Unix())
	w := postBeacon(t, h, body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent must get 404, got %d", w.Code)
	}
}

func TestExpiredTimestampReturns404(t *testing.T) {
	h, _ := newTestSetup(t)
	oldTs := time.Now().Add(-10 * time.Minute).Unix()
	body := makeBeacon(t, "agent-1", testSecret, oldTs)
	w := postBeacon(t, h, body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expired timestamp must get 404, got %d", w.Code)
	}
}

func TestWrongMethodReturns404(t *testing.T) {
	h, _ := newTestSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/cdn/static/update", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET must return 404, got %d", w.Code)
	}
}

func TestCrossAgentImpersonationReturns404(t *testing.T) {
	// Register a victim agent with a different secret.
	h, store := newTestSetup(t)
	victimSecret := []byte("victim-secret-32-bytes-padding-x")
	store.Register(&session.Agent{
		ID:     "victim",
		Secret: victimSecret,
	})
	if err := store.EnqueueTask("victim", &protocol.Task{ID: "stolen-task", Type: "shell", Payload: "id"}); err != nil {
		t.Fatalf("EnqueueTask victim: %v", err)
	}

	// Rogue agent: valid beacon for "agent-1" (envelope and payload agree),
	// but the internal AgentID is "victim". The MAC now covers "agent-1"||ct,
	// so the only way to pass is to have envelope.id == decrypted.agent_id.
	// This creates a beacon for "agent-1" with agent-1's secret, but sends it
	// with a raw-JSON-modified envelope to simulate the attack.
	n, _ := protocol.RandomNonce()
	b := &protocol.Beacon{
		AgentID: "agent-1", Timestamp: time.Now().Unix(), Nonce: n,
		Hostname: "rogue", OS: "linux", Arch: "amd64",
	}
	encoded, _ := protocol.EncodeBeacon(b, testSecret)
	// Tamper: change the envelope "id" to "victim" to try to steal victim's task.
	var raw map[string]interface{}
	json.Unmarshal(encoded, &raw) //nolint:errcheck
	raw["id"] = "victim"
	tampered, _ := json.Marshal(raw)

	w := postBeacon(t, h, tampered)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-agent impersonation must return 404, got %d", w.Code)
	}
	// Victim's task must remain in the queue untouched.
	remaining := store.DequeueTask("victim")
	if remaining == nil || remaining.ID != "stolen-task" {
		t.Fatal("victim's task was stolen or missing")
	}
}

func TestTaskDeliveredOnBeacon(t *testing.T) {
	h, store := newTestSetup(t)
	if err := store.EnqueueTask("agent-1", &protocol.Task{ID: "t1", Type: "shell", Payload: "whoami"}); err != nil {
		t.Fatalf("EnqueueTask agent-1: %v", err)
	}

	body := makeBeacon(t, "agent-1", testSecret, time.Now().Unix())
	w := postBeacon(t, h, body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	task, err := protocol.DecodeTask(w.Body.Bytes(), testSecret)
	if err != nil {
		t.Fatalf("DecodeTask failed: %v", err)
	}
	if task.ID != "t1" || task.Type != "shell" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

func TestChunkedOutputDefersTaskDeliveryUntilComplete(t *testing.T) {
	h, store := newTestSetup(t)
	if err := store.EnqueueTask("agent-1", &protocol.Task{ID: "next-task", Type: "shell", Payload: "id"}); err != nil {
		t.Fatalf("EnqueueTask agent-1: %v", err)
	}

	first := makeBeaconWithOutput(t, "agent-1", testSecret, &protocol.TaskResult{
		TaskID:     "chunked-result",
		Type:       "download",
		Output:     "hello ",
		ChunkIndex: 0,
		ChunkTotal: 2,
	})
	w := postBeacon(t, h, first)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for first chunk, got %d", w.Code)
	}
	task, err := protocol.DecodeTask(w.Body.Bytes(), testSecret)
	if err != nil {
		t.Fatalf("DecodeTask first chunk: %v", err)
	}
	if task.Type != "noop" {
		t.Fatalf("expected noop while chunked output is incomplete, got %+v", task)
	}
	if outs := store.GetOutputs("agent-1"); len(outs) != 0 {
		t.Fatalf("expected no visible output before reassembly, got %+v", outs)
	}

	second := makeBeaconWithOutput(t, "agent-1", testSecret, &protocol.TaskResult{
		TaskID:     "chunked-result",
		Type:       "download",
		Output:     "world",
		ChunkIndex: 1,
		ChunkTotal: 2,
	})
	w = postBeacon(t, h, second)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for second chunk, got %d", w.Code)
	}
	task, err = protocol.DecodeTask(w.Body.Bytes(), testSecret)
	if err != nil {
		t.Fatalf("DecodeTask second chunk: %v", err)
	}
	if task.ID != "next-task" {
		t.Fatalf("expected queued task after final chunk, got %+v", task)
	}
	outs := store.GetOutputs("agent-1")
	if len(outs) != 1 || outs[0].Output != "hello world" {
		t.Fatalf("expected reassembled output, got %+v", outs)
	}
}

func makeBeaconWithOutput(t *testing.T, agentID string, secret []byte, output *protocol.TaskResult) []byte {
	t.Helper()
	n, err := protocol.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	b := &protocol.Beacon{
		AgentID:    agentID,
		Timestamp:  time.Now().Unix(),
		Nonce:      n,
		Hostname:   "victim",
		OS:         "linux",
		Arch:       "amd64",
		TaskOutput: output,
	}
	encoded, err := protocol.EncodeBeacon(b, secret)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

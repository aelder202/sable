package protocol_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

var testSecret = []byte("32-byte-test-secret-padding-here")

func TestBeaconRoundTrip(t *testing.T) {
	nonce, err := protocol.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	b := &protocol.Beacon{
		AgentID:   "test-agent-id",
		Timestamp: time.Now().Unix(),
		Nonce:     nonce,
		Hostname:  "victim",
		OS:        "linux",
		Arch:      "amd64",
	}

	encoded, err := protocol.EncodeBeacon(b, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := protocol.DecodeBeacon(encoded, testSecret)
	if err != nil {
		t.Fatal(err)
	}

	if decoded.AgentID != b.AgentID {
		t.Fatalf("AgentID mismatch: got %q want %q", decoded.AgentID, b.AgentID)
	}
	if decoded.Hostname != b.Hostname {
		t.Fatalf("Hostname mismatch: got %q want %q", decoded.Hostname, b.Hostname)
	}
}

func TestDecodeBeaconTamperedSig(t *testing.T) {
	nonce, _ := protocol.RandomNonce()
	b := &protocol.Beacon{
		AgentID: "id", Timestamp: time.Now().Unix(),
		Nonce: nonce, Hostname: "h", OS: "linux", Arch: "amd64",
	}
	encoded, _ := protocol.EncodeBeacon(b, testSecret)
	// Tamper with the last byte of the JSON envelope
	encoded[len(encoded)-2] ^= 0xff
	_, err := protocol.DecodeBeacon(encoded, testSecret)
	if err == nil {
		t.Fatal("expected error on tampered beacon")
	}
}

func TestDecodeBeaconWrongSecret(t *testing.T) {
	nonce, _ := protocol.RandomNonce()
	b := &protocol.Beacon{
		AgentID: "id", Timestamp: time.Now().Unix(),
		Nonce: nonce, Hostname: "h", OS: "linux", Arch: "amd64",
	}
	encoded, _ := protocol.EncodeBeacon(b, testSecret)
	wrongSecret := []byte("wrong-secret-32-bytes-padding-xx")
	_, err := protocol.DecodeBeacon(encoded, wrongSecret)
	if err == nil {
		t.Fatal("expected error with wrong secret")
	}
}

func TestTaskRoundTrip(t *testing.T) {
	task := &protocol.Task{ID: "task-1", Type: "shell", Payload: "whoami"}
	encoded, err := protocol.EncodeTask(task, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := protocol.DecodeTask(encoded, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != task.ID || decoded.Payload != task.Payload {
		t.Fatal("Task round-trip mismatch")
	}
}

func TestDecodeTaskWrongSecret(t *testing.T) {
	task := &protocol.Task{ID: "t1", Type: "shell", Payload: "id"}
	encoded, _ := protocol.EncodeTask(task, testSecret)
	wrongSecret := []byte("wrong-secret-32-bytes-padding-xx")
	_, err := protocol.DecodeTask(encoded, wrongSecret)
	if err == nil {
		t.Fatal("expected error with wrong secret on task decode")
	}
}

// TestDecodeBeaconEnvelopeIDTampered verifies that changing the envelope AgentID
// after signing invalidates the MAC (the ID is bound into the MAC input).
func TestDecodeBeaconEnvelopeIDTampered(t *testing.T) {
	nonce, _ := protocol.RandomNonce()
	b := &protocol.Beacon{
		AgentID: "agent-1", Timestamp: time.Now().Unix(),
		Nonce: nonce, Hostname: "h", OS: "linux", Arch: "amd64",
	}
	encoded, _ := protocol.EncodeBeacon(b, testSecret)
	// Swap the envelope "id" field to a different agent ID.
	var raw map[string]interface{}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	raw["id"] = "agent-different"
	modified, _ := json.Marshal(raw)
	_, err := protocol.DecodeBeacon(modified, testSecret)
	if err == nil {
		t.Fatal("expected MAC failure when envelope AgentID is tampered")
	}
}

func TestRandomNonce(t *testing.T) {
	n1, err := protocol.RandomNonce()
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := protocol.RandomNonce()
	if len(n1) != 16 {
		t.Fatalf("expected 16-byte nonce, got %d", len(n1))
	}
	allSame := true
	for i := range n1 {
		if n1[i] != n2[i] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatal("nonces must be unique")
	}
}

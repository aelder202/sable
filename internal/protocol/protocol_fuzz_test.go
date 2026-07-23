package protocol_test

import (
	"testing"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

func FuzzDecodeBeacon(f *testing.F) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	nonce := []byte("0123456789abcdef")
	valid, err := protocol.EncodeBeacon(&protocol.Beacon{
		AgentID: "agent-1", Timestamp: time.Now().Unix(), Nonce: nonce, Hostname: "host", OS: "linux", Arch: "amd64",
	}, secret)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"id":"agent-1","data":"","sig":""}`))
	f.Add([]byte("not-json"))

	f.Fuzz(func(t *testing.T, data []byte) {
		beacon, err := protocol.DecodeBeacon(data, secret)
		if err == nil && beacon.AgentID == "" {
			t.Fatal("successful decode returned an empty agent ID")
		}
	})
}

func FuzzDecodeTask(f *testing.F) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	valid, err := protocol.EncodeTask(&protocol.Task{ID: "task-1", Type: "shell", Payload: "whoami"}, secret)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"data":null,"sig":null}`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		task, err := protocol.DecodeTask(data, secret)
		if err == nil && task == nil {
			t.Fatal("successful decode returned a nil task")
		}
	})
}

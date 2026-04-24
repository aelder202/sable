//go:build integration

package integration_test

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
)

var integSecret = []byte("32-byte-integration-secret-here!")

func TestFullBeaconCycle(t *testing.T) {
	store := session.NewStore()
	store.Register(&session.Agent{
		ID:       "integ-agent",
		Secret:   integSecret,
		Hostname: "integ-host",
		OS:       "linux",
		Arch:     "amd64",
	})
	nc := nonce.NewCache(5 * time.Minute)
	h := listener.NewHTTPSHandler(store, nc)
	srv := httptest.NewTLSServer(h)
	defer srv.Close()

	if err := store.EnqueueTask("integ-agent", &protocol.Task{ID: "t1", Type: "shell", Payload: "id"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	nonce16, _ := protocol.RandomNonce()
	beacon := &protocol.Beacon{
		AgentID:   "integ-agent",
		Timestamp: time.Now().Unix(),
		Nonce:     nonce16,
		Hostname:  "integ-host",
		OS:        "linux",
		Arch:      "amd64",
	}
	encoded, _ := protocol.EncodeBeacon(beacon, integSecret)

	client := srv.Client()
	resp, err := client.Post(srv.URL+"/cdn/static/update", "application/octet-stream", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	task, err := protocol.DecodeTask(buf.Bytes(), integSecret)
	if err != nil {
		t.Fatalf("DecodeTask: %v", err)
	}
	if task.ID != "t1" || task.Type != "shell" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

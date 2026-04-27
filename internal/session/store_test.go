package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
)

func TestRegisterAndGet(t *testing.T) {
	s := session.NewStore()
	ag := &session.Agent{
		ID:       "agent-1",
		Secret:   []byte("secret"),
		Hostname: "victim",
		OS:       "linux",
		Arch:     "amd64",
		LastSeen: time.Now(),
	}
	s.Register(ag)
	got, ok := s.Get("agent-1")
	if !ok {
		t.Fatal("expected agent after Register")
	}
	if got.Hostname != "victim" {
		t.Fatalf("Hostname mismatch: got %q", got.Hostname)
	}
}

func TestGetUnknownReturnsNotFound(t *testing.T) {
	s := session.NewStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected not found for unknown agent")
	}
}

func TestSecretLookup(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("mysecret")})
	secret, ok := s.Secret("a1")
	if !ok || string(secret) != "mysecret" {
		t.Fatal("Secret lookup failed")
	}
	_, ok = s.Secret("unknown")
	if ok {
		t.Fatal("Secret must return false for unknown agent")
	}
}

func TestTaskQueueRoundTrip(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	task := &protocol.Task{ID: "t1", Type: "shell", Payload: "id"}
	if err := s.EnqueueTask("a1", task); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	got := s.DequeueTask("a1")
	if got == nil {
		t.Fatal("expected task from queue")
	}
	if got.ID != "t1" {
		t.Fatalf("task ID mismatch: got %q", got.ID)
	}
	if s.DequeueTask("a1") != nil {
		t.Fatal("queue must be empty after dequeue")
	}
}

func TestDequeueEmptyReturnsNil(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})
	if s.DequeueTask("a1") != nil {
		t.Fatal("dequeue from empty queue must return nil")
	}
}

func TestDequeueUnknownAgentReturnsNil(t *testing.T) {
	s := session.NewStore()
	if s.DequeueTask("no-such-agent") != nil {
		t.Fatal("dequeue for unknown agent must return nil")
	}
}

func TestListExcludesSecrets(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("supersecret"), Hostname: "host1"})
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(list))
	}
	if len(list[0].Secret) != 0 {
		t.Fatal("List must not include agent secrets")
	}
	if list[0].Hostname != "host1" {
		t.Fatalf("expected hostname host1, got %q", list[0].Hostname)
	}
}

func TestListPreservesRegistrationOrder(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "first", Secret: []byte("s1")})
	s.Register(&session.Agent{ID: "second", Secret: []byte("s2")})
	s.Register(&session.Agent{ID: "third", Secret: []byte("s3")})

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(list))
	}
	if list[0].ID != "first" || list[1].ID != "second" || list[2].ID != "third" {
		t.Fatalf("unexpected list order: got [%s %s %s]", list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestRegisterExistingAgentKeepsOriginalOrder(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "first", Secret: []byte("s1"), Hostname: "one"})
	s.Register(&session.Agent{ID: "second", Secret: []byte("s2"), Hostname: "two"})

	s.Register(&session.Agent{ID: "first", Secret: []byte("s1"), Hostname: "updated"})

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(list))
	}
	if list[0].ID != "first" || list[1].ID != "second" {
		t.Fatalf("unexpected list order after re-register: got [%s %s]", list[0].ID, list[1].ID)
	}
	if list[0].Hostname != "updated" {
		t.Fatalf("expected updated hostname, got %q", list[0].Hostname)
	}
}

func TestUpdateInfo(t *testing.T) {
	s := session.NewStore()
	before := time.Now().Add(-time.Second)
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s"), LastSeen: before})
	s.UpdateInfo("a1", "newhost", "windows", "amd64")
	a, _ := s.Get("a1")
	if !a.LastSeen.After(before) {
		t.Fatal("UpdateInfo must update LastSeen")
	}
	if a.Hostname != "newhost" || a.OS != "windows" || a.Arch != "amd64" {
		t.Fatalf("UpdateInfo did not set fields: %+v", a)
	}
}

func TestRecordAndGetOutputs(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "t1", Output: "hello"})
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "t2", Output: "world", Error: "oops"})
	outs := s.GetOutputs("a1")
	if len(outs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outs))
	}
	if outs[0].TaskID != "t1" || outs[1].Error != "oops" {
		t.Fatalf("output mismatch: %+v", outs)
	}
}

func TestRecordOutputReassemblesChunks(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	if complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID:     "chunked",
		Type:       "download",
		Output:     "hello ",
		ChunkIndex: 0,
		ChunkTotal: 2,
	}); complete {
		t.Fatal("expected first chunk to be incomplete")
	}

	if complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID:     "chunked",
		Type:       "download",
		Output:     "world",
		ChunkIndex: 1,
		ChunkTotal: 2,
	}); !complete {
		t.Fatal("expected second chunk to complete output")
	}

	outs := s.GetOutputs("a1")
	if len(outs) != 1 {
		t.Fatalf("expected one reassembled output, got %d", len(outs))
	}
	if outs[0].TaskID != "chunked" || outs[0].Type != "download" || outs[0].Output != "hello world" {
		t.Fatalf("unexpected reassembled output: %+v", outs[0])
	}
}

func TestRecordOutputRejectsOversizedChunkedOutput(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID:     "too-big",
		Type:       "download",
		Output:     strings.Repeat("x", 25*1024*1024),
		ChunkIndex: 0,
		ChunkTotal: 2,
	})
	if !complete {
		t.Fatal("oversized output should complete with an error record")
	}
	outs := s.GetOutputs("a1")
	if len(outs) != 1 || outs[0].Error == "" {
		t.Fatalf("expected oversized error record, got %+v", outs)
	}
}

func TestEnqueueTaskQueueFull(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})
	for i := 0; i < 64; i++ {
		if err := s.EnqueueTask("a1", &protocol.Task{ID: "t", Type: "shell"}); err != nil {
			t.Fatalf("unexpected error before queue full: %v", err)
		}
	}
	if err := s.EnqueueTask("a1", &protocol.Task{ID: "overflow", Type: "shell"}); err == nil {
		t.Fatal("expected queue full error")
	}
}

func TestRecordOutputCapsHistory(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})
	for i := 0; i < 300; i++ {
		s.RecordOutput("a1", &protocol.TaskResult{TaskID: "t", Output: "x"})
	}
	outs := s.GetOutputs("a1")
	if len(outs) != 256 {
		t.Fatalf("expected capped output history, got %d", len(outs))
	}
}

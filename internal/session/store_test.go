package session_test

import (
	"path/filepath"
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

func TestUpdateInfoWithTransport(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "agent-1", Secret: []byte("secret")})

	s.UpdateInfoWithTransport("agent-1", "victim", "linux", "amd64", "dns")
	got, ok := s.Get("agent-1")
	if !ok {
		t.Fatal("expected agent after update")
	}
	if got.Transport != "dns" {
		t.Fatalf("Transport mismatch: got %q", got.Transport)
	}

	listed := s.List()
	if len(listed) != 1 || listed[0].Transport != "dns" {
		t.Fatalf("List should include transport, got %+v", listed)
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

func TestClearOutputs(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "t1", Output: "hello"})

	if !s.ClearOutputs("a1") {
		t.Fatal("expected ClearOutputs to find agent")
	}
	if outs := s.GetOutputs("a1"); len(outs) != 0 {
		t.Fatalf("expected output history to be cleared, got %+v", outs)
	}
	if s.ClearOutputs("missing") {
		t.Fatal("expected ClearOutputs to reject unknown agent")
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
		Output:     strings.Repeat("x", 73*1024*1024),
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

func TestArtifactsAreStoredAsServerObjects(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	saved, ok := s.AddArtifact("a1", session.Artifact{
		ID:       "artifact-1",
		Key:      "task:report.txt",
		TaskID:   "task-1",
		Label:    "report",
		Filename: "report.txt",
		MIME:     "text/plain",
		Data:     "aGVsbG8=",
	})
	if !ok {
		t.Fatal("expected AddArtifact to find agent")
	}
	if saved.Data != "" {
		t.Fatal("artifact summary must omit data")
	}

	listed := s.ListArtifacts("a1")
	if len(listed) != 1 || listed[0].Data != "" {
		t.Fatalf("list should include one summary without data, got %#v", listed)
	}

	full, ok := s.GetArtifact("a1", "artifact-1")
	if !ok {
		t.Fatal("expected GetArtifact to find artifact")
	}
	if full.Data != "aGVsbG8=" || full.ArchiveFilename != "report.txt" {
		t.Fatalf("artifact data/defaults not preserved: %#v", full)
	}

	dupe, ok := s.AddArtifact("a1", session.Artifact{
		ID:       "artifact-2",
		Key:      "task:report.txt",
		Filename: "other.txt",
		Data:     "b3RoZXI=",
	})
	if !ok || dupe.ID != "artifact-1" {
		t.Fatalf("expected duplicate key to return original summary, got %#v", dupe)
	}
	if listed := s.ListArtifacts("a1"); len(listed) != 1 {
		t.Fatalf("expected duplicate key to keep one artifact, got %d", len(listed))
	}
}

func TestPersistentStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sable-state.json")
	s, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatalf("NewPersistentStore: %v", err)
	}

	s.Register(&session.Agent{ID: "a1", Secret: []byte("secret")})
	s.UpdateInfoWithTransport("a1", "host", "linux", "amd64", "https")
	if _, ok := s.UpdateMetadata("a1", "important", []string{"lab", "lab", "linux"}); !ok {
		t.Fatal("expected metadata update to find agent")
	}
	if err := s.EnqueueTask("a1", &protocol.Task{ID: "task-1", Type: "shell", Payload: "id"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "done-1", Type: "shell", Output: "hello"})
	if _, ok := s.AddArtifact("a1", session.Artifact{
		ID:       "artifact-1",
		Key:      "done-1:output.txt",
		TaskID:   "done-1",
		Filename: "output.txt",
		Data:     "aGVsbG8=",
	}); !ok {
		t.Fatal("expected artifact save to find agent")
	}

	reloaded, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatalf("reload NewPersistentStore: %v", err)
	}
	agent, ok := reloaded.Get("a1")
	if !ok {
		t.Fatal("expected persisted agent after reload")
	}
	if agent.Hostname != "host" || agent.Transport != "https" || agent.Notes != "important" {
		t.Fatalf("unexpected persisted agent: %+v", agent)
	}
	if len(agent.Tags) != 2 || agent.Tags[0] != "lab" || agent.Tags[1] != "linux" {
		t.Fatalf("unexpected persisted tags: %#v", agent.Tags)
	}
	if len(agent.Queued) != 1 || agent.Queued[0].ID != "task-1" {
		t.Fatalf("unexpected persisted queue: %#v", agent.Queued)
	}
	outputs := reloaded.GetOutputs("a1")
	if len(outputs) != 1 || outputs[0].Output != "hello" {
		t.Fatalf("unexpected persisted outputs: %#v", outputs)
	}
	secret, ok := reloaded.Secret("a1")
	if !ok || string(secret) != "secret" {
		t.Fatalf("unexpected persisted secret: %q", secret)
	}
	if len(reloaded.AuditLog()) == 0 {
		t.Fatal("expected persisted audit events")
	}
	artifact, ok := reloaded.GetArtifact("a1", "artifact-1")
	if !ok || artifact.Data != "aGVsbG8=" {
		t.Fatalf("unexpected persisted artifact: %#v", artifact)
	}
}

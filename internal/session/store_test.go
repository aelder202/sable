package session_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
)

func enqueueAndDeliver(t *testing.T, s *session.Store, agentID string, task *protocol.Task) {
	t.Helper()
	if err := s.EnqueueTask(agentID, task); err != nil {
		t.Fatal(err)
	}
	if delivered := s.DeliverTask(agentID); delivered == nil || delivered.ID != task.ID {
		t.Fatalf("task %q was not delivered: %+v", task.ID, delivered)
	}
}

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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "t1", Type: "shell"})
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "t1", Output: "hello"})
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "t2", Type: "shell"})
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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "t1", Type: "shell"})
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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "chunked", Type: "download"})

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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "too-big", Type: "download"})

	complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID:     "too-big",
		Type:       "download",
		Output:     strings.Repeat("x", 1024*1024+1),
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

func TestRecordOutputRejectsUnsolicitedTaskID(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	if complete := s.RecordOutput("a1", &protocol.TaskResult{TaskID: "not-issued", Type: "shell", Output: "ignored"}); !complete {
		t.Fatal("an unsolicited result should be terminally acknowledged")
	}
	if outputs := s.GetOutputs("a1"); len(outputs) != 0 {
		t.Fatalf("unsolicited result was retained: %+v", outputs)
	}
}

func TestRecordOutputBoundsConcurrentChunkAssemblies(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("s")})

	for i := 0; i < 9; i++ {
		taskID := fmt.Sprintf("background-%d", i)
		enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: taskID, Type: "download"})
		s.RecordOutput("a1", &protocol.TaskResult{
			TaskID: taskID + "-download-progress",
			Type:   "download_progress",
			Output: "started",
		})
	}
	for i := 0; i < 8; i++ {
		if complete := s.RecordOutput("a1", &protocol.TaskResult{
			TaskID: fmt.Sprintf("background-%d", i), Type: "download", Output: "part", ChunkIndex: 0, ChunkTotal: 2,
		}); complete {
			t.Fatalf("assembly %d unexpectedly completed", i)
		}
	}
	if complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID: "background-8", Type: "download", Output: "part", ChunkIndex: 0, ChunkTotal: 2,
	}); !complete {
		t.Fatal("assembly over the per-agent cap should complete with an error")
	}
	outputs := s.GetOutputs("a1")
	if len(outputs) == 0 || !strings.Contains(outputs[len(outputs)-1].Error, "too many incomplete") {
		t.Fatalf("expected assembly limit error, got %+v", outputs)
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
		taskID := fmt.Sprintf("t-%d", i)
		enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: taskID, Type: "shell"})
		s.RecordOutput("a1", &protocol.TaskResult{TaskID: taskID, Output: "x"})
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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "task-1", Type: "shell", Payload: "id"})
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "task-1", Type: "shell", Output: "hello"})
	if err := s.EnqueueTask("a1", &protocol.Task{ID: "task-2", Type: "shell", Payload: "whoami"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}
	if _, ok := s.AddArtifact("a1", session.Artifact{
		ID:       "artifact-1",
		Key:      "task-1:output.txt",
		TaskID:   "task-1",
		Filename: "output.txt",
		Data:     "aGVsbG8=",
	}); !ok {
		t.Fatal("expected artifact save to find agent")
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush persistent state: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close persistent state: %v", err)
	}

	reloaded, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatalf("reload NewPersistentStore: %v", err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
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
	if len(agent.Queued) != 1 || agent.Queued[0].ID != "task-2" {
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

func TestPersistentStoreRetainsBackgroundTaskCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sable-state.json")
	s, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Register(&session.Agent{ID: "a1", Secret: []byte("secret")})
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "download-1", Type: "download"})
	if !s.RecordOutput("a1", &protocol.TaskResult{
		TaskID: "download-1-progress", Type: "download_progress", Output: "started",
	}) {
		t.Fatal("progress result should acknowledge background task start")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close() //nolint:errcheck
	if !reloaded.RecordOutput("a1", &protocol.TaskResult{TaskID: "download-1", Type: "download", Output: "finished"}) {
		t.Fatal("background final result should remain correlated after restart")
	}
	outputs := reloaded.GetOutputs("a1")
	if len(outputs) != 2 || outputs[1].TaskID != "download-1" || outputs[1].Output != "finished" {
		t.Fatalf("unexpected restored background outputs: %+v", outputs)
	}
}

func TestTaskDeliveryRetriesUntilMatchingAcknowledgment(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("secret")})
	for _, task := range []*protocol.Task{
		{ID: "task-1", Type: "shell", Payload: "one"},
		{ID: "task-2", Type: "shell", Payload: "two"},
	} {
		if err := s.EnqueueTask("a1", task); err != nil {
			t.Fatal(err)
		}
	}

	first := s.DeliverTask("a1")
	retry := s.DeliverTask("a1")
	if first == nil || retry == nil || first.ID != "task-1" || retry.ID != "task-1" {
		t.Fatalf("lost-response retry did not redeliver first task: first=%+v retry=%+v", first, retry)
	}
	queued := s.GetQueuedTasks("a1")
	if len(queued) != 2 || queued[0].Status != "in_flight" || queued[0].DeliveryAttempts != 2 {
		t.Fatalf("unexpected delivery status: %+v", queued)
	}

	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "task-1", Type: "shell", Output: "done"})
	if next := s.DeliverTask("a1"); next == nil || next.ID != "task-2" {
		t.Fatalf("matching acknowledgment did not advance queue: %+v", next)
	}
	// A retried old result must not acknowledge the new in-flight task.
	s.RecordOutput("a1", &protocol.TaskResult{TaskID: "task-1", Type: "shell", Output: "done"})
	if next := s.DeliverTask("a1"); next == nil || next.ID != "task-2" {
		t.Fatalf("duplicate old result advanced queue: %+v", next)
	}
}

func TestMaliciousChunkCountIsRejectedWithoutAssembly(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("secret")})
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "malicious", Type: "download"})
	complete := s.RecordOutput("a1", &protocol.TaskResult{
		TaskID: "malicious", Type: "download", Output: "x", ChunkIndex: 0, ChunkTotal: 1_000_000_000,
	})
	if !complete {
		t.Fatal("invalid chunk metadata should produce a terminal error")
	}
	outputs := s.GetOutputs("a1")
	if len(outputs) != 1 || !strings.Contains(outputs[0].Error, "chunk count") {
		t.Fatalf("unexpected validation output: %+v", outputs)
	}
}

func TestEncryptedStateAndArtifactBlobsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	key := bytes.Repeat([]byte{0x42}, 32)
	s, err := session.NewPersistentStoreWithKey(path, key)
	if err != nil {
		t.Fatal(err)
	}
	s.Register(&session.Agent{ID: "a1", Secret: bytes.Repeat([]byte{1}, 32)})
	if _, ok, err := s.AddArtifactChecked("a1", session.Artifact{
		ID: "artifact-1", Filename: "proof.txt", Data: "c2Vuc2l0aXZlLWRhdGE=",
	}); err != nil || !ok {
		t.Fatalf("store artifact: ok=%v err=%v", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	stateData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateData, []byte("a1")) || bytes.Contains(stateData, []byte("c2Vuc2l0aXZlLWRhdGE=")) {
		t.Fatal("encrypted state leaked plaintext metadata or artifact content")
	}
	blobPath := filepath.Join(path+".artifacts", "a1", "artifact-1.blob")
	blobData, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blobData, []byte("c2Vuc2l0aXZlLWRhdGE=")) {
		t.Fatal("encrypted artifact blob leaked plaintext")
	}
	if _, err := session.NewPersistentStore(path); err == nil {
		t.Fatal("encrypted state should require a key")
	}

	reloaded, err := session.NewPersistentStoreWithKey(path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	artifact, ok := reloaded.GetArtifact("a1", "artifact-1")
	if !ok || artifact.Data != "c2Vuc2l0aXZlLWRhdGE=" {
		t.Fatalf("unexpected reloaded artifact: %+v", artifact)
	}
	if !reloaded.DeleteArtifact("a1", "artifact-1") {
		t.Fatal("expected persisted artifact deletion to succeed")
	}
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Fatalf("artifact blob still exists after deletion: %v", err)
	}
}

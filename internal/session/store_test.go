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

func TestValidatePersistentStateDetectsWrongEncryptionKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	key := bytes.Repeat([]byte{0x41}, 32)
	store, err := session.NewPersistentStoreWithKey(path, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.ValidatePersistentState(path, key); err != nil {
		t.Fatalf("matching key rejected: %v", err)
	}
	if err := session.ValidatePersistentState(path, bytes.Repeat([]byte{0x42}, 32)); err == nil ||
		!strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong key error = %v, want authentication failure", err)
	}
}

func TestUpdateInfoWithTransport(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "agent-1", Secret: []byte("secret")})

	s.UpdateInfoWithAddresses("agent-1", "victim", "linux", "amd64", "dns", "192.0.2.25", "10.10.20.25", 30)
	got, ok := s.Get("agent-1")
	if !ok {
		t.Fatal("expected agent after update")
	}
	if got.Transport != "dns" {
		t.Fatalf("Transport mismatch: got %q", got.Transport)
	}
	if got.LastIP != "192.0.2.25" || got.HostIP != "10.10.20.25" || got.SleepSeconds != 30 {
		t.Fatalf("source/runtime metadata mismatch: %+v", got)
	}

	listed := s.List()
	if len(listed) != 1 || listed[0].Transport != "dns" || listed[0].LastIP != "192.0.2.25" || listed[0].HostIP != "10.10.20.25" {
		t.Fatalf("List should include transport, got %+v", listed)
	}
}

func TestOverviewUsesSleepAwareStatusAndLightweightCounts(t *testing.T) {
	s := session.NewStore()
	now := time.Now()
	s.Register(&session.Agent{
		ID:           "scheduled",
		Secret:       []byte("s1"),
		DisplayName:  "Web Server",
		FirstSeen:    now.Add(-time.Hour),
		LastSeen:     now.Add(-20 * time.Minute),
		SleepSeconds: 15 * 60,
		Artifacts:    []session.Artifact{{ID: "a1", Filename: "proof.txt"}},
	})
	s.Register(&session.Agent{ID: "never", Secret: []byte("s2")})
	s.Register(&session.Agent{
		ID:           "offline",
		Secret:       []byte("s3"),
		FirstSeen:    now.Add(-4 * time.Hour),
		LastSeen:     now.Add(-2 * time.Hour),
		SleepSeconds: 15 * 60,
	})
	if err := s.EnqueueTask("scheduled", &protocol.Task{ID: "task-1", Type: "shell", Payload: "id"}); err != nil {
		t.Fatal(err)
	}

	overview := s.Overview()
	if overview.Total != 3 || overview.OnSchedule != 1 || overview.NeverSeen != 1 || overview.Offline != 1 {
		t.Fatalf("unexpected overview counts: %#v", overview)
	}
	if overview.QueuedTasks != 1 || len(overview.Agents) != 3 {
		t.Fatalf("unexpected overview work summary: %#v", overview)
	}
	if overview.Agents[0].DisplayName != "Web Server" || overview.Agents[0].ArtifactCount != 1 {
		t.Fatalf("unexpected scheduled summary: %#v", overview.Agents[0])
	}
}

func TestOverviewOnlyTreatsDeliveredWorkAsActive(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{
		ID:          "agent-1",
		Secret:      []byte("secret"),
		DisplayName: "Workstation",
		FirstSeen:   time.Now().Add(-time.Minute),
		LastSeen:    time.Now(),
	})
	for _, task := range []*protocol.Task{
		{ID: "processing", Type: "shell", Payload: "whoami"},
		{ID: "waiting", Type: "ps"},
	} {
		if err := s.EnqueueTask("agent-1", task); err != nil {
			t.Fatal(err)
		}
	}
	if delivered := s.DeliverTask("agent-1"); delivered == nil || delivered.ID != "processing" {
		t.Fatalf("unexpected delivered task: %+v", delivered)
	}

	overview := s.Overview()
	if overview.QueuedTasks != 1 || overview.RunningTasks != 1 {
		t.Fatalf("queued/running counts = %d/%d, want 1/1", overview.QueuedTasks, overview.RunningTasks)
	}
	if len(overview.ActiveJobs) != 1 {
		t.Fatalf("active jobs = %+v, want one delivered job", overview.ActiveJobs)
	}
	job := overview.ActiveJobs[0]
	if job.ID != "processing" || job.AgentID != "agent-1" || job.AgentName != "Workstation" ||
		job.Type != "shell" || job.Payload != "whoami" || job.ReceivedAt.IsZero() {
		t.Fatalf("unexpected active job: %+v", job)
	}
	if len(overview.Agents) != 1 || overview.Agents[0].QueuedCount != 1 || overview.Agents[0].RunningCount != 1 {
		t.Fatalf("unexpected agent work summary: %+v", overview.Agents)
	}
}

func TestOverviewTracksBackgroundWorkUntilTerminalResult(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{
		ID:        "agent-1",
		Secret:    []byte("secret"),
		FirstSeen: time.Now().Add(-time.Minute),
		LastSeen:  time.Now(),
	})
	enqueueAndDeliver(t, s, "agent-1", &protocol.Task{
		ID:      "download-1",
		Type:    "download",
		Payload: "/tmp/evidence.zip",
	})
	if !s.RecordOutput("agent-1", &protocol.TaskResult{
		TaskID: "download-1-started",
		Type:   "download_progress",
		Output: "preparing",
	}) {
		t.Fatal("progress result should be recorded")
	}

	active := s.Overview()
	if active.QueuedTasks != 0 || active.RunningTasks != 1 || len(active.ActiveJobs) != 1 {
		t.Fatalf("background task not reported as active: %+v", active)
	}
	if active.ActiveJobs[0].ID != "download-1" || active.ActiveJobs[0].Type != "download" {
		t.Fatalf("unexpected background job: %+v", active.ActiveJobs[0])
	}

	if !s.RecordOutput("agent-1", &protocol.TaskResult{
		TaskID: "download-1",
		Type:   "download",
		Output: "finished",
	}) {
		t.Fatal("terminal result should be recorded")
	}
	finished := s.Overview()
	if finished.RunningTasks != 0 || len(finished.ActiveJobs) != 0 {
		t.Fatalf("completed task remained active: %+v", finished.ActiveJobs)
	}
}

func TestOverviewIncludesOutcomeBucketsAndRecentActivity(t *testing.T) {
	s := session.NewStore()
	now := time.Now()
	s.Register(&session.Agent{
		ID:          "lab-agent",
		Secret:      []byte("secret"),
		DisplayName: "Lab Agent",
		FirstSeen:   now.Add(-time.Hour),
		LastSeen:    now.Add(-5 * time.Minute),
		Transport:   "dns",
		Outputs: []session.TaskOutput{
			{TaskID: "success", Type: "shell", Timestamp: now.Add(-2 * time.Hour)},
			{TaskID: "warning", Type: "shell", Warning: "exit status 1", Timestamp: now.Add(-45 * time.Minute)},
			{TaskID: "failure", Type: "screenshot", Error: "capture failed", Timestamp: now.Add(-30 * time.Minute)},
			{TaskID: "progress", Type: "download_progress", Timestamp: now.Add(-10 * time.Minute)},
		},
		Artifacts: []session.Artifact{
			{ID: "artifact", Filename: "proof.txt", CreatedAt: now.Add(-20 * time.Minute)},
		},
	})

	overview := s.Overview()
	if len(overview.TaskOutcomes24Hours) != 24 || len(overview.TaskOutcomes7Days) != 7 {
		t.Fatalf("unexpected outcome bucket counts: 24h=%d 7d=%d", len(overview.TaskOutcomes24Hours), len(overview.TaskOutcomes7Days))
	}
	successful, warnings, failed := 0, 0, 0
	for _, bucket := range overview.TaskOutcomes24Hours {
		successful += bucket.Successful
		warnings += bucket.Warnings
		failed += bucket.Failed
	}
	if successful != 1 || warnings != 1 || failed != 1 || overview.FailedLast24Hours != 1 {
		t.Fatalf("unexpected task outcomes: successful=%d warnings=%d failed=%d overview=%+v", successful, warnings, failed, overview)
	}
	if len(overview.FailureAlerts) != 1 || overview.FailureAlerts[0].TaskID != "failure" {
		t.Fatalf("unexpected failure alerts: %+v", overview.FailureAlerts)
	}
	kinds := make(map[string]bool)
	for _, event := range overview.RecentActivity {
		kinds[event.Kind] = true
		if event.AgentID != "lab-agent" || event.AgentName != "Lab Agent" {
			t.Fatalf("activity event lost agent identity: %+v", event)
		}
	}
	for _, kind := range []string{"task_success", "task_warning", "task_failed", "artifact_received", "agent_overdue"} {
		if !kinds[kind] {
			t.Fatalf("missing %q activity event: %+v", kind, overview.RecentActivity)
		}
	}
	if kinds["task_progress"] {
		t.Fatalf("progress result appeared as a completed outcome: %+v", overview.RecentActivity)
	}

	alertID := overview.FailureAlerts[0].ID
	if !s.ResolveFailureAlert(alertID, "acknowledged") {
		t.Fatal("expected failure alert acknowledgment to succeed")
	}
	resolved := s.Overview()
	if resolved.FailedLast24Hours != 0 || len(resolved.FailureAlerts) != 0 {
		t.Fatalf("acknowledged alert remained actionable: %+v", resolved.FailureAlerts)
	}
	if len(s.GetOutputs("lab-agent")) != 4 {
		t.Fatal("acknowledging an Overview alert changed retained agent output")
	}
	historicalFailures := 0
	for _, bucket := range resolved.TaskOutcomes24Hours {
		historicalFailures += bucket.Failed
	}
	if historicalFailures != 1 {
		t.Fatalf("acknowledging an alert changed task outcome history: %+v", resolved.TaskOutcomes24Hours)
	}
	if s.ResolveFailureAlert(alertID, "ignored") {
		t.Fatal("unsupported alert disposition was accepted")
	}
}

func TestAcknowledgedOverviewFailureAlertPersistsWithoutDeletingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sable-state.json")
	s, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.Register(&session.Agent{
		ID:        "a1",
		Secret:    []byte("secret"),
		FirstSeen: now.Add(-time.Hour),
		LastSeen:  now,
		Outputs: []session.TaskOutput{
			{TaskID: "failed-task", Type: "screenshot", Error: "capture failed", Timestamp: now.Add(-time.Minute)},
		},
	})
	alerts := s.Overview().FailureAlerts
	if len(alerts) != 1 || !s.ResolveFailureAlert(alerts[0].ID, "acknowledged") {
		t.Fatalf("failed to acknowledge seeded Overview alert: %+v", alerts)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := session.NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	if got := reloaded.Overview(); got.FailedLast24Hours != 0 || len(got.FailureAlerts) != 0 {
		t.Fatalf("acknowledged Overview alert returned after reload: %+v", got.FailureAlerts)
	}
	outputs := reloaded.GetOutputs("a1")
	if len(outputs) != 1 || outputs[0].TaskID != "failed-task" || outputs[0].Error != "capture failed" {
		t.Fatalf("acknowledging Overview alert changed persisted output: %+v", outputs)
	}
}

func TestShellExecutionErrorIsRecordedAsWarning(t *testing.T) {
	s := session.NewStore()
	now := time.Now()
	s.Register(&session.Agent{
		ID:        "a1",
		Secret:    []byte("secret"),
		FirstSeen: now,
		LastSeen:  now,
	})
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "shell-warning", Type: "shell", Payload: "not-a-real-command"})
	if !s.RecordOutput("a1", &protocol.TaskResult{
		TaskID: "shell-warning",
		Type:   "shell",
		Error:  "command was not recognized by the OS: not-a-real-command",
	}) {
		t.Fatal("shell warning result was not recorded")
	}

	outputs := s.GetOutputs("a1")
	if len(outputs) != 1 || outputs[0].Error != "" || outputs[0].Warning == "" {
		t.Fatalf("shell execution outcome was not normalized to a warning: %+v", outputs)
	}
	overview := s.Overview()
	if overview.FailedLast24Hours != 0 || len(overview.FailureAlerts) != 0 {
		t.Fatalf("shell warning incorrectly required attention: %+v", overview)
	}
	warnings := 0
	for _, bucket := range overview.TaskOutcomes24Hours {
		warnings += bucket.Warnings
	}
	if warnings != 1 {
		t.Fatalf("shell warning missing from outcome history: %+v", overview.TaskOutcomes24Hours)
	}
}

func TestDisplayNameAndRetirementPersistAcrossRegistration(t *testing.T) {
	s := session.NewStore()
	s.Register(&session.Agent{ID: "a1", Secret: []byte("one"), DisplayName: "Initial"})
	if err := s.EnqueueTask("a1", &protocol.Task{ID: "queued-before-retirement", Type: "shell", Payload: "id"}); err != nil {
		t.Fatal(err)
	}
	updated, ok := s.UpdateMetadataWithName("a1", "Operator Name", "note", []string{"lab"})
	if !ok || updated.DisplayName != "Operator Name" {
		t.Fatalf("display name update failed: %#v", updated)
	}
	if _, ok := s.SetRetired("a1", true); !ok {
		t.Fatal("retire failed")
	}
	s.Register(&session.Agent{ID: "a1", Secret: []byte("two"), DisplayName: "Build Label"})
	agent, ok := s.Get("a1")
	if !ok || agent.DisplayName != "Operator Name" || !agent.Retired || agent.Status != "retired" {
		t.Fatalf("registration did not preserve operator metadata: %#v", agent)
	}
	if task := s.DeliverTask("a1"); task != nil {
		t.Fatalf("retired agent received queued work: %#v", task)
	}
	if err := s.EnqueueTask("a1", &protocol.Task{ID: "blocked", Type: "shell"}); err != session.ErrAgentRetired {
		t.Fatalf("enqueue on retired agent returned %v", err)
	}
	if _, ok := s.SetRetired("a1", false); !ok {
		t.Fatal("restore failed")
	}
	if task := s.DeliverTask("a1"); task == nil || task.ID != "queued-before-retirement" {
		t.Fatalf("restored agent did not resume queued work: %#v", task)
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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "t1", Type: "shell", Payload: "whoami"})
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
	if outs[0].Payload != "whoami" || outs[0].QueuedAt.IsZero() || outs[0].LastDeliveredAt.IsZero() {
		t.Fatalf("completed output should retain task timing and payload metadata: %+v", outs[0])
	}
	if outs[0].Timestamp.Before(outs[0].LastDeliveredAt) {
		t.Fatalf("completion timestamp precedes delivery: %+v", outs[0])
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
	if saved.SizeBytes != 5 {
		t.Fatalf("expected decoded artifact size 5, got %d", saved.SizeBytes)
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
	enqueueAndDeliver(t, s, "a1", &protocol.Task{ID: "download-1", Type: "download", Payload: `C:\evidence.txt`})
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
	if outputs[1].Payload != `C:\evidence.txt` || outputs[1].LastDeliveredAt.IsZero() {
		t.Fatalf("background output lost persisted task metadata: %+v", outputs[1])
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

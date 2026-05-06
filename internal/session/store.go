package session

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

// TaskOutput records the result of a completed task for the audit trail.
type TaskOutput struct {
	TaskID    string    `json:"task_id"`
	Type      string    `json:"type"`
	Output    string    `json:"output"`
	Error     string    `json:"error,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type TaskSummary struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Payload  string    `json:"payload,omitempty"`
	QueuedAt time.Time `json:"queued_at"`
}

type AuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	AgentID   string    `json:"agent_id,omitempty"`
	Action    string    `json:"action"`
	Detail    string    `json:"detail,omitempty"`
}

type Artifact struct {
	ID              string    `json:"id"`
	Key             string    `json:"key,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	Type            string    `json:"type,omitempty"`
	Label           string    `json:"label,omitempty"`
	Filename        string    `json:"filename"`
	ArchiveFilename string    `json:"archive_filename,omitempty"`
	MIME            string    `json:"mime,omitempty"`
	Data            string    `json:"data,omitempty"`
	Compress        bool      `json:"compress,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type queuedTask struct {
	task     *protocol.Task
	queuedAt time.Time
}

type resultChunkAssembly struct {
	taskType  string
	err       string
	parts     []string
	received  int
	bytes     int
	updatedAt time.Time
}

// Agent holds state for a connected implant.
// Secret is excluded from JSON to prevent it leaking through API responses.
type Agent struct {
	ID        string        `json:"id"`
	Secret    []byte        `json:"-"`
	Hostname  string        `json:"hostname"`
	OS        string        `json:"os"`
	Arch      string        `json:"arch"`
	Transport string        `json:"transport,omitempty"`
	LastSeen  time.Time     `json:"last_seen"`
	Notes     string        `json:"notes,omitempty"`
	Tags      []string      `json:"tags,omitempty"`
	Queued    []TaskSummary `json:"queued,omitempty"`
	Outputs   []TaskOutput  `json:"outputs,omitempty"`
	Artifacts []Artifact    `json:"artifacts,omitempty"`
	tasks     []*queuedTask
}

// Store is a concurrency-safe session store.
type Store struct {
	mu        sync.RWMutex
	agents    map[string]*Agent
	order     []string
	statePath string

	// subsMu guards subs independently from mu so RecordOutput can notify
	// subscribers after releasing the main lock, avoiding lock ordering issues.
	subsMu sync.Mutex
	subs   map[string][]chan struct{}

	chunks map[string]*resultChunkAssembly
	audit  []AuditEvent
}

const (
	maxQueuedTasksPerAgent = 64
	maxOutputsPerAgent     = 256
	maxArtifactsPerAgent   = 256
	maxAuditEvents         = 512
	maxChunkedOutputBytes  = 72 * 1024 * 1024
	chunkAssemblyTTL       = 10 * time.Minute
)

type persistedStoreState struct {
	Version int              `json:"version"`
	Order   []string         `json:"order"`
	Agents  []persistedAgent `json:"agents"`
	Audit   []AuditEvent     `json:"audit,omitempty"`
}

type persistedAgent struct {
	ID        string                `json:"id"`
	Secret    []byte                `json:"secret"`
	Hostname  string                `json:"hostname,omitempty"`
	OS        string                `json:"os,omitempty"`
	Arch      string                `json:"arch,omitempty"`
	Transport string                `json:"transport,omitempty"`
	LastSeen  time.Time             `json:"last_seen,omitempty"`
	Notes     string                `json:"notes,omitempty"`
	Tags      []string              `json:"tags,omitempty"`
	Queued    []persistedQueuedTask `json:"queued,omitempty"`
	Outputs   []TaskOutput          `json:"outputs,omitempty"`
	Artifacts []Artifact            `json:"artifacts,omitempty"`
}

type persistedQueuedTask struct {
	Task     *protocol.Task `json:"task"`
	QueuedAt time.Time      `json:"queued_at"`
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		agents: make(map[string]*Agent),
		order:  make([]string, 0),
		subs:   make(map[string][]chan struct{}),
		chunks: make(map[string]*resultChunkAssembly),
		audit:  make([]AuditEvent, 0),
	}
}

// NewPersistentStore returns a Store backed by a JSON state file. If path is
// empty, persistence is disabled and the store behaves like NewStore.
func NewPersistentStore(path string) (*Store, error) {
	s := NewStore()
	s.statePath = strings.TrimSpace(path)
	if s.statePath == "" {
		return s, nil
	}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	return s, nil
}

// Subscribe registers a buffered channel that receives a signal each time a new
// output is recorded for agentID. The caller must call Unsubscribe when done.
func (s *Store) Subscribe(agentID string) chan struct{} {
	ch := make(chan struct{}, 1)
	s.subsMu.Lock()
	s.subs[agentID] = append(s.subs[agentID], ch)
	s.subsMu.Unlock()
	return ch
}

// Unsubscribe removes a channel previously registered via Subscribe.
func (s *Store) Unsubscribe(agentID string, ch chan struct{}) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	subs := s.subs[agentID]
	for i, sub := range subs {
		if sub == ch {
			s.subs[agentID] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

// Register adds or replaces an agent session.
func (s *Store) Register(a *Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.agents[a.ID]; exists {
		a.Notes = existing.Notes
		a.Tags = cloneStrings(existing.Tags)
		a.tasks = existing.tasks
		a.Outputs = cloneOutputs(existing.Outputs)
		a.Artifacts = cloneArtifacts(existing.Artifacts, true)
	} else {
		s.order = append(s.order, a.ID)
	}
	s.agents[a.ID] = a
	s.appendAuditLocked(a.ID, "register", "agent registered")
	s.persistLocked()
}

// Get returns a value-copy snapshot of the Agent for id. ok is false if not found.
// Returning a copy (not a pointer) prevents callers from racing with concurrent
// UpdateInfo/RecordOutput writes after the read lock is released.
func (s *Store) Get(id string) (Agent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return Agent{}, false
	}
	out := cloneOutputs(a.Outputs)
	return Agent{
		ID:        a.ID,
		Hostname:  a.Hostname,
		OS:        a.OS,
		Arch:      a.Arch,
		Transport: a.Transport,
		LastSeen:  a.LastSeen,
		Notes:     a.Notes,
		Tags:      cloneStrings(a.Tags),
		Queued:    queuedSummaries(a.tasks),
		Outputs:   out,
		Artifacts: cloneArtifacts(a.Artifacts, false),
	}, true
}

// Secret returns only the pre-shared secret for an agent.
// Avoids exposing the full Agent struct when only the secret is needed.
func (s *Store) Secret(id string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[id]
	if !ok {
		return nil, false
	}
	return a.Secret, true
}

// UpdateInfo updates hostname, OS, arch, and last-seen from a beacon.
// Replaces the old Touch-only pattern so beacon metadata is kept current.
func (s *Store) UpdateInfo(id, hostname, osName, arch string) {
	s.UpdateInfoWithTransport(id, hostname, osName, arch, "")
}

// UpdateInfoWithTransport updates beacon metadata including the transport used
// for the most recent check-in.
func (s *Store) UpdateInfoWithTransport(id, hostname, osName, arch, transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[id]; ok {
		a.Hostname = hostname
		a.OS = osName
		a.Arch = arch
		if transport != "" {
			a.Transport = transport
		}
		a.LastSeen = time.Now()
	}
}

// RecordOutput appends a completed task result to the agent's output history and
// notifies any SSE subscribers. Chunked results are reassembled before they are
// recorded. It returns false while a chunked result is still incomplete.
func (s *Store) RecordOutput(agentID string, result *protocol.TaskResult) bool {
	s.mu.Lock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return true
	}

	complete := true
	if result.ChunkTotal > 1 {
		complete = s.recordChunkedOutputLocked(agentID, a, result)
	} else {
		appendOutputLocked(a, result)
	}
	if complete {
		s.persistLocked()
	}
	s.mu.Unlock()

	if !complete {
		return false
	}
	s.subsMu.Lock()
	for _, ch := range s.subs[agentID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.subsMu.Unlock()
	return true
}

func appendOutputLocked(a *Agent, result *protocol.TaskResult) {
	a.Outputs = append(a.Outputs, TaskOutput{
		TaskID:    result.TaskID,
		Type:      result.Type,
		Output:    result.Output,
		Error:     result.Error,
		Timestamp: time.Now(),
	})
	if len(a.Outputs) > maxOutputsPerAgent {
		a.Outputs = a.Outputs[len(a.Outputs)-maxOutputsPerAgent:]
	}
}

func (s *Store) recordChunkedOutputLocked(agentID string, a *Agent, result *protocol.TaskResult) bool {
	if hasOutputLocked(a, result.TaskID) {
		return true
	}
	if result.ChunkIndex < 0 || result.ChunkIndex >= result.ChunkTotal {
		appendOutputLocked(a, &protocol.TaskResult{
			TaskID: result.TaskID,
			Type:   result.Type,
			Error:  "invalid chunk metadata",
		})
		return true
	}

	s.evictExpiredChunksLocked(time.Now())
	key := chunkKey(agentID, result.TaskID)
	assembly, ok := s.chunks[key]
	if !ok || len(assembly.parts) != result.ChunkTotal || assembly.taskType != result.Type {
		assembly = &resultChunkAssembly{
			taskType:  result.Type,
			parts:     make([]string, result.ChunkTotal),
			updatedAt: time.Now(),
		}
		s.chunks[key] = assembly
	}

	if assembly.parts[result.ChunkIndex] == "" {
		assembly.parts[result.ChunkIndex] = result.Output
		assembly.received++
		assembly.bytes += len(result.Output)
	}
	assembly.updatedAt = time.Now()
	if result.Error != "" {
		assembly.err = result.Error
	}

	if assembly.bytes > maxChunkedOutputBytes {
		delete(s.chunks, key)
		appendOutputLocked(a, &protocol.TaskResult{
			TaskID: result.TaskID,
			Type:   result.Type,
			Error:  "chunked output exceeded maximum size",
		})
		return true
	}
	if assembly.received < len(assembly.parts) {
		return false
	}

	var output strings.Builder
	output.Grow(assembly.bytes)
	for _, part := range assembly.parts {
		output.WriteString(part)
	}
	delete(s.chunks, key)
	appendOutputLocked(a, &protocol.TaskResult{
		TaskID: result.TaskID,
		Type:   result.Type,
		Output: output.String(),
		Error:  assembly.err,
	})
	return true
}

func hasOutputLocked(a *Agent, taskID string) bool {
	for _, output := range a.Outputs {
		if output.TaskID == taskID {
			return true
		}
	}
	return false
}

func (s *Store) evictExpiredChunksLocked(now time.Time) {
	cutoff := now.Add(-chunkAssemblyTTL)
	for key, assembly := range s.chunks {
		if assembly.updatedAt.Before(cutoff) {
			delete(s.chunks, key)
		}
	}
}

func chunkKey(agentID, taskID string) string {
	return agentID + "\x00" + taskID
}

// GetOutputs returns a copy of the task output history for an agent.
func (s *Store) GetOutputs(agentID string) []TaskOutput {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return nil
	}
	return cloneOutputs(a.Outputs)
}

// ClearOutputs removes recorded task output history and incomplete output
// assemblies for an agent. It returns false when the agent does not exist.
func (s *Store) ClearOutputs(agentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return false
	}
	a.Outputs = nil
	prefix := agentID + "\x00"
	for key := range s.chunks {
		if strings.HasPrefix(key, prefix) {
			delete(s.chunks, key)
		}
	}
	s.appendAuditLocked(agentID, "clear_outputs", "task output history cleared")
	s.persistLocked()
	return true
}

// EnqueueTask adds a task to an agent's pending queue.
func (s *Store) EnqueueTask(agentID string, t *protocol.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[agentID]; ok {
		if len(a.tasks) >= maxQueuedTasksPerAgent {
			return errors.New("task queue full")
		}
		a.tasks = append(a.tasks, &queuedTask{task: t, queuedAt: time.Now()})
		s.appendAuditLocked(agentID, "queue_task", t.Type+" "+t.ID)
		s.persistLocked()
	}
	return nil
}

func (s *Store) RemoveQueuedTask(agentID, taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return false
	}
	for i, item := range a.tasks {
		if item.task.ID == taskID {
			a.tasks = append(a.tasks[:i], a.tasks[i+1:]...)
			s.appendAuditLocked(agentID, "remove_queued_task", item.task.Type+" "+taskID)
			s.persistLocked()
			return true
		}
	}
	return false
}

func (s *Store) GetQueuedTasks(agentID string) []TaskSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return nil
	}
	return queuedSummaries(a.tasks)
}

func (s *Store) UpdateMetadata(agentID, notes string, tags []string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return Agent{}, false
	}
	a.Notes = notes
	a.Tags = normalizeTags(tags)
	s.appendAuditLocked(agentID, "update_metadata", "notes/tags updated")
	s.persistLocked()
	return Agent{
		ID:        a.ID,
		Hostname:  a.Hostname,
		OS:        a.OS,
		Arch:      a.Arch,
		Transport: a.Transport,
		LastSeen:  a.LastSeen,
		Notes:     a.Notes,
		Tags:      cloneStrings(a.Tags),
		Queued:    queuedSummaries(a.tasks),
		Outputs:   cloneOutputs(a.Outputs),
		Artifacts: cloneArtifacts(a.Artifacts, false),
	}, true
}

func (s *Store) AuditLog() []AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AuditEvent, len(s.audit))
	copy(out, s.audit)
	return out
}

func (s *Store) AddArtifact(agentID string, artifact Artifact) (Artifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return Artifact{}, false
	}
	if artifact.Key != "" {
		for _, existing := range a.Artifacts {
			if existing.Key == artifact.Key {
				return artifactSummary(existing), true
			}
		}
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now()
	}
	if artifact.ArchiveFilename == "" {
		artifact.ArchiveFilename = artifact.Filename
	}
	a.Artifacts = append([]Artifact{artifact}, a.Artifacts...)
	if len(a.Artifacts) > maxArtifactsPerAgent {
		a.Artifacts = a.Artifacts[:maxArtifactsPerAgent]
	}
	s.appendAuditLocked(agentID, "save_artifact", artifact.Filename)
	s.persistLocked()
	return artifactSummary(artifact), true
}

func (s *Store) ListArtifacts(agentID string) []Artifact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return nil
	}
	return cloneArtifacts(a.Artifacts, false)
}

func (s *Store) GetArtifact(agentID, artifactID string) (Artifact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return Artifact{}, false
	}
	for _, artifact := range a.Artifacts {
		if artifact.ID == artifactID {
			return cloneArtifact(artifact, true), true
		}
	}
	return Artifact{}, false
}

// DequeueTask pops the next pending task for an agent, or nil if none.
func (s *Store) DequeueTask(agentID string) *protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok || len(a.tasks) == 0 {
		return nil
	}
	t := a.tasks[0].task
	a.tasks = a.tasks[1:]
	s.appendAuditLocked(agentID, "dequeue_task", t.Type+" "+t.ID)
	s.persistLocked()
	return t
}

// List returns a snapshot of all agents without secrets, task queues, or output history.
func (s *Store) List() []*Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Agent, 0, len(s.order))
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		out = append(out, &Agent{
			ID:        a.ID,
			Hostname:  a.Hostname,
			OS:        a.OS,
			Arch:      a.Arch,
			Transport: a.Transport,
			LastSeen:  a.LastSeen,
			Notes:     a.Notes,
			Tags:      cloneStrings(a.Tags),
			Queued:    queuedSummaries(a.tasks),
			Artifacts: cloneArtifacts(a.Artifacts, false),
		})
	}
	return out
}

func queuedSummaries(tasks []*queuedTask) []TaskSummary {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]TaskSummary, 0, len(tasks))
	for _, item := range tasks {
		if item == nil || item.task == nil {
			continue
		}
		out = append(out, TaskSummary{
			ID:       item.task.ID,
			Type:     item.task.Type,
			Payload:  taskPayloadSummary(item.task),
			QueuedAt: item.queuedAt,
		})
	}
	return out
}

func taskPayloadSummary(task *protocol.Task) string {
	if task == nil {
		return ""
	}
	if task.Type != "upload" {
		return task.Payload
	}
	idx := strings.LastIndexByte(task.Payload, ':')
	if idx <= 0 {
		return "[upload payload]"
	}
	return task.Payload[:idx] + ":<base64>"
}

func cloneOutputs(outputs []TaskOutput) []TaskOutput {
	out := make([]TaskOutput, len(outputs))
	copy(out, outputs)
	return out
}

func cloneArtifacts(artifacts []Artifact, includeData bool) []Artifact {
	out := make([]Artifact, len(artifacts))
	for i, artifact := range artifacts {
		out[i] = cloneArtifact(artifact, includeData)
	}
	return out
}

func cloneArtifact(artifact Artifact, includeData bool) Artifact {
	out := artifact
	if !includeData {
		out.Data = ""
	}
	return out
}

func artifactSummary(artifact Artifact) Artifact {
	return cloneArtifact(artifact, false)
}

func cloneStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func (s *Store) appendAuditLocked(agentID, action, detail string) {
	s.audit = append(s.audit, AuditEvent{
		Timestamp: time.Now(),
		AgentID:   agentID,
		Action:    action,
		Detail:    detail,
	})
	if len(s.audit) > maxAuditEvents {
		s.audit = s.audit[len(s.audit)-maxAuditEvents:]
	}
}

func (s *Store) loadState() error {
	data, err := os.ReadFile(s.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	var state persistedStoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	s.agents = make(map[string]*Agent)
	s.order = make([]string, 0, len(state.Agents))
	for _, a := range state.Agents {
		if a.ID == "" {
			continue
		}
		agent := &Agent{
			ID:        a.ID,
			Secret:    cloneBytes(a.Secret),
			Hostname:  a.Hostname,
			OS:        a.OS,
			Arch:      a.Arch,
			Transport: a.Transport,
			LastSeen:  a.LastSeen,
			Notes:     a.Notes,
			Tags:      cloneStrings(a.Tags),
			Outputs:   cloneOutputs(a.Outputs),
			Artifacts: cloneArtifacts(a.Artifacts, true),
		}
		for _, item := range a.Queued {
			if item.Task == nil || item.Task.ID == "" {
				continue
			}
			task := *item.Task
			agent.tasks = append(agent.tasks, &queuedTask{
				task:     &task,
				queuedAt: item.QueuedAt,
			})
		}
		s.agents[a.ID] = agent
	}

	for _, id := range state.Order {
		if _, ok := s.agents[id]; ok && !containsString(s.order, id) {
			s.order = append(s.order, id)
		}
	}
	for _, a := range state.Agents {
		if _, ok := s.agents[a.ID]; ok && !containsString(s.order, a.ID) {
			s.order = append(s.order, a.ID)
		}
	}

	s.audit = cloneAuditEvents(state.Audit)
	if len(s.audit) > maxAuditEvents {
		s.audit = s.audit[len(s.audit)-maxAuditEvents:]
	}
	return nil
}

func (s *Store) persistLocked() {
	if s.statePath == "" {
		return
	}
	if err := writeStateFile(s.statePath, s.snapshotLocked()); err != nil {
		log.Printf("session state persist failed: %v", err)
	}
}

func (s *Store) snapshotLocked() persistedStoreState {
	state := persistedStoreState{
		Version: 1,
		Order:   cloneStrings(s.order),
		Audit:   cloneAuditEvents(s.audit),
	}
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		agent := persistedAgent{
			ID:        a.ID,
			Secret:    cloneBytes(a.Secret),
			Hostname:  a.Hostname,
			OS:        a.OS,
			Arch:      a.Arch,
			Transport: a.Transport,
			LastSeen:  a.LastSeen,
			Notes:     a.Notes,
			Tags:      cloneStrings(a.Tags),
			Outputs:   cloneOutputs(a.Outputs),
			Artifacts: cloneArtifacts(a.Artifacts, true),
		}
		for _, item := range a.tasks {
			if item == nil || item.task == nil {
				continue
			}
			task := *item.task
			agent.Queued = append(agent.Queued, persistedQueuedTask{
				Task:     &task,
				QueuedAt: item.queuedAt,
			})
		}
		state.Agents = append(state.Agents, agent)
	}
	return state
}

func writeStateFile(path string, state persistedStoreState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if retryErr := os.Rename(tmpPath, path); retryErr != nil {
			return retryErr
		}
	}
	return nil
}

func cloneBytes(values []byte) []byte {
	out := make([]byte, len(values))
	copy(out, values)
	return out
}

func cloneAuditEvents(events []AuditEvent) []AuditEvent {
	out := make([]AuditEvent, len(events))
	copy(out, events)
	return out
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

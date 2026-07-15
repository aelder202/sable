package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/securefile"
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
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	Payload          string    `json:"payload,omitempty"`
	Status           string    `json:"status"`
	DeliveryAttempts int       `json:"delivery_attempts,omitempty"`
	QueuedAt         time.Time `json:"queued_at"`
	LastDeliveredAt  time.Time `json:"last_delivered_at,omitempty"`
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
	task             *protocol.Task
	queuedAt         time.Time
	deliveryAttempts int
	lastDeliveredAt  time.Time
}

type resultChunkAssembly struct {
	taskType  string
	err       string
	parts     []string
	seen      []bool
	received  int
	bytes     int
	updatedAt time.Time
}

// Agent holds state for a connected implant.
// Secret is excluded from JSON to prevent it leaking through API responses.
type Agent struct {
	ID                string        `json:"id"`
	Secret            []byte        `json:"-"`
	Hostname          string        `json:"hostname"`
	OS                string        `json:"os"`
	Arch              string        `json:"arch"`
	Transport         string        `json:"transport,omitempty"`
	LastSeen          time.Time     `json:"last_seen"`
	Notes             string        `json:"notes,omitempty"`
	Tags              []string      `json:"tags,omitempty"`
	Queued            []TaskSummary `json:"queued,omitempty"`
	Outputs           []TaskOutput  `json:"outputs,omitempty"`
	Artifacts         []Artifact    `json:"artifacts,omitempty"`
	ArtifactRetention int           `json:"artifact_retention,omitempty"`
	tasks             []*queuedTask
}

// Store is a concurrency-safe session store.
type Store struct {
	mu         sync.RWMutex
	artifactMu sync.Mutex
	agents     map[string]*Agent
	order      []string
	statePath  string
	stateKey   []byte
	blobDir    string

	// subsMu guards subs independently from mu so RecordOutput can notify
	// subscribers after releasing the main lock, avoiding lock ordering issues.
	subsMu sync.Mutex
	subs   map[string][]chan struct{}

	chunks map[string]*resultChunkAssembly
	audit  []AuditEvent

	persistCh   chan persistRequest
	persistDone chan struct{}
	closeOnce   sync.Once
}

const (
	maxQueuedTasksPerAgent = 64
	maxOutputsPerAgent     = 256
	maxArtifactsPerAgent   = 256
	maxAuditEvents         = 512
	maxChunkedOutputBytes  = 72 * 1024 * 1024
	maxResultChunks        = 256
	maxResultTaskIDBytes   = 128
	maxResultTypeBytes     = 64
	maxResultErrorBytes    = 64 * 1024
	maxResultChunkBytes    = 1 * 1024 * 1024
	chunkAssemblyTTL       = 10 * time.Minute
	persistDebounce        = 250 * time.Millisecond
)

var encryptedStateMagic = []byte("SABLE-STATE-ENC-v1\n")

type persistRequest struct {
	flush chan error
	stop  bool
}

type persistedStoreState struct {
	Version int              `json:"version"`
	Order   []string         `json:"order"`
	Agents  []persistedAgent `json:"agents"`
	Audit   []AuditEvent     `json:"audit,omitempty"`
}

type persistedAgent struct {
	ID                string                `json:"id"`
	Secret            []byte                `json:"secret"`
	Hostname          string                `json:"hostname,omitempty"`
	OS                string                `json:"os,omitempty"`
	Arch              string                `json:"arch,omitempty"`
	Transport         string                `json:"transport,omitempty"`
	LastSeen          time.Time             `json:"last_seen,omitempty"`
	Notes             string                `json:"notes,omitempty"`
	Tags              []string              `json:"tags,omitempty"`
	Queued            []persistedQueuedTask `json:"queued,omitempty"`
	Outputs           []TaskOutput          `json:"outputs,omitempty"`
	Artifacts         []Artifact            `json:"artifacts,omitempty"`
	ArtifactRetention int                   `json:"artifact_retention,omitempty"`
}

type persistedQueuedTask struct {
	Task             *protocol.Task `json:"task"`
	QueuedAt         time.Time      `json:"queued_at"`
	DeliveryAttempts int            `json:"delivery_attempts,omitempty"`
	LastDeliveredAt  time.Time      `json:"last_delivered_at,omitempty"`
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
	return NewPersistentStoreWithKey(path, nil)
}

// NewPersistentStoreWithKey returns a JSON state store optionally encrypted
// with a 32-byte AES-256-GCM key. Existing plaintext state is migrated on the
// next write when a key is supplied.
func NewPersistentStoreWithKey(path string, key []byte) (*Store, error) {
	s := NewStore()
	s.statePath = strings.TrimSpace(path)
	if len(key) != 0 && len(key) != 32 {
		return nil, errors.New("state encryption key must be 32 bytes")
	}
	s.stateKey = cloneBytes(key)
	if s.statePath == "" {
		return s, nil
	}
	s.blobDir = s.statePath + ".artifacts"
	if err := s.loadState(); err != nil {
		return nil, err
	}
	s.persistCh = make(chan persistRequest, 1)
	s.persistDone = make(chan struct{})
	go s.persistLoop()
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
		if a.Hostname == "" {
			a.Hostname = existing.Hostname
		}
		if a.OS == "" {
			a.OS = existing.OS
		}
		if a.Arch == "" {
			a.Arch = existing.Arch
		}
		if a.Transport == "" {
			a.Transport = existing.Transport
		}
		if a.LastSeen.IsZero() {
			a.LastSeen = existing.LastSeen
		}
		a.Notes = existing.Notes
		a.Tags = cloneStrings(existing.Tags)
		a.tasks = existing.tasks
		a.Outputs = cloneOutputs(existing.Outputs)
		a.Artifacts = cloneArtifacts(existing.Artifacts, true)
		a.ArtifactRetention = existing.ArtifactRetention
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
		ID:                a.ID,
		Hostname:          a.Hostname,
		OS:                a.OS,
		Arch:              a.Arch,
		Transport:         a.Transport,
		LastSeen:          a.LastSeen,
		Notes:             a.Notes,
		Tags:              cloneStrings(a.Tags),
		Queued:            queuedSummaries(a.tasks),
		Outputs:           out,
		Artifacts:         cloneArtifacts(a.Artifacts, false),
		ArtifactRetention: a.ArtifactRetention,
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
		s.persistLocked()
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

	if result == nil {
		s.mu.Unlock()
		return true
	}

	complete := true
	if validationErr := validateTaskResult(result); validationErr != "" {
		appendOutputLocked(a, &protocol.TaskResult{
			TaskID: truncateString(result.TaskID, maxResultTaskIDBytes),
			Type:   truncateString(result.Type, maxResultTypeBytes),
			Error:  validationErr,
		})
	} else if result.ChunkTotal > 1 {
		complete = s.recordChunkedOutputLocked(agentID, a, result)
	} else if !hasOutputLocked(a, result.TaskID) {
		appendOutputLocked(a, result)
	} else {
		// A response may be lost after the server records a result. The agent
		// retries that result on its next beacon; keep output history idempotent.
		complete = true
	}
	if complete {
		s.ackTaskLocked(agentID, a, result)
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

func validateTaskResult(result *protocol.TaskResult) string {
	if result.TaskID == "" || len(result.TaskID) > maxResultTaskIDBytes {
		return "invalid task result id"
	}
	if len(result.Type) > maxResultTypeBytes {
		return "invalid task result type"
	}
	if len(result.Error) > maxResultErrorBytes {
		return "task result error exceeded maximum size"
	}
	if len(result.Output) > maxResultChunkBytes {
		return "task result chunk exceeded maximum size"
	}
	if result.ChunkTotal < 0 || result.ChunkTotal > maxResultChunks {
		return "invalid chunk count"
	}
	if result.ChunkTotal > 1 && (result.ChunkIndex < 0 || result.ChunkIndex >= result.ChunkTotal) {
		return "invalid chunk metadata"
	}
	return ""
}

func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
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
	s.evictExpiredChunksLocked(time.Now())
	key := chunkKey(agentID, result.TaskID)
	assembly, ok := s.chunks[key]
	if !ok || len(assembly.parts) != result.ChunkTotal || assembly.taskType != result.Type {
		assembly = &resultChunkAssembly{
			taskType:  result.Type,
			parts:     make([]string, result.ChunkTotal),
			seen:      make([]bool, result.ChunkTotal),
			updatedAt: time.Now(),
		}
		s.chunks[key] = assembly
	}

	if !assembly.seen[result.ChunkIndex] {
		assembly.parts[result.ChunkIndex] = result.Output
		assembly.seen[result.ChunkIndex] = true
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
	a, ok := s.agents[agentID]
	if !ok {
		return errors.New("agent not found")
	}
	if len(a.tasks) >= maxQueuedTasksPerAgent {
		return errors.New("task queue full")
	}
	a.tasks = append(a.tasks, &queuedTask{task: t, queuedAt: time.Now()})
	s.appendAuditLocked(agentID, "queue_task", t.Type+" "+t.ID)
	s.persistLocked()
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
		ID:                a.ID,
		Hostname:          a.Hostname,
		OS:                a.OS,
		Arch:              a.Arch,
		Transport:         a.Transport,
		LastSeen:          a.LastSeen,
		Notes:             a.Notes,
		Tags:              cloneStrings(a.Tags),
		Queued:            queuedSummaries(a.tasks),
		Outputs:           cloneOutputs(a.Outputs),
		Artifacts:         cloneArtifacts(a.Artifacts, false),
		ArtifactRetention: a.ArtifactRetention,
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
	saved, ok, _ := s.AddArtifactChecked(agentID, artifact)
	return saved, ok
}

// AddArtifactChecked stores artifact metadata and, for persistent stores, puts
// the potentially large data body in a separate protected blob file.
func (s *Store) AddArtifactChecked(agentID string, artifact Artifact) (Artifact, bool, error) {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.mu.RLock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.RUnlock()
		return Artifact{}, false, nil
	}
	for _, existing := range a.Artifacts {
		if existing.ID == artifact.ID || (artifact.Key != "" && existing.Key == artifact.Key) {
			s.mu.RUnlock()
			return artifactSummary(existing), true, nil
		}
	}
	s.mu.RUnlock()

	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now()
	}
	if artifact.ArchiveFilename == "" {
		artifact.ArchiveFilename = artifact.Filename
	}
	stored := artifact
	if s.blobDir != "" {
		if err := s.writeArtifactBlob(agentID, artifact.ID, []byte(artifact.Data)); err != nil {
			return Artifact{}, true, err
		}
		stored.Data = ""
	}

	s.mu.Lock()
	a, ok = s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		s.removeArtifactBlob(agentID, artifact.ID)
		return Artifact{}, false, nil
	}
	for _, existing := range a.Artifacts {
		if existing.ID == artifact.ID || (artifact.Key != "" && existing.Key == artifact.Key) {
			s.mu.Unlock()
			s.removeArtifactBlob(agentID, artifact.ID)
			return artifactSummary(existing), true, nil
		}
	}
	a.Artifacts = append([]Artifact{stored}, a.Artifacts...)
	retention := artifactRetentionLimit(a)
	var removed []Artifact
	if len(a.Artifacts) > retention {
		removed = append(removed, a.Artifacts[retention:]...)
		a.Artifacts = a.Artifacts[:retention]
	}
	s.appendAuditLocked(agentID, "save_artifact", artifact.Filename)
	s.persistLocked()
	s.mu.Unlock()
	for _, expired := range removed {
		s.removeArtifactBlob(agentID, expired.ID)
	}
	return artifactSummary(artifact), true, nil
}

// SetArtifactRetention changes the retained artifact count for one agent and
// immediately removes the oldest excess entries.
func (s *Store) SetArtifactRetention(agentID string, limit int) bool {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.mu.Lock()
	a, ok := s.agents[agentID]
	if !ok || limit < 1 || limit > maxArtifactsPerAgent {
		s.mu.Unlock()
		return false
	}
	a.ArtifactRetention = limit
	var removed []Artifact
	if len(a.Artifacts) > limit {
		removed = append(removed, a.Artifacts[limit:]...)
		a.Artifacts = a.Artifacts[:limit]
	}
	s.appendAuditLocked(agentID, "artifact_retention", strconv.Itoa(limit))
	s.persistLocked()
	s.mu.Unlock()
	for _, artifact := range removed {
		s.removeArtifactBlob(agentID, artifact.ID)
	}
	return true
}

func artifactRetentionLimit(agent *Agent) int {
	if agent != nil && agent.ArtifactRetention > 0 && agent.ArtifactRetention <= maxArtifactsPerAgent {
		return agent.ArtifactRetention
	}
	return maxArtifactsPerAgent
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
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.mu.RLock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.RUnlock()
		return Artifact{}, false
	}
	for _, artifact := range a.Artifacts {
		if artifact.ID == artifactID {
			result := cloneArtifact(artifact, true)
			s.mu.RUnlock()
			if result.Data == "" && s.blobDir != "" {
				data, err := s.readArtifactBlob(agentID, artifactID)
				if err != nil {
					return Artifact{}, false
				}
				result.Data = string(data)
			}
			return result, true
		}
	}
	s.mu.RUnlock()
	return Artifact{}, false
}

// DeleteArtifact removes an artifact from an agent's retained library.
func (s *Store) DeleteArtifact(agentID, artifactID string) bool {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.mu.Lock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return false
	}
	for i, artifact := range a.Artifacts {
		if artifact.ID != artifactID {
			continue
		}
		a.Artifacts = append(a.Artifacts[:i], a.Artifacts[i+1:]...)
		s.appendAuditLocked(agentID, "delete_artifact", artifact.Filename)
		s.persistLocked()
		s.mu.Unlock()
		s.removeArtifactBlob(agentID, artifactID)
		return true
	}
	s.mu.Unlock()
	return false
}

// DeliverTask returns the oldest queued task without removing it. It remains
// in-flight and is retransmitted until RecordOutput observes an acknowledgment.
func (s *Store) DeliverTask(agentID string) *protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok || len(a.tasks) == 0 || a.tasks[0] == nil || a.tasks[0].task == nil {
		return nil
	}
	item := a.tasks[0]
	item.deliveryAttempts++
	item.lastDeliveredAt = time.Now()
	if item.deliveryAttempts == 1 {
		s.appendAuditLocked(agentID, "deliver_task", item.task.Type+" "+item.task.ID)
	} else if item.deliveryAttempts == 2 || item.deliveryAttempts&(item.deliveryAttempts-1) == 0 {
		s.appendAuditLocked(agentID, "retry_task", item.task.Type+" "+item.task.ID)
	}
	s.persistLocked()
	task := *item.task
	return &task
}

func (s *Store) ackTaskLocked(agentID string, a *Agent, result *protocol.TaskResult) bool {
	if result == nil || len(a.tasks) == 0 || a.tasks[0] == nil || a.tasks[0].task == nil {
		return false
	}
	item := a.tasks[0]
	if !resultAcknowledgesTask(result, item.task.ID) {
		return false
	}
	a.tasks = a.tasks[1:]
	s.appendAuditLocked(agentID, "ack_task", item.task.Type+" "+item.task.ID)
	if item.task.Type == "kill" && len(a.tasks) > 0 {
		dropped := len(a.tasks)
		a.tasks = nil
		s.appendAuditLocked(agentID, "cancel_after_kill", strconv.Itoa(dropped)+" queued task(s)")
	}
	return true
}

func resultAcknowledgesTask(result *protocol.TaskResult, taskID string) bool {
	if result.TaskID == taskID {
		return true
	}
	return strings.HasSuffix(result.Type, "_progress") && strings.HasPrefix(result.TaskID, taskID+"-")
}

// DequeueTask pops the next pending task for an agent, or nil if none.
// Deprecated: beacon listeners should use DeliverTask so delivery is reliable.
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

// RekeyAgent replaces an agent's pre-shared secret while retaining its history.
func (s *Store) RekeyAgent(agentID string, secret []byte) bool {
	if len(secret) != 32 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return false
	}
	a.Secret = cloneBytes(secret)
	s.appendAuditLocked(agentID, "rekey_agent", "agent secret rotated")
	s.persistLocked()
	return true
}

// DeleteAgent revokes an agent identity and removes its retained state.
func (s *Store) DeleteAgent(agentID string) bool {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.mu.Lock()
	if _, ok := s.agents[agentID]; !ok {
		s.mu.Unlock()
		return false
	}
	delete(s.agents, agentID)
	for i, id := range s.order {
		if id == agentID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	prefix := agentID + "\x00"
	for key := range s.chunks {
		if strings.HasPrefix(key, prefix) {
			delete(s.chunks, key)
		}
	}
	s.appendAuditLocked(agentID, "delete_agent", "agent identity revoked")
	s.persistLocked()
	s.mu.Unlock()
	if dir, err := s.artifactAgentDir(agentID); err == nil && dir != "" {
		_ = os.RemoveAll(dir)
	}
	return true
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
			ID:                a.ID,
			Hostname:          a.Hostname,
			OS:                a.OS,
			Arch:              a.Arch,
			Transport:         a.Transport,
			LastSeen:          a.LastSeen,
			Notes:             a.Notes,
			Tags:              cloneStrings(a.Tags),
			Queued:            queuedSummaries(a.tasks),
			Artifacts:         cloneArtifacts(a.Artifacts, false),
			ArtifactRetention: a.ArtifactRetention,
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
			ID:               item.task.ID,
			Type:             item.task.Type,
			Payload:          taskPayloadSummary(item.task),
			Status:           taskDeliveryStatus(item),
			DeliveryAttempts: item.deliveryAttempts,
			QueuedAt:         item.queuedAt,
			LastDeliveredAt:  item.lastDeliveredAt,
		})
	}
	return out
}

func taskDeliveryStatus(item *queuedTask) string {
	if item != nil && item.deliveryAttempts > 0 {
		return "in_flight"
	}
	return "queued"
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
	data, err = decodeStateData(data, s.stateKey)
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
			ID:                a.ID,
			Secret:            cloneBytes(a.Secret),
			Hostname:          a.Hostname,
			OS:                a.OS,
			Arch:              a.Arch,
			Transport:         a.Transport,
			LastSeen:          a.LastSeen,
			Notes:             a.Notes,
			Tags:              cloneStrings(a.Tags),
			Outputs:           cloneOutputs(a.Outputs),
			Artifacts:         cloneArtifacts(a.Artifacts, true),
			ArtifactRetention: a.ArtifactRetention,
		}
		if s.blobDir != "" {
			for i := range agent.Artifacts {
				if agent.Artifacts[i].Data == "" {
					if len(s.stateKey) > 0 {
						path, pathErr := s.artifactBlobPath(a.ID, agent.Artifacts[i].ID)
						blob, readErr := os.ReadFile(path)
						if pathErr == nil && readErr == nil && !bytes.HasPrefix(blob, encryptedStateMagic) {
							if err := s.writeArtifactBlob(a.ID, agent.Artifacts[i].ID, blob); err != nil {
								return fmt.Errorf("encrypt artifact %s: %w", agent.Artifacts[i].ID, err)
							}
						}
					}
					continue
				}
				if err := s.writeArtifactBlob(a.ID, agent.Artifacts[i].ID, []byte(agent.Artifacts[i].Data)); err != nil {
					return fmt.Errorf("migrate artifact %s: %w", agent.Artifacts[i].ID, err)
				}
				agent.Artifacts[i].Data = ""
			}
		}
		for _, item := range a.Queued {
			if item.Task == nil || item.Task.ID == "" {
				continue
			}
			task := *item.Task
			agent.tasks = append(agent.tasks, &queuedTask{
				task:             &task,
				queuedAt:         item.QueuedAt,
				deliveryAttempts: item.DeliveryAttempts,
				lastDeliveredAt:  item.LastDeliveredAt,
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
	if s.statePath == "" || s.persistCh == nil {
		return
	}
	select {
	case s.persistCh <- persistRequest{}:
	default:
	}
}

func (s *Store) persistLoop() {
	defer close(s.persistDone)
	var timer *time.Timer
	var timerC <-chan time.Time
	dirty := false
	stopTimer := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer = nil
		timerC = nil
	}
	write := func() error {
		if !dirty {
			return nil
		}
		err := s.persistSnapshot()
		if err != nil {
			log.Printf("session state persist failed: %v", err)
			return err
		}
		dirty = false
		return nil
	}

	for {
		select {
		case req := <-s.persistCh:
			dirty = true
			if req.flush != nil || req.stop {
				stopTimer()
				err := write()
				if req.flush != nil {
					req.flush <- err
				}
				if req.stop {
					return
				}
				continue
			}
			if timer == nil {
				timer = time.NewTimer(persistDebounce)
				timerC = timer.C
			}
		case <-timerC:
			timer = nil
			timerC = nil
			_ = write()
		}
	}
}

func (s *Store) persistSnapshot() error {
	s.mu.RLock()
	state := s.snapshotLocked()
	s.mu.RUnlock()
	return writeStateFile(s.statePath, state, s.stateKey)
}

// Flush waits until the current in-memory state has been written.
func (s *Store) Flush() error {
	if s.statePath == "" || s.persistCh == nil {
		return nil
	}
	done := make(chan error, 1)
	s.persistCh <- persistRequest{flush: done}
	return <-done
}

// Close flushes state and stops the persistence worker. It is idempotent.
func (s *Store) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.statePath == "" || s.persistCh == nil {
			return
		}
		done := make(chan error, 1)
		s.persistCh <- persistRequest{flush: done, stop: true}
		closeErr = <-done
		<-s.persistDone
	})
	return closeErr
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
			ID:                a.ID,
			Secret:            cloneBytes(a.Secret),
			Hostname:          a.Hostname,
			OS:                a.OS,
			Arch:              a.Arch,
			Transport:         a.Transport,
			LastSeen:          a.LastSeen,
			Notes:             a.Notes,
			Tags:              cloneStrings(a.Tags),
			Outputs:           cloneOutputs(a.Outputs),
			Artifacts:         cloneArtifacts(a.Artifacts, false),
			ArtifactRetention: a.ArtifactRetention,
		}
		for _, item := range a.tasks {
			if item == nil || item.task == nil {
				continue
			}
			task := *item.task
			agent.Queued = append(agent.Queued, persistedQueuedTask{
				Task:             &task,
				QueuedAt:         item.queuedAt,
				DeliveryAttempts: item.deliveryAttempts,
				LastDeliveredAt:  item.lastDeliveredAt,
			})
		}
		state.Agents = append(state.Agents, agent)
	}
	return state
}

func writeStateFile(path string, state persistedStoreState, key []byte) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(key) > 0 {
		data, err = encodeStateData(data, key)
		if err != nil {
			return err
		}
	}
	return writeSecureDataFile(path, data)
}

func (s *Store) writeArtifactBlob(agentID, artifactID string, data []byte) error {
	path, err := s.artifactBlobPath(agentID, artifactID)
	if err != nil || path == "" {
		return err
	}
	if len(s.stateKey) > 0 {
		data, err = encodeStateData(data, s.stateKey)
		if err != nil {
			return err
		}
	}
	return writeSecureDataFile(path, data)
}

func (s *Store) readArtifactBlob(agentID, artifactID string) ([]byte, error) {
	path, err := s.artifactBlobPath(agentID, artifactID)
	if err != nil || path == "" {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeStateData(data, s.stateKey)
}

func (s *Store) removeArtifactBlob(agentID, artifactID string) {
	path, err := s.artifactBlobPath(agentID, artifactID)
	if err == nil && path != "" {
		_ = os.Remove(path)
	}
}

func (s *Store) artifactAgentDir(agentID string) (string, error) {
	if s.blobDir == "" {
		return "", nil
	}
	if !safeStorageComponent(agentID) {
		return "", errors.New("invalid agent storage id")
	}
	return filepath.Join(s.blobDir, agentID), nil
}

func (s *Store) artifactBlobPath(agentID, artifactID string) (string, error) {
	dir, err := s.artifactAgentDir(agentID)
	if err != nil || dir == "" {
		return "", err
	}
	if !safeStorageComponent(artifactID) {
		return "", errors.New("invalid artifact storage id")
	}
	return filepath.Join(dir, artifactID+".blob"), nil
}

func safeStorageComponent(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func writeSecureDataFile(path string, data []byte) error {

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
	if err := securefile.Restrict(tmpPath); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Sync(); err != nil {
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
	return securefile.Restrict(path)
}

func encodeStateData(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, encryptedStateMagic...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, encryptedStateMagic)
	return out, nil
}

func decodeStateData(data, key []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, encryptedStateMagic) {
		return data, nil
	}
	if len(key) != 32 {
		return nil, errors.New("state file is encrypted; supply a 32-byte state key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	payload := data[len(encryptedStateMagic):]
	if len(payload) < gcm.NonceSize() {
		return nil, errors.New("encrypted state file is truncated")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, encryptedStateMagic)
	if err != nil {
		return nil, errors.New("decrypt state file: authentication failed")
	}
	return plaintext, nil
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

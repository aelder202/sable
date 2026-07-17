package session

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/securefile"
)

// TaskOutput records the result of a completed task for the audit trail.
type TaskOutput struct {
	TaskID          string    `json:"task_id"`
	Type            string    `json:"type"`
	Payload         string    `json:"payload,omitempty"`
	Output          string    `json:"output"`
	Error           string    `json:"error,omitempty"`
	QueuedAt        time.Time `json:"queued_at,omitempty"`
	LastDeliveredAt time.Time `json:"last_delivered_at,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
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
	SizeBytes       int64     `json:"size_bytes,omitempty"`
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
	DisplayName       string        `json:"display_name,omitempty"`
	Hostname          string        `json:"hostname"`
	OS                string        `json:"os"`
	Arch              string        `json:"arch"`
	Transport         string        `json:"transport,omitempty"`
	LastIP            string        `json:"last_ip,omitempty"`
	HostIP            string        `json:"host_ip,omitempty"`
	RegisteredAt      time.Time     `json:"registered_at,omitempty"`
	FirstSeen         time.Time     `json:"first_seen,omitempty"`
	LastSeen          time.Time     `json:"last_seen"`
	SleepSeconds      int           `json:"sleep_seconds,omitempty"`
	Retired           bool          `json:"retired,omitempty"`
	RetiredAt         time.Time     `json:"retired_at,omitempty"`
	Notes             string        `json:"notes,omitempty"`
	Tags              []string      `json:"tags,omitempty"`
	Queued            []TaskSummary `json:"queued,omitempty"`
	Outputs           []TaskOutput  `json:"outputs,omitempty"`
	Artifacts         []Artifact    `json:"artifacts,omitempty"`
	ArtifactRetention int           `json:"artifact_retention,omitempty"`
	Status            string        `json:"status,omitempty"`
	ExpectedNextSeen  time.Time     `json:"expected_next_seen,omitempty"`
	QueuedCount       int           `json:"queued_count"`
	RunningCount      int           `json:"running_count"`
	ActiveTransfers   int           `json:"active_transfers"`
	ArtifactCount     int           `json:"artifact_count"`
	LastResultAt      time.Time     `json:"last_result_at,omitempty"`
	LastResultStatus  string        `json:"last_result_status,omitempty"`
	tasks             []*queuedTask
	lastPersisted     time.Time
}

type AgentSummary struct {
	ID               string    `json:"id"`
	DisplayName      string    `json:"display_name,omitempty"`
	Hostname         string    `json:"hostname,omitempty"`
	OS               string    `json:"os,omitempty"`
	Arch             string    `json:"arch,omitempty"`
	Transport        string    `json:"transport,omitempty"`
	LastIP           string    `json:"last_ip,omitempty"`
	HostIP           string    `json:"host_ip,omitempty"`
	RegisteredAt     time.Time `json:"registered_at,omitempty"`
	FirstSeen        time.Time `json:"first_seen,omitempty"`
	LastSeen         time.Time `json:"last_seen,omitempty"`
	ExpectedNextSeen time.Time `json:"expected_next_seen,omitempty"`
	SleepSeconds     int       `json:"sleep_seconds,omitempty"`
	Status           string    `json:"status"`
	Retired          bool      `json:"retired,omitempty"`
	RetiredAt        time.Time `json:"retired_at,omitempty"`
	Tags             []string  `json:"tags,omitempty"`
	QueuedCount      int       `json:"queued_count"`
	RunningCount     int       `json:"running_count"`
	ActiveTransfers  int       `json:"active_transfers"`
	ArtifactCount    int       `json:"artifact_count"`
	LastResultAt     time.Time `json:"last_result_at,omitempty"`
	LastResultStatus string    `json:"last_result_status,omitempty"`
}

// ActiveJob is work that has been delivered to an agent and has not produced a
// terminal result yet. Tasks that are still waiting in the operator queue are
// intentionally excluded.
type ActiveJob struct {
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	AgentName  string    `json:"agent_name"`
	Type       string    `json:"type"`
	Payload    string    `json:"payload,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
}

type TaskOutcomeBucket struct {
	Start      time.Time `json:"start"`
	Successful int       `json:"successful"`
	Failed     int       `json:"failed"`
}

type FleetActivityEvent struct {
	Timestamp time.Time `json:"timestamp"`
	AgentID   string    `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Kind      string    `json:"kind"`
	TaskType  string    `json:"task_type,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

type FailureAlert struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	AgentID   string    `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	TaskID    string    `json:"task_id"`
	TaskType  string    `json:"task_type,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

type OverviewAlertState struct {
	Disposition string    `json:"disposition"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type FleetOverview struct {
	GeneratedAt         time.Time            `json:"generated_at"`
	Total               int                  `json:"total"`
	NeverSeen           int                  `json:"never_seen"`
	OnSchedule          int                  `json:"on_schedule"`
	Overdue             int                  `json:"overdue"`
	Offline             int                  `json:"offline"`
	Retired             int                  `json:"retired"`
	QueuedTasks         int                  `json:"queued_tasks"`
	RunningTasks        int                  `json:"running_tasks"`
	ActiveJobs          []ActiveJob          `json:"active_jobs"`
	ActiveTransfers     int                  `json:"active_transfers"`
	FailedLast24Hours   int                  `json:"failed_last_24_hours"`
	Agents              []AgentSummary       `json:"agents"`
	TaskOutcomes24Hours []TaskOutcomeBucket  `json:"task_outcomes_24h"`
	TaskOutcomes7Days   []TaskOutcomeBucket  `json:"task_outcomes_7d"`
	RecentActivity      []FleetActivityEvent `json:"recent_activity"`
	FailureAlerts       []FailureAlert       `json:"failure_alerts"`
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
	// background tracks tasks acknowledged by a progress result while their
	// asynchronous final result is still outstanding.
	background map[string]time.Time
	// backgroundTypes augments the persisted expiry map without changing its
	// backward-compatible representation.
	backgroundTypes     map[string]string
	backgroundMetadata  map[string]TaskSummary
	overviewAlertStates map[string]OverviewAlertState
	audit               []AuditEvent

	persistCh   chan persistRequest
	persistDone chan struct{}
	closeOnce   sync.Once
}

const (
	maxQueuedTasksPerAgent         = 64
	maxOutputsPerAgent             = 256
	maxArtifactsPerAgent           = 256
	maxAuditEvents                 = 512
	maxChunkedOutputBytes          = 72 * 1024 * 1024
	maxResultChunks                = 256
	maxResultTaskIDBytes           = 128
	maxResultTypeBytes             = 64
	maxResultErrorBytes            = 64 * 1024
	maxResultChunkBytes            = 1 * 1024 * 1024
	maxChunkAssemblies             = 256
	maxChunkAssembliesPerAgent     = 8
	maxChunkAssemblyBytes          = 256 * 1024 * 1024
	maxChunkAssemblyBytesPerAgent  = 80 * 1024 * 1024
	maxRetainedOutputBytesPerAgent = 128 * 1024 * 1024
	maxBackgroundTasks             = 4096
	maxBackgroundTasksPerAgent     = 64
	maxOverviewAlertStates         = 512
	chunkAssemblyTTL               = 10 * time.Minute
	backgroundTaskTTL              = 2 * time.Hour
	heartbeatPersistInterval       = 30 * time.Second
	persistDebounce                = 250 * time.Millisecond
)

var encryptedStateMagic = []byte("SABLE-STATE-ENC-v1\n")

var (
	ErrAgentNotFound = errors.New("agent not found")
	ErrAgentRetired  = errors.New("agent is retired")
	ErrTaskQueueFull = errors.New("task queue full")
)

type persistRequest struct {
	flush chan error
	stop  bool
}

type persistedStoreState struct {
	Version             int                           `json:"version"`
	Order               []string                      `json:"order"`
	Agents              []persistedAgent              `json:"agents"`
	Audit               []AuditEvent                  `json:"audit,omitempty"`
	OverviewAlertStates map[string]OverviewAlertState `json:"overview_alert_states,omitempty"`
}

type persistedAgent struct {
	ID                 string                 `json:"id"`
	Secret             []byte                 `json:"secret"`
	DisplayName        string                 `json:"display_name,omitempty"`
	Hostname           string                 `json:"hostname,omitempty"`
	OS                 string                 `json:"os,omitempty"`
	Arch               string                 `json:"arch,omitempty"`
	Transport          string                 `json:"transport,omitempty"`
	LastIP             string                 `json:"last_ip,omitempty"`
	HostIP             string                 `json:"host_ip,omitempty"`
	RegisteredAt       time.Time              `json:"registered_at,omitempty"`
	FirstSeen          time.Time              `json:"first_seen,omitempty"`
	LastSeen           time.Time              `json:"last_seen,omitempty"`
	SleepSeconds       int                    `json:"sleep_seconds,omitempty"`
	Retired            bool                   `json:"retired,omitempty"`
	RetiredAt          time.Time              `json:"retired_at,omitempty"`
	Notes              string                 `json:"notes,omitempty"`
	Tags               []string               `json:"tags,omitempty"`
	Queued             []persistedQueuedTask  `json:"queued,omitempty"`
	Outputs            []TaskOutput           `json:"outputs,omitempty"`
	Artifacts          []Artifact             `json:"artifacts,omitempty"`
	ArtifactRetention  int                    `json:"artifact_retention,omitempty"`
	Background         map[string]time.Time   `json:"background,omitempty"`
	BackgroundTypes    map[string]string      `json:"background_types,omitempty"`
	BackgroundMetadata map[string]TaskSummary `json:"background_metadata,omitempty"`
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
		agents:              make(map[string]*Agent),
		order:               make([]string, 0),
		subs:                make(map[string][]chan struct{}),
		chunks:              make(map[string]*resultChunkAssembly),
		background:          make(map[string]time.Time),
		backgroundTypes:     make(map[string]string),
		backgroundMetadata:  make(map[string]TaskSummary),
		overviewAlertStates: make(map[string]OverviewAlertState),
		audit:               make([]AuditEvent, 0),
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

// ValidatePersistentState verifies that an existing state file can be decoded
// and parsed with key without starting a store or modifying the file.
func ValidatePersistentState(path string, key []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if len(key) != 0 && len(key) != 32 {
		return errors.New("state encryption key must be 32 bytes")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err = decodeStateData(data, key)
	if err != nil {
		return err
	}
	var state persistedStoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}
	return nil
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
	now := time.Now()
	if existing, exists := s.agents[a.ID]; exists {
		if existing.DisplayName != "" {
			a.DisplayName = existing.DisplayName
		}
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
		if a.LastIP == "" {
			a.LastIP = existing.LastIP
		}
		if a.HostIP == "" {
			a.HostIP = existing.HostIP
		}
		if a.LastSeen.IsZero() {
			a.LastSeen = existing.LastSeen
		}
		a.RegisteredAt = existing.RegisteredAt
		a.FirstSeen = existing.FirstSeen
		a.SleepSeconds = existing.SleepSeconds
		a.Retired = existing.Retired
		a.RetiredAt = existing.RetiredAt
		a.Notes = existing.Notes
		a.Tags = cloneStrings(existing.Tags)
		a.tasks = existing.tasks
		a.Outputs = cloneOutputs(existing.Outputs)
		a.Artifacts = cloneArtifacts(existing.Artifacts, true)
		a.ArtifactRetention = existing.ArtifactRetention
		a.lastPersisted = existing.lastPersisted
	} else {
		if a.RegisteredAt.IsZero() {
			a.RegisteredAt = now
		}
		if a.FirstSeen.IsZero() && !a.LastSeen.IsZero() {
			a.FirstSeen = a.LastSeen
		}
		s.order = append(s.order, a.ID)
	}
	if a.RegisteredAt.IsZero() {
		a.RegisteredAt = now
	}
	s.agents[a.ID] = a
	s.appendAuditLocked(a.ID, "register", "agent registered")
	a.lastPersisted = now
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
	return s.agentSnapshotLocked(a, true, true, time.Now()), true
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
	s.UpdateInfoWithAddresses(id, hostname, osName, arch, "", "", "", 0)
}

// UpdateInfoWithTransport updates beacon metadata including the transport used
// for the most recent check-in.
func (s *Store) UpdateInfoWithTransport(id, hostname, osName, arch, transport string) {
	s.UpdateInfoWithAddresses(id, hostname, osName, arch, transport, "", "", 0)
}

// UpdateInfoWithRuntime updates beacon metadata, including the agent's current
// configured sleep interval. Older agents may report zero; in that case the
// last known interval is preserved.
func (s *Store) UpdateInfoWithRuntime(id, hostname, osName, arch, transport string, sleepSeconds int) {
	s.UpdateInfoWithAddresses(id, hostname, osName, arch, transport, "", "", sleepSeconds)
}

// UpdateInfoWithSource updates beacon metadata and records the authenticated
// network source used for the most recent check-in.
func (s *Store) UpdateInfoWithSource(id, hostname, osName, arch, transport, sourceIP string, sleepSeconds int) {
	s.UpdateInfoWithAddresses(id, hostname, osName, arch, transport, sourceIP, "", sleepSeconds)
}

// UpdateInfoWithAddresses records both the server-observed network source and
// the route-selected local address reported by the authenticated agent.
func (s *Store) UpdateInfoWithAddresses(id, hostname, osName, arch, transport, sourceIP, hostIP string, sleepSeconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[id]; ok {
		sourceIP = strings.TrimSpace(sourceIP)
		hostIP = strings.TrimSpace(hostIP)
		metadataChanged := a.Hostname != hostname || a.OS != osName || a.Arch != arch ||
			(transport != "" && a.Transport != transport) ||
			(sourceIP != "" && a.LastIP != sourceIP) ||
			(hostIP != "" && a.HostIP != hostIP) ||
			(sleepSeconds > 0 && a.SleepSeconds != sleepSeconds)
		a.Hostname = hostname
		a.OS = osName
		a.Arch = arch
		if transport != "" {
			a.Transport = transport
		}
		if sourceIP != "" {
			a.LastIP = sourceIP
		}
		if hostIP != "" {
			a.HostIP = hostIP
		}
		if sleepSeconds > 0 {
			a.SleepSeconds = sleepSeconds
		}
		now := time.Now()
		if a.RegisteredAt.IsZero() {
			a.RegisteredAt = now
			metadataChanged = true
		}
		if a.FirstSeen.IsZero() {
			a.FirstSeen = now
			metadataChanged = true
		}
		a.LastSeen = now
		if metadataChanged || a.lastPersisted.IsZero() || now.Sub(a.lastPersisted) >= heartbeatPersistInterval {
			a.lastPersisted = now
			s.persistLocked()
		}
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
	now := time.Now()
	s.evictExpiredStateLocked(now)
	if !s.resultMatchesKnownTaskLocked(agentID, a, result, now) {
		// Authenticated but unsolicited results must not allocate assemblies,
		// consume retained output, or acknowledge a different queued task.
		s.mu.Unlock()
		return true
	}

	complete := true
	if validationErr := validateTaskResult(result); validationErr != "" {
		s.appendOutputLocked(agentID, a, &protocol.TaskResult{
			TaskID: truncateString(result.TaskID, maxResultTaskIDBytes),
			Type:   truncateString(result.Type, maxResultTypeBytes),
			Error:  validationErr,
		})
	} else if result.ChunkTotal > 1 {
		complete = s.recordChunkedOutputLocked(agentID, a, result)
	} else if !hasOutputLocked(a, result.TaskID) {
		s.appendOutputLocked(agentID, a, result)
	} else {
		// A response may be lost after the server records a result. The agent
		// retries that result on its next beacon; keep output history idempotent.
		complete = true
	}
	if complete {
		s.ackTaskLocked(agentID, a, result)
		s.completeBackgroundTaskLocked(agentID, result)
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

func (s *Store) appendOutputLocked(agentID string, a *Agent, result *protocol.TaskResult) {
	metadata := s.taskMetadataForResultLocked(agentID, a, result)
	a.Outputs = append(a.Outputs, TaskOutput{
		TaskID:          result.TaskID,
		Type:            result.Type,
		Payload:         metadata.Payload,
		Output:          result.Output,
		Error:           result.Error,
		QueuedAt:        metadata.QueuedAt,
		LastDeliveredAt: metadata.LastDeliveredAt,
		Timestamp:       time.Now(),
	})
	trimOutputsLocked(a)
}

func (s *Store) taskMetadataForResultLocked(agentID string, a *Agent, result *protocol.TaskResult) TaskSummary {
	if result == nil {
		return TaskSummary{}
	}
	if len(a.tasks) > 0 && a.tasks[0] != nil && a.tasks[0].task != nil &&
		resultAcknowledgesTask(result, a.tasks[0].task.ID) {
		return queuedTaskSummary(a.tasks[0])
	}
	prefix := agentID + "\x00"
	for key, metadata := range s.backgroundMetadata {
		if strings.HasPrefix(key, prefix) && resultAcknowledgesTask(result, strings.TrimPrefix(key, prefix)) {
			return metadata
		}
	}
	return TaskSummary{}
}

func trimOutputsLocked(a *Agent) {
	retainedBytes := outputBytes(a.Outputs)
	for len(a.Outputs) > 0 && (len(a.Outputs) > maxOutputsPerAgent || retainedBytes > maxRetainedOutputBytesPerAgent) {
		retainedBytes -= outputByteSize(a.Outputs[0])
		a.Outputs = a.Outputs[1:]
	}
}

func outputBytes(outputs []TaskOutput) int {
	total := 0
	for _, output := range outputs {
		total += outputByteSize(output)
	}
	return total
}

func outputByteSize(output TaskOutput) int {
	return len(output.TaskID) + len(output.Type) + len(output.Payload) + len(output.Output) + len(output.Error)
}

func (s *Store) recordChunkedOutputLocked(agentID string, a *Agent, result *protocol.TaskResult) bool {
	if hasOutputLocked(a, result.TaskID) {
		return true
	}
	key := chunkKey(agentID, result.TaskID)
	assembly, ok := s.chunks[key]
	if ok && (len(assembly.parts) != result.ChunkTotal || assembly.taskType != result.Type) {
		delete(s.chunks, key)
		s.appendOutputLocked(agentID, a, &protocol.TaskResult{
			TaskID: result.TaskID,
			Type:   result.Type,
			Error:  "inconsistent chunk metadata",
		})
		return true
	}
	if !ok {
		if len(s.chunks) >= maxChunkAssemblies || s.chunkAssemblyCountLocked(agentID) >= maxChunkAssembliesPerAgent {
			s.appendOutputLocked(agentID, a, &protocol.TaskResult{
				TaskID: result.TaskID,
				Type:   result.Type,
				Error:  "too many incomplete chunked outputs",
			})
			return true
		}
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

	if assembly.bytes > maxChunkedOutputBytes ||
		s.chunkAssemblyBytesLocked("") > maxChunkAssemblyBytes ||
		s.chunkAssemblyBytesLocked(agentID) > maxChunkAssemblyBytesPerAgent {
		delete(s.chunks, key)
		s.appendOutputLocked(agentID, a, &protocol.TaskResult{
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
	s.appendOutputLocked(agentID, a, &protocol.TaskResult{
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

func (s *Store) evictExpiredStateLocked(now time.Time) {
	cutoff := now.Add(-chunkAssemblyTTL)
	for key, assembly := range s.chunks {
		if assembly.updatedAt.Before(cutoff) {
			delete(s.chunks, key)
		}
	}
	for key, expiresAt := range s.background {
		if !expiresAt.After(now) {
			delete(s.background, key)
			delete(s.backgroundTypes, key)
			delete(s.backgroundMetadata, key)
		}
	}
}

func (s *Store) chunkAssemblyCountLocked(agentID string) int {
	prefix := agentID + "\x00"
	count := 0
	for key := range s.chunks {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func (s *Store) chunkAssemblyBytesLocked(agentID string) int {
	prefix := agentID + "\x00"
	total := 0
	for key, assembly := range s.chunks {
		if agentID == "" || strings.HasPrefix(key, prefix) {
			total += assembly.bytes
		}
	}
	return total
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
		return ErrAgentNotFound
	}
	if a.Retired {
		return ErrAgentRetired
	}
	if len(a.tasks) >= maxQueuedTasksPerAgent {
		return ErrTaskQueueFull
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
	s.mu.RLock()
	a, ok := s.agents[agentID]
	name := ""
	if ok {
		name = a.DisplayName
	}
	s.mu.RUnlock()
	if !ok {
		return Agent{}, false
	}
	return s.UpdateMetadataWithName(agentID, name, notes, tags)
}

func (s *Store) UpdateMetadataWithName(agentID, displayName, notes string, tags []string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return Agent{}, false
	}
	a.DisplayName = strings.TrimSpace(displayName)
	a.Notes = notes
	a.Tags = normalizeTags(tags)
	s.appendAuditLocked(agentID, "update_metadata", "display name/notes/tags updated")
	s.persistLocked()
	return s.agentSnapshotLocked(a, true, true, time.Now()), true
}

func (s *Store) SetRetired(agentID string, retired bool) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok {
		return Agent{}, false
	}
	if a.Retired != retired {
		a.Retired = retired
		if retired {
			a.RetiredAt = time.Now()
			s.appendAuditLocked(agentID, "retire_agent", "agent archived from active fleet views")
		} else {
			a.RetiredAt = time.Time{}
			s.appendAuditLocked(agentID, "restore_agent", "agent restored to active fleet views")
		}
		s.persistLocked()
	}
	return s.agentSnapshotLocked(a, true, true, time.Now()), true
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
	if artifact.SizeBytes <= 0 && artifact.Data != "" {
		artifact.SizeBytes = encodedArtifactSize(artifact.Data)
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

func encodedArtifactSize(data string) int64 {
	data = strings.TrimSpace(data)
	if data == "" {
		return 0
	}
	padding := int64(0)
	if strings.HasSuffix(data, "==") {
		padding = 2
	} else if strings.HasSuffix(data, "=") {
		padding = 1
	}
	size := int64(len(data))*3/4 - padding
	if size < 0 {
		return 0
	}
	return size
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
	if !ok || a.Retired || len(a.tasks) == 0 || a.tasks[0] == nil || a.tasks[0].task == nil {
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
	if result.TaskID != item.task.ID {
		s.trackBackgroundTaskLocked(agentID, item, time.Now())
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

func (s *Store) resultMatchesKnownTaskLocked(agentID string, a *Agent, result *protocol.TaskResult, now time.Time) bool {
	if hasOutputLocked(a, result.TaskID) {
		return true
	}
	if len(a.tasks) > 0 && a.tasks[0] != nil && a.tasks[0].task != nil && a.tasks[0].deliveryAttempts > 0 && resultAcknowledgesTask(result, a.tasks[0].task.ID) {
		return true
	}
	prefix := agentID + "\x00"
	for key, expiresAt := range s.background {
		if !expiresAt.After(now) || !strings.HasPrefix(key, prefix) {
			continue
		}
		taskID := strings.TrimPrefix(key, prefix)
		if resultAcknowledgesTask(result, taskID) {
			s.background[key] = now.Add(backgroundTaskTTL)
			return true
		}
	}
	return false
}

func (s *Store) trackBackgroundTaskLocked(agentID string, item *queuedTask, now time.Time) {
	if item == nil || item.task == nil {
		return
	}
	taskID := item.task.ID
	taskType := item.task.Type
	key := chunkKey(agentID, taskID)
	if _, exists := s.background[key]; exists {
		s.background[key] = now.Add(backgroundTaskTTL)
		if taskType != "" {
			s.backgroundTypes[key] = taskType
		}
		s.backgroundMetadata[key] = queuedTaskSummary(item)
		return
	}

	prefix := agentID + "\x00"
	perAgent := 0
	globalOldestKey := ""
	globalOldestExpiry := time.Time{}
	agentOldestKey := ""
	agentOldestExpiry := time.Time{}
	for existingKey, expiresAt := range s.background {
		if globalOldestKey == "" || expiresAt.Before(globalOldestExpiry) {
			globalOldestKey, globalOldestExpiry = existingKey, expiresAt
		}
		if strings.HasPrefix(existingKey, prefix) {
			perAgent++
			if agentOldestKey == "" || expiresAt.Before(agentOldestExpiry) {
				agentOldestKey, agentOldestExpiry = existingKey, expiresAt
			}
		}
	}
	if perAgent >= maxBackgroundTasksPerAgent {
		delete(s.background, agentOldestKey)
		delete(s.backgroundTypes, agentOldestKey)
		delete(s.backgroundMetadata, agentOldestKey)
	} else if len(s.background) >= maxBackgroundTasks {
		delete(s.background, globalOldestKey)
		delete(s.backgroundTypes, globalOldestKey)
		delete(s.backgroundMetadata, globalOldestKey)
	}
	s.background[key] = now.Add(backgroundTaskTTL)
	if taskType != "" {
		s.backgroundTypes[key] = taskType
	}
	s.backgroundMetadata[key] = queuedTaskSummary(item)
}

func (s *Store) completeBackgroundTaskLocked(agentID string, result *protocol.TaskResult) {
	if result == nil {
		return
	}
	key := chunkKey(agentID, result.TaskID)
	delete(s.background, key)
	delete(s.backgroundTypes, key)
	delete(s.backgroundMetadata, key)
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
	for key := range s.background {
		if strings.HasPrefix(key, prefix) {
			delete(s.background, key)
			delete(s.backgroundTypes, key)
			delete(s.backgroundMetadata, key)
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
	now := time.Now()
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		snapshot := s.agentSnapshotLocked(a, false, true, now)
		out = append(out, &snapshot)
	}
	return out
}

// Overview returns lightweight, fleet-wide status and activity summaries. It
// intentionally omits output and artifact payload lists so frequent dashboard
// refreshes remain cheap as the fleet grows.
func (s *Store) Overview() FleetOverview {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	hourlyStart := now.Truncate(time.Hour).Add(-23 * time.Hour)
	dailyStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
	overview := FleetOverview{
		GeneratedAt:         now,
		Total:               len(s.agents),
		Agents:              make([]AgentSummary, 0, len(s.order)),
		TaskOutcomes24Hours: taskOutcomeBuckets(hourlyStart, 24, time.Hour),
		TaskOutcomes7Days:   taskOutcomeBuckets(dailyStart, 7, 24*time.Hour),
		RecentActivity:      make([]FleetActivityEvent, 0, 32),
		FailureAlerts:       make([]FailureAlert, 0, 8),
		ActiveJobs:          make([]ActiveJob, 0),
	}
	failedCutoff := now.Add(-24 * time.Hour)
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		summary := s.agentSummaryLocked(a, now)
		overview.Agents = append(overview.Agents, summary)
		overview.QueuedTasks += summary.QueuedCount
		overview.RunningTasks += summary.RunningCount
		overview.ActiveJobs = append(overview.ActiveJobs, s.activeJobsLocked(a, now)...)
		overview.ActiveTransfers += summary.ActiveTransfers
		switch summary.Status {
		case "never_seen":
			overview.NeverSeen++
		case "on_schedule":
			overview.OnSchedule++
		case "overdue":
			overview.Overdue++
		case "offline":
			overview.Offline++
		case "retired":
			overview.Retired++
		}
		for _, output := range a.Outputs {
			if strings.HasSuffix(output.Type, "_progress") {
				continue
			}
			if output.Error != "" && output.Timestamp.After(failedCutoff) && !a.Retired {
				alertID := failureAlertID(a.ID, output)
				if _, resolved := s.overviewAlertStates[alertID]; !resolved {
					overview.FailedLast24Hours++
					overview.FailureAlerts = append(overview.FailureAlerts, FailureAlert{
						ID:        alertID,
						Timestamp: output.Timestamp,
						AgentID:   a.ID,
						AgentName: agentOverviewName(a),
						TaskID:    output.TaskID,
						TaskType:  output.Type,
						Detail:    output.Error,
					})
				}
			}
			addTaskOutcome(overview.TaskOutcomes24Hours, output)
			addTaskOutcome(overview.TaskOutcomes7Days, output)
			if !a.Retired && !output.Timestamp.IsZero() {
				kind := "task_success"
				if output.Error != "" {
					kind = "task_failed"
				}
				overview.RecentActivity = append(overview.RecentActivity, FleetActivityEvent{
					Timestamp: output.Timestamp,
					AgentID:   a.ID,
					AgentName: agentOverviewName(a),
					Kind:      kind,
					TaskType:  output.Type,
					Detail:    output.Error,
				})
			}
		}
		if !a.Retired {
			for _, artifact := range a.Artifacts {
				if artifact.CreatedAt.IsZero() {
					continue
				}
				overview.RecentActivity = append(overview.RecentActivity, FleetActivityEvent{
					Timestamp: artifact.CreatedAt,
					AgentID:   a.ID,
					AgentName: agentOverviewName(a),
					Kind:      "artifact_received",
					Detail:    artifact.Filename,
				})
			}
			if transitionAt := agentStatusTransitionAt(a, summary.Status); !transitionAt.IsZero() {
				overview.RecentActivity = append(overview.RecentActivity, FleetActivityEvent{
					Timestamp: transitionAt,
					AgentID:   a.ID,
					AgentName: agentOverviewName(a),
					Kind:      "agent_" + summary.Status,
				})
			}
		}
	}
	sort.SliceStable(overview.RecentActivity, func(i, j int) bool {
		return overview.RecentActivity[i].Timestamp.After(overview.RecentActivity[j].Timestamp)
	})
	if len(overview.RecentActivity) > 32 {
		overview.RecentActivity = overview.RecentActivity[:32]
	}
	sort.SliceStable(overview.FailureAlerts, func(i, j int) bool {
		return overview.FailureAlerts[i].Timestamp.After(overview.FailureAlerts[j].Timestamp)
	})
	if len(overview.FailureAlerts) > 64 {
		overview.FailureAlerts = overview.FailureAlerts[:64]
	}
	sort.SliceStable(overview.ActiveJobs, func(i, j int) bool {
		return overview.ActiveJobs[i].ReceivedAt.After(overview.ActiveJobs[j].ReceivedAt)
	})
	return overview
}

// ResolveFailureAlert removes one failure from Overview attention and counters
// without mutating the agent's retained task output or historical outcome data.
func (s *Store) ResolveFailureAlert(alertID, disposition string) bool {
	if disposition != "acknowledged" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var agentID, detail string
	found := false
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		for _, output := range a.Outputs {
			if output.Error == "" || failureAlertID(a.ID, output) != alertID {
				continue
			}
			agentID = a.ID
			detail = output.Type + " " + output.TaskID
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return false
	}
	if _, exists := s.overviewAlertStates[alertID]; !exists && len(s.overviewAlertStates) >= maxOverviewAlertStates {
		oldestID := ""
		var oldest time.Time
		for id, state := range s.overviewAlertStates {
			if oldestID == "" || state.UpdatedAt.Before(oldest) {
				oldestID = id
				oldest = state.UpdatedAt
			}
		}
		delete(s.overviewAlertStates, oldestID)
	}
	s.overviewAlertStates[alertID] = OverviewAlertState{
		Disposition: disposition,
		UpdatedAt:   time.Now(),
	}
	s.appendAuditLocked(agentID, "acknowledge_failure_alert", detail)
	s.persistLocked()
	return true
}

func failureAlertID(agentID string, output TaskOutput) string {
	value := agentID + "\x00" + output.TaskID + "\x00" + output.Timestamp.UTC().Format(time.RFC3339Nano)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func taskOutcomeBuckets(start time.Time, count int, width time.Duration) []TaskOutcomeBucket {
	buckets := make([]TaskOutcomeBucket, count)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * width)
	}
	return buckets
}

func addTaskOutcome(buckets []TaskOutcomeBucket, output TaskOutput) {
	if output.Timestamp.IsZero() || len(buckets) == 0 {
		return
	}
	for i := range buckets {
		start := buckets[i].Start
		var end time.Time
		if i+1 < len(buckets) {
			end = buckets[i+1].Start
		} else if len(buckets) > 1 {
			end = start.Add(start.Sub(buckets[i-1].Start))
		} else {
			end = start.Add(time.Hour)
		}
		if output.Timestamp.Before(start) || !output.Timestamp.Before(end) {
			continue
		}
		if output.Error != "" {
			buckets[i].Failed++
		} else {
			buckets[i].Successful++
		}
		return
	}
}

func agentOverviewName(a *Agent) string {
	switch {
	case a.DisplayName != "":
		return a.DisplayName
	case a.Hostname != "":
		return a.Hostname
	default:
		return a.ID
	}
}

func agentStatusTransitionAt(a *Agent, status string) time.Time {
	if a == nil || a.LastSeen.IsZero() {
		return time.Time{}
	}
	if a.SleepSeconds <= 0 {
		switch status {
		case "overdue":
			return a.LastSeen.Add(3 * time.Minute)
		case "offline":
			return a.LastSeen.Add(10 * time.Minute)
		default:
			return time.Time{}
		}
	}
	interval := time.Duration(a.SleepSeconds) * time.Second
	expected := a.LastSeen.Add(interval + interval/5)
	switch status {
	case "overdue":
		grace := interval / 2
		if grace < 30*time.Second {
			grace = 30 * time.Second
		}
		return expected.Add(grace)
	case "offline":
		offlineAfter := 4 * interval
		if offlineAfter < 10*time.Minute {
			offlineAfter = 10 * time.Minute
		}
		return a.LastSeen.Add(offlineAfter)
	default:
		return time.Time{}
	}
}

func (s *Store) agentSnapshotLocked(a *Agent, includeOutputs, includeArtifacts bool, now time.Time) Agent {
	summary := s.agentSummaryLocked(a, now)
	result := Agent{
		ID:                summary.ID,
		DisplayName:       summary.DisplayName,
		Hostname:          summary.Hostname,
		OS:                summary.OS,
		Arch:              summary.Arch,
		Transport:         summary.Transport,
		LastIP:            summary.LastIP,
		HostIP:            summary.HostIP,
		RegisteredAt:      summary.RegisteredAt,
		FirstSeen:         summary.FirstSeen,
		LastSeen:          summary.LastSeen,
		SleepSeconds:      summary.SleepSeconds,
		Retired:           summary.Retired,
		RetiredAt:         summary.RetiredAt,
		Notes:             a.Notes,
		Tags:              cloneStrings(a.Tags),
		Queued:            queuedSummaries(a.tasks),
		ArtifactRetention: a.ArtifactRetention,
		Status:            summary.Status,
		ExpectedNextSeen:  summary.ExpectedNextSeen,
		QueuedCount:       summary.QueuedCount,
		RunningCount:      summary.RunningCount,
		ActiveTransfers:   summary.ActiveTransfers,
		ArtifactCount:     summary.ArtifactCount,
		LastResultAt:      summary.LastResultAt,
		LastResultStatus:  summary.LastResultStatus,
	}
	if includeOutputs {
		result.Outputs = cloneOutputs(a.Outputs)
	}
	if includeArtifacts {
		result.Artifacts = cloneArtifacts(a.Artifacts, false)
	}
	return result
}

func (s *Store) agentSummaryLocked(a *Agent, now time.Time) AgentSummary {
	status, expected := agentScheduleStatus(a, now)
	activeJobs := s.activeJobsLocked(a, now)
	_, transfers := s.backgroundCountsLocked(a.ID, now)
	queued := 0
	for _, item := range a.tasks {
		if item != nil && item.task != nil && item.deliveryAttempts == 0 {
			queued++
		}
	}
	lastResultAt := time.Time{}
	lastResultStatus := ""
	if len(a.Outputs) > 0 {
		last := a.Outputs[len(a.Outputs)-1]
		lastResultAt = last.Timestamp
		switch {
		case last.Error != "":
			lastResultStatus = "failed"
		case strings.HasSuffix(last.Type, "_progress"):
			lastResultStatus = "progress"
		default:
			lastResultStatus = "success"
		}
	}
	return AgentSummary{
		ID:               a.ID,
		DisplayName:      a.DisplayName,
		Hostname:         a.Hostname,
		OS:               a.OS,
		Arch:             a.Arch,
		Transport:        a.Transport,
		LastIP:           a.LastIP,
		HostIP:           a.HostIP,
		RegisteredAt:     a.RegisteredAt,
		FirstSeen:        a.FirstSeen,
		LastSeen:         a.LastSeen,
		ExpectedNextSeen: expected,
		SleepSeconds:     a.SleepSeconds,
		Status:           status,
		Retired:          a.Retired,
		RetiredAt:        a.RetiredAt,
		Tags:             cloneStrings(a.Tags),
		QueuedCount:      queued,
		RunningCount:     len(activeJobs),
		ActiveTransfers:  transfers,
		ArtifactCount:    len(a.Artifacts),
		LastResultAt:     lastResultAt,
		LastResultStatus: lastResultStatus,
	}
}

func (s *Store) activeJobsLocked(a *Agent, now time.Time) []ActiveJob {
	if a == nil {
		return nil
	}
	name := agentOverviewName(a)
	jobs := make([]ActiveJob, 0)
	seen := make(map[string]struct{})
	for _, item := range a.tasks {
		if item == nil || item.task == nil || item.deliveryAttempts == 0 {
			continue
		}
		jobs = append(jobs, ActiveJob{
			ID:         item.task.ID,
			AgentID:    a.ID,
			AgentName:  name,
			Type:       item.task.Type,
			Payload:    truncateString(taskPayloadSummary(item.task), 512),
			ReceivedAt: item.lastDeliveredAt,
		})
		seen[item.task.ID] = struct{}{}
	}

	prefix := a.ID + "\x00"
	for key, expiresAt := range s.background {
		if !strings.HasPrefix(key, prefix) || !expiresAt.After(now) {
			continue
		}
		taskID := strings.TrimPrefix(key, prefix)
		if _, exists := seen[taskID]; exists {
			continue
		}
		metadata := s.backgroundMetadata[key]
		taskType := metadata.Type
		if taskType == "" {
			taskType = s.backgroundTypes[key]
		}
		jobs = append(jobs, ActiveJob{
			ID:         taskID,
			AgentID:    a.ID,
			AgentName:  name,
			Type:       taskType,
			Payload:    truncateString(metadata.Payload, 512),
			ReceivedAt: metadata.LastDeliveredAt,
		})
	}
	return jobs
}

func (s *Store) backgroundCountsLocked(agentID string, now time.Time) (int, int) {
	prefix := agentID + "\x00"
	running := 0
	transfers := 0
	for key, expiresAt := range s.background {
		if !strings.HasPrefix(key, prefix) || !expiresAt.After(now) {
			continue
		}
		running++
		switch s.backgroundTypes[key] {
		case "download", "download_archive":
			transfers++
		}
	}
	return running, transfers
}

func agentScheduleStatus(a *Agent, now time.Time) (string, time.Time) {
	if a.Retired {
		return "retired", time.Time{}
	}
	if a.FirstSeen.IsZero() || a.LastSeen.IsZero() {
		return "never_seen", time.Time{}
	}
	if a.SleepSeconds <= 0 {
		age := now.Sub(a.LastSeen)
		switch {
		case age <= 3*time.Minute:
			return "on_schedule", a.LastSeen.Add(3 * time.Minute)
		case age <= 10*time.Minute:
			return "overdue", a.LastSeen.Add(3 * time.Minute)
		default:
			return "offline", a.LastSeen.Add(3 * time.Minute)
		}
	}
	interval := time.Duration(a.SleepSeconds) * time.Second
	expected := a.LastSeen.Add(interval + interval/5)
	grace := interval / 2
	if grace < 30*time.Second {
		grace = 30 * time.Second
	}
	if !now.After(expected.Add(grace)) {
		return "on_schedule", expected
	}
	offlineAfter := 4 * interval
	if offlineAfter < 10*time.Minute {
		offlineAfter = 10 * time.Minute
	}
	if now.Sub(a.LastSeen) <= offlineAfter {
		return "overdue", expected
	}
	return "offline", expected
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
		out = append(out, queuedTaskSummary(item))
	}
	return out
}

func queuedTaskSummary(item *queuedTask) TaskSummary {
	if item == nil || item.task == nil {
		return TaskSummary{}
	}
	return TaskSummary{
		ID:               item.task.ID,
		Type:             item.task.Type,
		Payload:          taskPayloadSummary(item.task),
		Status:           taskDeliveryStatus(item),
		DeliveryAttempts: item.deliveryAttempts,
		QueuedAt:         item.queuedAt,
		LastDeliveredAt:  item.lastDeliveredAt,
	}
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
	s.background = make(map[string]time.Time)
	s.backgroundTypes = make(map[string]string)
	s.backgroundMetadata = make(map[string]TaskSummary)
	s.overviewAlertStates = make(map[string]OverviewAlertState)
	s.order = make([]string, 0, len(state.Agents))
	now := time.Now()
	for _, a := range state.Agents {
		if a.ID == "" {
			continue
		}
		registeredAt := a.RegisteredAt
		if registeredAt.IsZero() {
			registeredAt = a.LastSeen
			if registeredAt.IsZero() {
				registeredAt = now
			}
		}
		firstSeen := a.FirstSeen
		if firstSeen.IsZero() && !a.LastSeen.IsZero() {
			firstSeen = a.LastSeen
		}
		agent := &Agent{
			ID:                a.ID,
			Secret:            cloneBytes(a.Secret),
			DisplayName:       a.DisplayName,
			Hostname:          a.Hostname,
			OS:                a.OS,
			Arch:              a.Arch,
			Transport:         a.Transport,
			LastIP:            a.LastIP,
			HostIP:            a.HostIP,
			RegisteredAt:      registeredAt,
			FirstSeen:         firstSeen,
			LastSeen:          a.LastSeen,
			SleepSeconds:      a.SleepSeconds,
			Retired:           a.Retired,
			RetiredAt:         a.RetiredAt,
			Notes:             a.Notes,
			Tags:              cloneStrings(a.Tags),
			Outputs:           cloneOutputs(a.Outputs),
			Artifacts:         cloneArtifacts(a.Artifacts, true),
			ArtifactRetention: a.ArtifactRetention,
			lastPersisted:     a.LastSeen,
		}
		trimOutputsLocked(agent)
		if s.blobDir != "" {
			for i := range agent.Artifacts {
				if agent.Artifacts[i].SizeBytes <= 0 && agent.Artifacts[i].Data != "" {
					agent.Artifacts[i].SizeBytes = encodedArtifactSize(agent.Artifacts[i].Data)
				}
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
		backgroundCount := 0
		for taskID, expiresAt := range a.Background {
			if backgroundCount >= maxBackgroundTasksPerAgent || len(s.background) >= maxBackgroundTasks || taskID == "" || len(taskID) > maxResultTaskIDBytes || !expiresAt.After(now) {
				continue
			}
			key := chunkKey(a.ID, taskID)
			s.background[key] = expiresAt
			if taskType := a.BackgroundTypes[taskID]; taskType != "" {
				s.backgroundTypes[key] = taskType
			}
			if metadata, ok := a.BackgroundMetadata[taskID]; ok {
				s.backgroundMetadata[key] = metadata
			}
			backgroundCount++
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
	for id, alertState := range state.OverviewAlertStates {
		if len(s.overviewAlertStates) >= maxOverviewAlertStates {
			break
		}
		if len(id) != 32 || (alertState.Disposition != "acknowledged" && alertState.Disposition != "cleared") {
			continue
		}
		s.overviewAlertStates[id] = alertState
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
		Version:             1,
		Order:               cloneStrings(s.order),
		Audit:               cloneAuditEvents(s.audit),
		OverviewAlertStates: cloneOverviewAlertStates(s.overviewAlertStates),
	}
	for _, id := range s.order {
		a, ok := s.agents[id]
		if !ok {
			continue
		}
		agent := persistedAgent{
			ID:                a.ID,
			Secret:            cloneBytes(a.Secret),
			DisplayName:       a.DisplayName,
			Hostname:          a.Hostname,
			OS:                a.OS,
			Arch:              a.Arch,
			Transport:         a.Transport,
			LastIP:            a.LastIP,
			HostIP:            a.HostIP,
			RegisteredAt:      a.RegisteredAt,
			FirstSeen:         a.FirstSeen,
			LastSeen:          a.LastSeen,
			SleepSeconds:      a.SleepSeconds,
			Retired:           a.Retired,
			RetiredAt:         a.RetiredAt,
			Notes:             a.Notes,
			Tags:              cloneStrings(a.Tags),
			Outputs:           cloneOutputs(a.Outputs),
			Artifacts:         cloneArtifacts(a.Artifacts, false),
			ArtifactRetention: a.ArtifactRetention,
		}
		prefix := a.ID + "\x00"
		for key, expiresAt := range s.background {
			if strings.HasPrefix(key, prefix) && expiresAt.After(time.Now()) {
				if agent.Background == nil {
					agent.Background = make(map[string]time.Time)
				}
				taskID := strings.TrimPrefix(key, prefix)
				agent.Background[taskID] = expiresAt
				if taskType := s.backgroundTypes[key]; taskType != "" {
					if agent.BackgroundTypes == nil {
						agent.BackgroundTypes = make(map[string]string)
					}
					agent.BackgroundTypes[taskID] = taskType
				}
				if metadata, ok := s.backgroundMetadata[key]; ok {
					if agent.BackgroundMetadata == nil {
						agent.BackgroundMetadata = make(map[string]TaskSummary)
					}
					agent.BackgroundMetadata[taskID] = metadata
				}
			}
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
	return securefile.WriteFile(path, data)
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

func cloneOverviewAlertStates(states map[string]OverviewAlertState) map[string]OverviewAlertState {
	out := make(map[string]OverviewAlertState, len(states))
	for id, state := range states {
		out[id] = state
	}
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

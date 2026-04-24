package session

import (
	"errors"
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

// Agent holds state for a connected implant.
// Secret is excluded from JSON to prevent it leaking through API responses.
type Agent struct {
	ID       string       `json:"id"`
	Secret   []byte       `json:"-"`
	Hostname string       `json:"hostname"`
	OS       string       `json:"os"`
	Arch     string       `json:"arch"`
	LastSeen time.Time    `json:"last_seen"`
	Outputs  []TaskOutput `json:"outputs,omitempty"`
	tasks    []*protocol.Task
}

// Store is a concurrency-safe in-memory session store.
type Store struct {
	mu     sync.RWMutex
	agents map[string]*Agent
	order  []string

	// subsMu guards subs independently from mu so RecordOutput can notify
	// subscribers after releasing the main lock, avoiding lock ordering issues.
	subsMu sync.Mutex
	subs   map[string][]chan struct{}
}

const (
	maxQueuedTasksPerAgent = 64
	maxOutputsPerAgent     = 256
)

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{
		agents: make(map[string]*Agent),
		order:  make([]string, 0),
		subs:   make(map[string][]chan struct{}),
	}
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
	if _, exists := s.agents[a.ID]; !exists {
		s.order = append(s.order, a.ID)
	}
	s.agents[a.ID] = a
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
	out := make([]TaskOutput, len(a.Outputs))
	copy(out, a.Outputs)
	return Agent{
		ID:       a.ID,
		Hostname: a.Hostname,
		OS:       a.OS,
		Arch:     a.Arch,
		LastSeen: a.LastSeen,
		Outputs:  out,
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[id]; ok {
		a.Hostname = hostname
		a.OS = osName
		a.Arch = arch
		a.LastSeen = time.Now()
	}
}

// RecordOutput appends a completed task result to the agent's output history and
// notifies any SSE subscribers. The main lock is released before signalling
// subscribers to avoid lock-ordering deadlocks.
func (s *Store) RecordOutput(agentID string, result *protocol.TaskResult) {
	s.mu.Lock()
	a, ok := s.agents[agentID]
	if !ok {
		s.mu.Unlock()
		return
	}
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
	s.mu.Unlock()

	s.subsMu.Lock()
	for _, ch := range s.subs[agentID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.subsMu.Unlock()
}

// GetOutputs returns a copy of the task output history for an agent.
func (s *Store) GetOutputs(agentID string) []TaskOutput {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.agents[agentID]
	if !ok {
		return nil
	}
	out := make([]TaskOutput, len(a.Outputs))
	copy(out, a.Outputs)
	return out
}

// EnqueueTask adds a task to an agent's pending queue.
func (s *Store) EnqueueTask(agentID string, t *protocol.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.agents[agentID]; ok {
		if len(a.tasks) >= maxQueuedTasksPerAgent {
			return errors.New("task queue full")
		}
		a.tasks = append(a.tasks, t)
	}
	return nil
}

// DequeueTask pops the next pending task for an agent, or nil if none.
func (s *Store) DequeueTask(agentID string) *protocol.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentID]
	if !ok || len(a.tasks) == 0 {
		return nil
	}
	t := a.tasks[0]
	a.tasks = a.tasks[1:]
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
			ID:       a.ID,
			Hostname: a.Hostname,
			OS:       a.OS,
			Arch:     a.Arch,
			LastSeen: a.LastSeen,
		})
	}
	return out
}

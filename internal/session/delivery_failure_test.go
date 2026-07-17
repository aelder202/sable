package session

import (
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

func TestOverviewFailsInFlightTaskAfterOfflineThreshold(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Register(&Agent{
		ID:           "offline-agent",
		Secret:       []byte("secret"),
		FirstSeen:    now.Add(-time.Hour),
		LastSeen:     now.Add(-11 * time.Minute),
		SleepSeconds: 30,
	})
	if err := s.EnqueueTask("offline-agent", &protocol.Task{
		ID:      "lost-task",
		Type:    "shell",
		Payload: "whoami",
	}); err != nil {
		t.Fatal(err)
	}
	if delivered := s.DeliverTask("offline-agent"); delivered == nil {
		t.Fatal("task was not delivered")
	}

	s.mu.Lock()
	s.agents["offline-agent"].tasks[0].lastDeliveredAt = now.Add(-11 * time.Minute)
	s.mu.Unlock()

	overview := s.Overview()
	if overview.FailedLast24Hours != 1 || len(overview.FailureAlerts) != 1 {
		t.Fatalf("missed check-in was not recorded as a failure: %+v", overview)
	}
	if got := s.GetQueuedTasks("offline-agent"); len(got) != 0 {
		t.Fatalf("terminally failed task remained queued: %+v", got)
	}
	outputs := s.GetOutputs("offline-agent")
	if len(outputs) != 1 || outputs[0].Warning != "" ||
		!strings.Contains(outputs[0].Error, "did not check in") {
		t.Fatalf("unexpected communication failure output: %+v", outputs)
	}
}

func TestOverviewKeepsOverdueTaskInFlightUntilOfflineThreshold(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Register(&Agent{
		ID:           "overdue-agent",
		Secret:       []byte("secret"),
		FirstSeen:    now.Add(-time.Hour),
		LastSeen:     now.Add(-2 * time.Minute),
		SleepSeconds: 30,
	})
	if err := s.EnqueueTask("overdue-agent", &protocol.Task{ID: "slow-task", Type: "shell"}); err != nil {
		t.Fatal(err)
	}
	if delivered := s.DeliverTask("overdue-agent"); delivered == nil {
		t.Fatal("task was not delivered")
	}
	s.mu.Lock()
	s.agents["overdue-agent"].tasks[0].lastDeliveredAt = now.Add(-2 * time.Minute)
	s.mu.Unlock()

	overview := s.Overview()
	if overview.FailedLast24Hours != 0 || len(overview.FailureAlerts) != 0 {
		t.Fatalf("overdue task was failed before the offline threshold: %+v", overview)
	}
	if got := s.GetQueuedTasks("overdue-agent"); len(got) != 1 || got[0].Status != "in_flight" {
		t.Fatalf("overdue task did not remain in flight: %+v", got)
	}
}

package agent

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/aelder202/sable/internal/protocol"
)

func TestRunRetainsPendingResultUntilBeaconDeliverySucceeds(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	cfg := &Config{
		AgentID:         "agent-test",
		Secret:          secret,
		ServerURL:       "https://127.0.0.1:8443",
		SleepSeconds:    1,
		CertFingerprint: []byte("unused"),
	}

	origNonceFn := beaconNonceFn
	origHTTPSFn := sendBeaconHTTPSFn
	origDNSFn := sendBeaconDNSFn
	defer func() {
		beaconNonceFn = origNonceFn
		sendBeaconHTTPSFn = origHTTPSFn
		sendBeaconDNSFn = origDNSFn
		atomic.StoreInt32(&interactiveMode, 0)
	}()
	atomic.StoreInt32(&interactiveMode, 1)

	nonceValue := []byte("0123456789abcdef")
	beaconNonceFn = func() ([]byte, error) {
		return nonceValue, nil
	}
	sendBeaconDNSFn = func([]byte, string) ([]byte, error) {
		return nil, errors.New("dns disabled")
	}

	callCount := 0
	sendBeaconHTTPSFn = func(_ *http.Client, _ string, payload []byte) ([]byte, error) {
		callCount++

		beacon, err := protocol.DecodeBeacon(payload, secret)
		if err != nil {
			t.Fatalf("DecodeBeacon failed on call %d: %v", callCount, err)
		}

		switch callCount {
		case 1:
			if beacon.TaskOutput != nil {
				t.Fatalf("expected no task output on first beacon, got %#v", beacon.TaskOutput)
			}
			return encodeTaskForTest(t, secret, &protocol.Task{ID: "task-1", Type: "interactive", Payload: "start"}), nil
		case 2:
			if beacon.TaskOutput == nil || beacon.TaskOutput.Output != "interactive mode started" {
				t.Fatalf("expected pending interactive result on retrying beacon, got %#v", beacon.TaskOutput)
			}
			return nil, errors.New("simulated transport failure")
		case 3:
			if beacon.TaskOutput == nil || beacon.TaskOutput.Output != "interactive mode started" {
				t.Fatalf("expected pending result to be retained after failure, got %#v", beacon.TaskOutput)
			}
			return encodeTaskForTest(t, secret, &protocol.Task{ID: "task-2", Type: "kill", Payload: ""}), nil
		default:
			t.Fatalf("unexpected beacon call %d", callCount)
			return nil, nil
		}
	}

	Run(cfg)

	if callCount != 3 {
		t.Fatalf("expected 3 beacon attempts, got %d", callCount)
	}
}

func encodeTaskForTest(t *testing.T, secret []byte, task *protocol.Task) []byte {
	t.Helper()
	encoded, err := protocol.EncodeTask(task, secret)
	if err != nil {
		t.Fatalf("EncodeTask failed: %v", err)
	}
	return encoded
}

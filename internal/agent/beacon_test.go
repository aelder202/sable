package agent

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		atomic.StoreInt64(&pathBrowseFastUntil, 0)
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

func TestCompleteTaskExtendsPathBrowseFastBeacon(t *testing.T) {
	atomic.StoreInt32(&interactiveMode, 0)
	atomic.StoreInt64(&pathBrowseFastUntil, 0)
	t.Cleanup(func() {
		atomic.StoreInt32(&interactiveMode, 0)
		atomic.StoreInt64(&pathBrowseFastUntil, 0)
	})

	if fastBeaconActive() {
		t.Fatal("expected fast beacon to be inactive before completion")
	}

	result := executeTask(&protocol.Task{ID: "task-complete", Type: "complete", Payload: t.TempDir()})
	if result.Error != "" {
		t.Fatalf("complete task failed: %s", result.Error)
	}

	if !fastBeaconActive() {
		t.Fatal("expected completion task to enable temporary fast beaconing")
	}
}

func TestPathBrowseStartStopControlsFastBeacon(t *testing.T) {
	atomic.StoreInt32(&interactiveMode, 0)
	atomic.StoreInt64(&pathBrowseFastUntil, 0)
	t.Cleanup(func() {
		atomic.StoreInt32(&interactiveMode, 0)
		atomic.StoreInt64(&pathBrowseFastUntil, 0)
	})

	start := executeTask(&protocol.Task{ID: "task-pathbrowse-start", Type: "pathbrowse", Payload: "start"})
	if start.Error != "" || start.Output != "path browser ready" {
		t.Fatalf("unexpected start result: %#v", start)
	}
	if !fastBeaconActive() {
		t.Fatal("expected pathbrowse start to enable fast beaconing")
	}

	stop := executeTask(&protocol.Task{ID: "task-pathbrowse-stop", Type: "pathbrowse", Payload: "stop"})
	if stop.Error != "" || stop.Output != "path browser stopped" {
		t.Fatalf("unexpected stop result: %#v", stop)
	}
	if fastBeaconActive() {
		t.Fatal("expected pathbrowse stop to disable fast beaconing")
	}
}

func TestSuspendPathBrowseOnFailureDoesNotStopInteractiveFastBeacon(t *testing.T) {
	atomic.StoreInt32(&interactiveMode, 0)
	atomic.StoreInt64(&pathBrowseFastUntil, time.Now().Add(time.Minute).UnixNano())
	t.Cleanup(func() {
		atomic.StoreInt32(&interactiveMode, 0)
		atomic.StoreInt64(&pathBrowseFastUntil, 0)
	})

	suspendPathBrowseOnFailure()
	if fastBeaconActive() {
		t.Fatal("expected path browse fast mode to stop after transport failure")
	}

	atomic.StoreInt32(&interactiveMode, 1)
	atomic.StoreInt64(&pathBrowseFastUntil, time.Now().Add(time.Minute).UnixNano())
	suspendPathBrowseOnFailure()
	if !fastBeaconActive() {
		t.Fatal("expected interactive fast mode to remain active after transport failure")
	}
}

func TestLogBeaconFailureThrottlesRepeatedFailures(t *testing.T) {
	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(origOutput) })

	var lastLog time.Time
	err := errors.New("dial timeout")
	logBeaconFailure("beacon failed", err, 1, &lastLog)
	logBeaconFailure("beacon failed", err, 2, &lastLog)

	if got := strings.Count(buf.String(), "beacon failed"); got != 1 {
		t.Fatalf("expected one immediate failure log, got %d logs:\n%s", got, buf.String())
	}

	lastLog = time.Now().Add(-failureLogInterval)
	logBeaconFailure("beacon failed", err, 3, &lastLog)
	if !strings.Contains(buf.String(), "still failing after 3 attempts") {
		t.Fatalf("expected throttled summary log, got:\n%s", buf.String())
	}
}

func TestChunkTaskResultSplitsLargeOutput(t *testing.T) {
	output := strings.Repeat("a", resultChunkBytes*2+7)
	chunks := chunkTaskResult(&protocol.TaskResult{
		TaskID: "task-large",
		Type:   "download",
		Output: output,
	})
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	var rebuilt strings.Builder
	for i, chunk := range chunks {
		if chunk.TaskID != "task-large" || chunk.Type != "download" {
			t.Fatalf("unexpected chunk identity: %#v", chunk)
		}
		if chunk.ChunkIndex != i || chunk.ChunkTotal != len(chunks) {
			t.Fatalf("unexpected chunk metadata: %#v", chunk)
		}
		rebuilt.WriteString(chunk.Output)
	}
	if rebuilt.String() != output {
		t.Fatal("chunked output did not reassemble to original output")
	}
}

func TestChunkTaskResultKeepsErrorsWhole(t *testing.T) {
	chunks := chunkTaskResult(&protocol.TaskResult{
		TaskID: "task-error",
		Type:   "shell",
		Output: strings.Repeat("x", resultChunkBytes*2),
		Error:  "failed",
	})
	if len(chunks) != 1 || chunks[0].ChunkTotal != 0 {
		t.Fatalf("expected error result to remain unchunked, got %#v", chunks)
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

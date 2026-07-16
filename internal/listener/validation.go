package listener

import (
	"time"

	"github.com/aelder202/sable/internal/protocol"
)

const (
	beaconNonceBytes     = 16
	maxAgentIDBytes      = 64
	maxHostnameBytes     = 255
	maxPlatformNameBytes = 64
)

func validAgentID(agentID string) bool {
	if len(agentID) == 0 || len(agentID) > maxAgentIDBytes {
		return false
	}
	for i := 0; i < len(agentID); i++ {
		c := agentID[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func validBeacon(beacon *protocol.Beacon, expectedAgentID string, now time.Time) bool {
	if beacon == nil || beacon.AgentID != expectedAgentID || !validAgentID(beacon.AgentID) {
		return false
	}
	if len(beacon.Nonce) != beaconNonceBytes || len(beacon.Hostname) > maxHostnameBytes || len(beacon.OS) > maxPlatformNameBytes || len(beacon.Arch) > maxPlatformNameBytes {
		return false
	}
	slackSeconds := int64(timestampSlack / time.Second)
	nowUnix := now.Unix()
	return beacon.Timestamp >= nowUnix-slackSeconds && beacon.Timestamp <= nowUnix+slackSeconds
}

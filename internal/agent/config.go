package agent

import (
	"encoding/hex"
	"fmt"
	"strconv"
)

// These vars are injected at build time via -ldflags.
// Defaults are obviously invalid to fail fast if not injected.
var (
	AgentID            = "UNSET"
	SecretHex          = "UNSET"
	ServerURL          = "UNSET"
	CertFingerprintHex = "UNSET"
	SleepSecondsStr    = "30"
	DNSDomainStr       = ""
)

// Config holds the decoded runtime config for the agent.
type Config struct {
	AgentID         string
	Secret          []byte
	ServerURL       string
	CertFingerprint []byte
	SleepSeconds    int
	DNSDomain       string
}

// LoadConfig decodes the ldflags-injected hex values into a Config.
// Returns an error if any required value is missing or malformed.
func LoadConfig() (*Config, error) {
	if AgentID == "UNSET" || SecretHex == "UNSET" || ServerURL == "UNSET" || CertFingerprintHex == "UNSET" {
		return nil, fmt.Errorf("agent not compiled with required ldflags")
	}

	secret, err := hex.DecodeString(SecretHex)
	if err != nil {
		return nil, fmt.Errorf("invalid secret hex: %w", err)
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("secret must be 32 bytes, got %d", len(secret))
	}

	fp, err := hex.DecodeString(CertFingerprintHex)
	if err != nil {
		return nil, fmt.Errorf("invalid fingerprint hex: %w", err)
	}

	sleep, err := strconv.Atoi(SleepSecondsStr)
	if err != nil || sleep < 1 {
		return nil, fmt.Errorf("invalid sleep seconds: %q", SleepSecondsStr)
	}

	return &Config{
		AgentID:         AgentID,
		Secret:          secret,
		ServerURL:       ServerURL,
		CertFingerprint: fp,
		SleepSeconds:    sleep,
		DNSDomain:       DNSDomainStr,
	}, nil
}

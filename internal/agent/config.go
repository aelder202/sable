package agent

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
	if len(fp) != 32 {
		return nil, fmt.Errorf("certificate fingerprint must be 32 bytes, got %d", len(fp))
	}
	serverURL, err := validateServerURL(ServerURL)
	if err != nil {
		return nil, err
	}

	sleep, err := strconv.Atoi(SleepSecondsStr)
	if err != nil || sleep < 1 {
		return nil, fmt.Errorf("invalid sleep seconds: %q", SleepSecondsStr)
	}

	return &Config{
		AgentID:         AgentID,
		Secret:          secret,
		ServerURL:       serverURL,
		CertFingerprint: fp,
		SleepSeconds:    sleep,
		DNSDomain:       DNSDomainStr,
	}, nil
}

func validateServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("server URL must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

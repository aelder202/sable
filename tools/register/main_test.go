package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainsAgentID(t *testing.T) {
	agents := []agentSummary{{ID: "one"}, {ID: "two"}}
	if !containsAgentID(agents, "two") {
		t.Fatal("expected agent ID to be found")
	}
	if containsAgentID(agents, "three") {
		t.Fatal("expected unknown agent ID to be absent")
	}
}

func TestBuildAgentEnvIncludesOptionalFields(t *testing.T) {
	cfg := &registerConfig{
		ServerURL:    "https://10.0.0.1:443",
		CertFPHex:    "abc123",
		SleepSeconds: "15",
		DNSDomain:    "c2.example.com",
	}
	content := string(buildAgentEnv("agent-1", "deadbeef", "web01", cfg))

	for _, expected := range []string{
		"AGENT_ID=agent-1",
		"AGENT_SECRET_HEX=deadbeef",
		"CERT_FP_HEX=abc123",
		"SERVER_URL=https://10.0.0.1:443",
		"SLEEP_SECONDS=15",
		"DNS_DOMAIN=c2.example.com",
		"AGENT_LABEL=web01",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected env content to include %q", expected)
		}
	}
}

func TestWriteAgentEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	path, err := writeAgentEnv(dir, "web01", []byte("AGENT_ID=agent-1\n"))
	if err != nil {
		t.Fatalf("writeAgentEnv returned error: %v", err)
	}
	if filepath.Base(path) != "web01.env" {
		t.Fatalf("unexpected env file path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "AGENT_ID=agent-1\n" {
		t.Fatalf("unexpected file contents %q", data)
	}
}

func TestParseConfigNewMode(t *testing.T) {
	cfg, err := parseConfig([]string{
		"--new",
		"--server-url", "https://10.0.0.1:443",
		"--cert-fp", "abc123",
		"--sleep-seconds", "30",
		"--dns-domain", "c2.example.com",
		"agent-1",
		"deadbeef",
		"pw",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if !cfg.New {
		t.Fatal("expected New to be true")
	}
	if cfg.ServerURL != "https://10.0.0.1:443" || cfg.CertFPHex != "abc123" {
		t.Fatal("expected server URL and fingerprint to be parsed")
	}
	if cfg.SleepSeconds != "30" || cfg.DNSDomain != "c2.example.com" {
		t.Fatal("expected optional fields to be parsed")
	}
}

func TestLooksLikeFingerprint(t *testing.T) {
	if !looksLikeFingerprint(strings.Repeat("ab", 32)) {
		t.Fatal("expected valid fingerprint to be accepted")
	}
	if looksLikeFingerprint("not-hex") {
		t.Fatal("expected invalid fingerprint to be rejected")
	}
}

func TestParseConfigLabelFlag(t *testing.T) {
	cfg, err := parseConfig([]string{
		"--new",
		"--label", "web01",
		"--server-url", "https://10.0.0.1:443",
		"--cert-fp", "abc123",
		"agent-1",
		"deadbeef",
		"pw",
	})
	if err != nil {
		t.Fatalf("parseConfig returned error: %v", err)
	}
	if cfg.Label != "web01" {
		t.Fatalf("expected label to be parsed, got %q", cfg.Label)
	}
}

func TestAllocateNewAgentWithRequestedLabel(t *testing.T) {
	dir := t.TempDir()
	id, secret, label, err := allocateNewAgent(dir, "web01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "web01" {
		t.Fatalf("expected label 'web01', got %q", label)
	}
	if id == "" || secret == "" {
		t.Fatal("expected id and secret to be generated")
	}
}

func TestAllocateNewAgentRejectsInvalidLabel(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := allocateNewAgent(dir, "Bad-Label"); err == nil {
		t.Fatal("expected uppercase label to be rejected")
	}
}

func TestAllocateNewAgentRejectsDuplicateLabel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web01.env"), []byte("x"), 0600); err != nil {
		t.Fatalf("seed existing env file: %v", err)
	}
	if _, _, _, err := allocateNewAgent(dir, "web01"); err == nil {
		t.Fatal("expected duplicate label to be rejected")
	}
}

func TestAllocateNewAgentAutoGeneratesLabel(t *testing.T) {
	dir := t.TempDir()
	id, _, label, err := allocateNewAgent(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label == "" {
		t.Fatal("expected auto-generated label")
	}
	if !strings.HasPrefix(id, label) {
		t.Fatalf("expected label %q to be a prefix of id %q", label, id)
	}
}

func TestLabelExistsDetectsEnvAndBuilds(t *testing.T) {
	dir := t.TempDir()

	// No collision yet.
	if exists, _ := labelExists(dir, "web01"); exists {
		t.Fatal("expected labelExists to return false for fresh dir")
	}

	// Env-file collision.
	if err := os.WriteFile(filepath.Join(dir, "web01.env"), []byte("x"), 0600); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	if exists, _ := labelExists(dir, "web01"); !exists {
		t.Fatal("expected labelExists to detect existing env file")
	}

	// Build-directory collision.
	label := "web02"
	if err := os.MkdirAll(filepath.Join("builds", label), 0700); err != nil {
		t.Fatalf("seed build dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join("builds", label))
	})
	if exists, _ := labelExists(dir, label); !exists {
		t.Fatal("expected labelExists to detect existing build directory")
	}
}

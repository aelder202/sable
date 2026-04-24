package main

import (
	"strings"
	"testing"
)

func TestResolveLabelDefault(t *testing.T) {
	label, err := resolveLabel("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "main" {
		t.Fatalf("expected default 'main', got %q", label)
	}
}

func TestResolveLabelFlagWins(t *testing.T) {
	label, err := resolveLabel("web01", "ignored")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "web01" {
		t.Fatalf("expected flag to win, got %q", label)
	}
}

func TestResolveLabelEnvFallback(t *testing.T) {
	label, err := resolveLabel("", "alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if label != "alpha" {
		t.Fatalf("expected env fallback, got %q", label)
	}
}

func TestResolveLabelRejectsInvalid(t *testing.T) {
	if _, err := resolveLabel("Bad-Label", ""); err == nil {
		t.Fatal("expected uppercase label to be rejected")
	}
	if _, err := resolveLabel("", "builds"); err == nil {
		t.Fatal("expected reserved label to be rejected")
	}
}

func TestBuildConfigEnvIncludesLabel(t *testing.T) {
	got := string(buildConfigEnv("agent-1", "deadbeef", "fp", "https://host:443", "web01"))
	for _, want := range []string{
		"AGENT_ID=agent-1",
		"AGENT_SECRET_HEX=deadbeef",
		"CERT_FP_HEX=fp",
		"SERVER_URL=https://host:443",
		"AGENT_LABEL=web01",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected config.env to contain %q; got:\n%s", want, got)
		}
	}
}

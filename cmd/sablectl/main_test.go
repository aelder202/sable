package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManifestAgentPathsAreDedupedAndTargetsTracked(t *testing.T) {
	m := manifest{}
	addAgentPath(&m, filepath.FromSlash("agents/web01.env"), "windows")
	addAgentPath(&m, filepath.FromSlash("agents/web01.env"), "windows")
	addAgentPath(&m, "config.env", "linux")

	if got, want := m.Agents, []string{"agents/web01.env", "config.env"}; !reflect.DeepEqual(cleanList(got), want) {
		t.Fatalf("agents = %#v, want %#v", got, want)
	}
	if got := m.AgentTargets["agents/web01.env"]; got != "windows" {
		t.Fatalf("web01 target = %q, want windows", got)
	}
	if got := m.AgentTargets["config.env"]; got != "linux" {
		t.Fatalf("config target = %q, want linux", got)
	}
}

func TestLoadAgentConfigSupportsDefaultsAndTarget(t *testing.T) {
	t.Chdir(t.TempDir())
	env := strings.Join([]string{
		"AGENT_ID=12345678-1234-1234-1234-123456789abc",
		"AGENT_SECRET_HEX=" + strings.Repeat("a", 64),
		"CERT_FP_HEX=" + strings.Repeat("b", 64),
		"SERVER_URL=https://10.0.0.5:443",
		"AGENT_TARGET=windows",
		"",
	}, "\n")
	if err := os.WriteFile("config.env", []byte(env), 0600); err != nil {
		t.Fatal(err)
	}

	agent, err := loadAgentConfig("config.env")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Label != "12345678" {
		t.Fatalf("label = %q, want UUID prefix", agent.Label)
	}
	if agent.Profile != "default" || agent.SleepSeconds != "30" {
		t.Fatalf("defaults not applied: %#v", agent)
	}
	if agent.Target != "windows" {
		t.Fatalf("target = %q, want windows", agent.Target)
	}
}

func TestFindAgentByLabelChecksPrimaryAndAgentsDir(t *testing.T) {
	t.Chdir(t.TempDir())
	writeEnv := func(path, label, target string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil && filepath.Dir(path) != "." {
			t.Fatal(err)
		}
		env := strings.Join([]string{
			"AGENT_ID=12345678-1234-1234-1234-123456789abc",
			"AGENT_SECRET_HEX=" + strings.Repeat("a", 64),
			"CERT_FP_HEX=" + strings.Repeat("b", 64),
			"SERVER_URL=https://10.0.0.5:443",
			"AGENT_LABEL=" + label,
			"AGENT_TARGET=" + target,
			"",
		}, "\n")
		if err := os.WriteFile(path, []byte(env), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnv("config.env", "main", "linux")
	writeEnv(filepath.FromSlash("agents/win01.env"), "win01", "windows")

	path, agent, err := findAgentByLabel("win01")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(path) != "agents/win01.env" || agent.Target != "windows" {
		t.Fatalf("got path=%q agent=%#v", path, agent)
	}
}

func TestGoVersionAtLeast(t *testing.T) {
	tests := []struct {
		output   string
		required string
		want     bool
	}{
		{"go version go1.26.2 windows/amd64", "1.26.2", true},
		{"go version go1.26.3 windows/amd64", "1.26.2", true},
		{"go version go1.27.0 windows/amd64", "1.26.2", true},
		{"go version go1.25.9 windows/amd64", "1.26.2", false},
	}
	for _, tt := range tests {
		if got := goVersionAtLeast(tt.output, tt.required); got != tt.want {
			t.Fatalf("goVersionAtLeast(%q, %q) = %v, want %v", tt.output, tt.required, got, tt.want)
		}
	}
}

func TestReadOperatorPasswordFromFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("pw.txt", []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readOperatorPassword("pw.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret" {
		t.Fatalf("password = %q, want secret", got)
	}
}

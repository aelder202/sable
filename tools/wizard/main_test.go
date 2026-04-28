package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordedCommand struct {
	Name string
	Args []string
	Env  []string
}

type recordingRunner struct {
	commands []recordedCommand
}

func (r *recordingRunner) Run(name string, args []string, env []string) error {
	r.commands = append(r.commands, recordedCommand{
		Name: name,
		Args: append([]string(nil), args...),
		Env:  append([]string(nil), env...),
	})
	return nil
}

func TestResolveProfile(t *testing.T) {
	name, sleep, err := resolveProfile("fast")
	if err != nil {
		t.Fatalf("resolveProfile returned error: %v", err)
	}
	if name != "fast" || sleep != "5" {
		t.Fatalf("unexpected profile %q sleep %q", name, sleep)
	}
	if _, _, err := resolveProfile("bad"); err == nil {
		t.Fatal("expected bad profile to fail")
	}
}

func TestParseEnv(t *testing.T) {
	values := parseEnv([]byte("AGENT_ID=one\n# comment\nSERVER_URL=\"https://127.0.0.1:443\"\n"))
	if values["AGENT_ID"] != "one" {
		t.Fatalf("unexpected AGENT_ID %q", values["AGENT_ID"])
	}
	if values["SERVER_URL"] != "https://127.0.0.1:443" {
		t.Fatalf("unexpected SERVER_URL %q", values["SERVER_URL"])
	}
}

func TestLoadAgentConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		"AGENT_ID=abcdef12-0000-0000-0000-000000000000",
		"AGENT_SECRET_HEX=secret",
		"CERT_FP_HEX=fingerprint",
		"SERVER_URL=https://127.0.0.1:443",
	}, "\n")), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	agent, err := loadAgentConfig(path)
	if err != nil {
		t.Fatalf("loadAgentConfig returned error: %v", err)
	}
	if agent.Label != "abcdef12" {
		t.Fatalf("expected fallback label, got %q", agent.Label)
	}
	if agent.Profile != "default" || agent.SleepSeconds != "30" {
		t.Fatalf("unexpected defaults: %#v", agent)
	}
}

func TestRunBuildPlanRecordsExpectedCommands(t *testing.T) {
	runner := &recordingRunner{}
	agent := agentConfig{
		ID:           "agent-1",
		SecretHex:    "deadbeef",
		CertFPHex:    "fingerprint",
		ServerURL:    "https://127.0.0.1:443",
		Label:        "main",
		SleepSeconds: "30",
	}
	windowsAgent := agent
	windowsAgent.ID = "agent-2"
	windowsAgent.SecretHex = "cafebabe"
	windowsAgent.Label = "windows"
	var out bytes.Buffer
	if err := runBuildPlan(&out, runner, agent, buildPlan{
		Server:      true,
		AgentTarget: "both",
		AgentBuilds: []agentBuild{
			{Target: "linux", Agent: agent, Primary: true},
			{Target: "windows", Agent: windowsAgent},
		},
	}); err != nil {
		t.Fatalf("runBuildPlan returned error: %v", err)
	}
	if len(runner.commands) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(runner.commands))
	}
	if runner.commands[0].Name != "go" || !containsArg(runner.commands[0].Args, "./cmd/server") {
		t.Fatalf("unexpected server command: %#v", runner.commands[0])
	}
	if !containsEnv(runner.commands[1].Env, "GOOS=linux") {
		t.Fatalf("expected linux env, got %#v", runner.commands[1].Env)
	}
	if !containsEnv(runner.commands[2].Env, "GOOS=windows") {
		t.Fatalf("expected windows env, got %#v", runner.commands[2].Env)
	}
	if strings.Contains(out.String(), "deadbeef") {
		t.Fatal("build output should not print secret material")
	}
}

func TestRunBuildPlanRejectsUnpreparedBoth(t *testing.T) {
	err := runBuildPlan(ioDiscard{}, &recordingRunner{}, agentConfig{Label: "main"}, buildPlan{AgentTarget: "both"})
	if err == nil {
		t.Fatal("expected unprepared both-agent build to fail")
	}
}

func TestPrepareAgentBuildsCreatesSeparateWindowsIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	primary := agentConfig{
		ID:           "agent-1",
		SecretHex:    "deadbeef",
		CertFPHex:    "fingerprint",
		ServerURL:    "https://127.0.0.1:443",
		Label:        "main",
		Profile:      "default",
		SleepSeconds: "30",
	}
	plan := buildPlan{AgentTarget: "both"}
	var out bytes.Buffer
	err := prepareAgentBuilds(
		bufio.NewReader(strings.NewReader("\n\n")),
		&out,
		strings.NewReader(""),
		primary,
		&plan,
		&wizardConfig{},
	)
	if err != nil {
		t.Fatalf("prepareAgentBuilds returned error: %v", err)
	}
	if len(plan.AgentBuilds) != 2 {
		t.Fatalf("expected 2 agent builds, got %d", len(plan.AgentBuilds))
	}
	if plan.AgentBuilds[0].Agent.ID == plan.AgentBuilds[1].Agent.ID {
		t.Fatal("expected distinct agent IDs")
	}
	if plan.AgentBuilds[1].Agent.Label != "windows" {
		t.Fatalf("expected default windows label, got %q", plan.AgentBuilds[1].Agent.Label)
	}
	if _, err := os.Stat(filepath.Join("agents", "windows.env")); err != nil {
		t.Fatalf("expected windows env file: %v", err)
	}
}

func TestEnsureConfigAssumeYesUsesDefaultLabel(t *testing.T) {
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	agent, created, err := ensureConfig(
		bufio.NewReader(strings.NewReader("")),
		&out,
		&wizardConfig{
			ServerURL: "https://127.0.0.1:443",
			AssumeYes: true,
		},
	)
	if err != nil {
		t.Fatalf("ensureConfig returned error: %v", err)
	}
	if !created {
		t.Fatal("expected config to be created")
	}
	if agent.Label != "main" {
		t.Fatalf("expected default label main, got %q", agent.Label)
	}
	if strings.Contains(out.String(), "Initial agent label") {
		t.Fatal("expected --yes path not to prompt for label")
	}
}

func TestResolveBuildPlanRejectsInvalidTarget(t *testing.T) {
	_, err := resolveBuildPlan(bufio.NewReader(strings.NewReader("")), ioDiscard{}, &wizardConfig{
		BuildServer:  true,
		AgentTargets: "darwin",
		AssumeYes:    true,
	})
	if err == nil {
		t.Fatal("expected invalid target to fail")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func containsEnv(env []string, value string) bool {
	for _, item := range env {
		if item == value {
			return true
		}
	}
	return false
}

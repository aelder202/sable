package main

import (
	"bufio"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/session"
)

func TestRegisterAgentSendsDisplayName(t *testing.T) {
	var received map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	if err := registerAgent(server.Client(), server.URL, "token", "agent-1", strings.Repeat("ab", 32), "web01"); err != nil {
		t.Fatal(err)
	}
	if received["display_name"] != "web01" || received["id"] != "agent-1" {
		t.Fatalf("registration body = %#v", received)
	}
}

type testRunner struct {
	run   func(name string, args []string, env []string, stdout, stderr io.Writer) error
	start func(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error)
}

func (r testRunner) Run(name string, args []string, env []string, stdout, stderr io.Writer) error {
	if r.run != nil {
		return r.run(name, args, env, stdout, stderr)
	}
	return nil
}

func (r testRunner) Start(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error) {
	if r.start != nil {
		return r.start(name, args, env, stdout, stderr)
	}
	return nil, os.ErrInvalid
}

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
		{"go version go1.26.5 windows/amd64", "1.26.5", true},
		{"go version go1.26.6 windows/amd64", "1.26.5", true},
		{"go version go1.27.0 windows/amd64", "1.26.5", true},
		{"go version go1.26.4 windows/amd64", "1.26.5", false},
	}
	for _, tt := range tests {
		if got := goVersionAtLeast(tt.output, tt.required); got != tt.want {
			t.Fatalf("goVersionAtLeast(%q, %q) = %v, want %v", tt.output, tt.required, got, tt.want)
		}
	}
}

func TestParseInterspersedAcceptsFlagsAfterPositionals(t *testing.T) {
	cases := map[string][]string{
		"flags before positional": {"--password-file", "pw.txt", "main"},
		"flags after positional":  {"main", "--password-file", "pw.txt"},
		"flag equals after":       {"main", "--password-file=pw.txt"},
		"only positional":         {"main"},
		"no args":                 {},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			pwFile := fs.String("password-file", "", "")
			positional, err := parseInterspersed(fs, args)
			if err != nil {
				t.Fatal(err)
			}
			wantPositional := []string{}
			wantPW := ""
			if len(args) > 0 {
				if args[0] == "main" || (len(args) >= 3 && args[2] == "main") {
					wantPositional = []string{"main"}
				}
				for _, a := range args {
					if strings.HasPrefix(a, "--password-file=") {
						wantPW = strings.TrimPrefix(a, "--password-file=")
					}
				}
				if wantPW == "" {
					for i, a := range args {
						if a == "--password-file" && i+1 < len(args) {
							wantPW = args[i+1]
						}
					}
				}
			}
			if len(positional) != len(wantPositional) {
				t.Fatalf("positional = %v, want %v", positional, wantPositional)
			}
			for i := range positional {
				if positional[i] != wantPositional[i] {
					t.Fatalf("positional[%d] = %q, want %q", i, positional[i], wantPositional[i])
				}
			}
			if *pwFile != wantPW {
				t.Fatalf("--password-file = %q, want %q", *pwFile, wantPW)
			}
		})
	}
}

func TestPasswordFileOrManifestPrefersExplicit(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("manifest-pw.txt", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m := manifest{PasswordFile: "manifest-pw.txt"}
	if got := passwordFileOrManifest("override.txt", m); got != "override.txt" {
		t.Fatalf("explicit override = %q, want override.txt", got)
	}
	if got := passwordFileOrManifest("", m); got != "manifest-pw.txt" {
		t.Fatalf("manifest fallback = %q, want manifest-pw.txt", got)
	}
	missing := manifest{PasswordFile: "does-not-exist.txt"}
	if got := passwordFileOrManifest("", missing); got != "" {
		t.Fatalf("missing manifest path = %q, want empty", got)
	}
}

func TestURLValidationRejectsUnsafeOrigins(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:443",
		"https://user:pass@127.0.0.1:443",
		"https://127.0.0.1:443/path",
		"https://127.0.0.1:443?x=1",
	} {
		if err := validateAgentServerURL(raw); err == nil {
			t.Fatalf("validateAgentServerURL(%q) unexpectedly succeeded", raw)
		}
	}
	if err := validateAgentServerURL("https://10.0.0.5:443"); err != nil {
		t.Fatalf("valid agent origin rejected: %v", err)
	}
	if err := requireLoopbackAPIURL("https://example.com:8443"); err == nil {
		t.Fatal("remote operator API URL should be rejected")
	}
	if err := requireLoopbackAPIURL("https://[::1]:8443"); err != nil {
		t.Fatalf("loopback API rejected: %v", err)
	}
}

func TestRunStartReusesManifestStatePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(serverBinary(runtime.GOOS), []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pw.txt", []byte("password"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureStateKeyFile(filepath.FromSlash("secure/state.key")); err != nil {
		t.Fatal(err)
	}
	if err := saveManifest(manifest{
		State: filepath.FromSlash("data/custom-state.json"), StateKeyFile: filepath.FromSlash("secure/state.key"), PasswordFile: "pw.txt",
	}); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}}
	if err := runStart(nil, runner, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--state-file "+filepath.FromSlash("data/custom-state.json")) ||
		!strings.Contains(joined, "--state-key-file "+filepath.FromSlash("secure/state.key")) {
		t.Fatalf("start did not reuse manifest paths: %v", gotArgs)
	}
}

func TestRunStartPassesExplicitEncryptionOptOut(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(serverBinary(runtime.GOOS), []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("pw.txt", []byte("password"), 0600); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	runner := testRunner{run: func(_ string, args []string, _ []string, _, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}}
	if err := runStart([]string{"--password-file", "pw.txt", "--state-key-file", "none"}, runner, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(gotArgs, " "); !strings.Contains(joined, "--state-key-file none") {
		t.Fatalf("explicit encryption opt-out was not passed to server: %v", gotArgs)
	}
	if _, err := os.Stat(filepath.FromSlash(defaultStateKeyPath)); !os.IsNotExist(err) {
		t.Fatalf("encryption opt-out unexpectedly created a state key: %v", err)
	}
}

func TestManifestRemembersEncryptionOptOut(t *testing.T) {
	if got := manifestStateKeyDefault(manifest{StateEncryptionDisabled: true}); got != "none" {
		t.Fatalf("plaintext manifest default = %q, want none", got)
	}
	if got := manifestStateKeyDefault(manifest{}); got != filepath.FromSlash(defaultStateKeyPath) {
		t.Fatalf("secure manifest default = %q, want %q", got, filepath.FromSlash(defaultStateKeyPath))
	}
}

func TestNextStepsAfterInstallUsesManagedLifecycle(t *testing.T) {
	t.Run("install only", func(t *testing.T) {
		got := nextStepsAfterInstall(installConfig{PasswordFile: "pw.txt"}, false)
		want := []string{"sablectl up"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("next = %v, want %v", got, want)
		}
	})
	t.Run("started but not registered", func(t *testing.T) {
		got := nextStepsAfterInstall(installConfig{Start: true, PasswordFile: "pw.txt"}, false)
		want := []string{"sablectl agent register --password-file pw.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("next = %v, want %v", got, want)
		}
	})
	t.Run("started and registered", func(t *testing.T) {
		got := nextStepsAfterInstall(installConfig{Start: true, PasswordFile: "pw.txt"}, true)
		want := []string{"transfer built agents to authorized targets"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("next = %v, want %v", got, want)
		}
	})
}

func TestRunResetWipesInstallArtifactsIdempotently(t *testing.T) {
	t.Chdir(t.TempDir())
	mustWrite := func(path, content string) {
		t.Helper()
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("config.env", "x")
	mustWrite("server.crt", "x")
	mustWrite("server.key", "x")
	mustWrite("sable-state.json", "{}")
	mustWrite(filepath.FromSlash("agents/win01.env"), "x")
	mustWrite(filepath.FromSlash("builds/main/agent-linux"), "x")
	mustWrite("pw.txt", "secret")
	mustWrite(serverBinary(runtime.GOOS), "x")
	mustWrite(filepath.FromSlash(".sable/server.log"), "log")

	m := manifest{
		Config:       "config.env",
		Cert:         "server.crt",
		Key:          "server.key",
		State:        "sable-state.json",
		PasswordFile: "pw.txt",
		Agents:       []string{"agents/win01.env", "config.env"},
		Builds:       []string{"builds/main/agent-linux", serverBinary(runtime.GOOS)},
		Logs:         []string{".sable/server.log"},
	}
	if err := saveManifest(m); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runReset(nil, &out); err != nil {
		t.Fatalf("first reset: %v", err)
	}
	for _, path := range []string{
		"config.env", "server.crt", "server.key", "sable-state.json",
		"pw.txt", serverBinary(runtime.GOOS),
		filepath.FromSlash("agents/win01.env"),
		filepath.FromSlash("builds/main/agent-linux"),
		filepath.FromSlash(".sable/install.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after reset (err=%v)", path, err)
		}
	}

	out.Reset()
	if err := runReset(nil, &out); err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to remove") {
		t.Fatalf("expected idempotent no-op, got: %q", out.String())
	}
}

func TestResetRefusesBeforeRemovingFilesWhileManagedProcessRuns(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.env", []byte("must remain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordServerPID(os.Getpid()); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	err := runReset(nil, &out)
	if err == nil || !strings.Contains(err.Error(), "still running") || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("error = %v, want safe running-process refusal", err)
	}
	if out.Len() != 0 {
		t.Fatalf("reset reported removals before preflight completed: %q", out.String())
	}
	data, readErr := os.ReadFile("config.env")
	if readErr != nil || string(data) != "must remain" {
		t.Fatalf("reset changed config before refusing: data=%q err=%v", data, readErr)
	}
}

func TestRunDownDoesNotReportStoppedForUnverifiedListener(t *testing.T) {
	t.Chdir(t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var out strings.Builder
	err = runDown([]string{"--api", "https://" + listener.Addr().String()}, &out)
	if err == nil {
		t.Fatal("down reported success for a listener it could not authenticate or manage")
	}
	if strings.Contains(out.String(), "server stopped") {
		t.Fatalf("down falsely reported a stopped server: %q", out.String())
	}
	if !strings.Contains(err.Error(), "authenticated shutdown unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWipePathsContinuesPastFailures(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("a.txt", []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.txt", []byte("b"), 0600); err != nil {
		t.Fatal(err)
	}

	// "../escape" is rejected by removeTrackedPath's workspace guard, simulating
	// a per-path failure without needing OS-specific file locks.
	removed, failed := wipePaths([]string{"a.txt", "../escape", "b.txt"})

	wantRemoved := []string{"a.txt", "b.txt"}
	if !reflect.DeepEqual(removed, wantRemoved) {
		t.Fatalf("removed = %v, want %v", removed, wantRemoved)
	}
	if len(failed) != 1 || !strings.Contains(failed[0].Path, "escape") {
		t.Fatalf("failed = %#v, want one entry mentioning escape", failed)
	}
	if _, err := os.Stat("a.txt"); !os.IsNotExist(err) {
		t.Fatalf("a.txt should be removed despite later failure")
	}
	if _, err := os.Stat("b.txt"); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be removed after recovery")
	}
}

func TestRunResetKeepStatePreservesStateFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.env", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("sable-state.json", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := runReset([]string{"--keep-state"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("config.env"); !os.IsNotExist(err) {
		t.Fatalf("config.env should be removed")
	}
	if _, err := os.Stat("sable-state.json"); err != nil {
		t.Fatalf("sable-state.json should be preserved: %v", err)
	}
}

func TestSensitiveLocalPathsIncludesSecretBearingArtifacts(t *testing.T) {
	m := manifest{
		Config:       "config.env",
		State:        "sable-state.json",
		Cert:         "server.crt",
		Key:          "server.key",
		PasswordFile: "pw.txt",
		Agents:       []string{"agents/win01.env"},
		Builds:       []string{"builds/win01/agent.exe"},
	}
	got := sensitiveLocalPaths(m)
	want := []string{
		".sable/install.json",
		"agents/win01.env",
		"builds/win01/agent.exe",
		"config.env",
		"pw.txt",
		"sable-state.json",
		"server.crt",
		"server.key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sensitive paths = %#v, want %#v", got, want)
	}
}

func TestBuildAgentRestrictsOutputArtifact(t *testing.T) {
	t.Chdir(t.TempDir())
	agent := agentConfig{
		ID:           "12345678-1234-1234-1234-123456789abc",
		SecretHex:    strings.Repeat("a", 64),
		CertFPHex:    strings.Repeat("b", 64),
		ServerURL:    "https://127.0.0.1:443",
		Label:        "main",
		SleepSeconds: "30",
	}
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		if name != "go" {
			t.Fatalf("runner name = %q, want go", name)
		}
		out := agentOutputPath(agent.Label, "linux")
		if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			return err
		}
		return os.WriteFile(out, []byte("agent"), 0644)
	}}

	out, err := buildAgent(runner, agent, "linux", io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("buildAgent: %v", err)
	}
	if filepath.ToSlash(out) != "builds/main/agent-linux" {
		t.Fatalf("out = %q", out)
	}
	if check := checkSensitivePermissions(false); check.Warn || check.Err != "" {
		t.Fatalf("permissions after buildAgent = %+v", check)
	}
}

func TestPrepareGoCommandEnvUsesWorkspaceDirectoriesOnWindows(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOCACHE", "")
	t.Setenv("GOTMPDIR", "")

	env, err := prepareGoCommandEnv("windows", []string{"GOOS=linux", "GOARCH=amd64"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GOCACHE", "GOTMPDIR"} {
		path := environmentValue(env, name)
		if path == "" || !filepath.IsAbs(path) {
			t.Fatalf("%s = %q, want an absolute workspace path", name, path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s directory: %v", name, err)
		}
	}
	if got := environmentValue(env, "GOOS"); got != "linux" {
		t.Fatalf("GOOS = %q, want linux", got)
	}
}

func TestPrepareGoCommandEnvRespectsExplicitOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GOCACHE", "")
	t.Setenv("GOTMPDIR", "")

	env, err := prepareGoCommandEnv("windows", []string{"GOCACHE=C:\\custom-cache", "GOTMPDIR=C:\\custom-tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(env, "GOCACHE"); got != `C:\custom-cache` {
		t.Fatalf("GOCACHE = %q", got)
	}
	if got := environmentValue(env, "GOTMPDIR"); got != `C:\custom-tmp` {
		t.Fatalf("GOTMPDIR = %q", got)
	}
}

func TestCheckSensitivePermissionsCanHardenExistingFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.env", []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	check := checkSensitivePermissions(true)
	if check.Err != "" || check.Warn {
		t.Fatalf("check = %+v, want successful hardening", check)
	}
	if !strings.Contains(check.Message, "hardened") {
		t.Fatalf("message = %q, want hardened", check.Message)
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

func TestReadOperatorPasswordHandlesUTF16BOMFromPowerShell(t *testing.T) {
	t.Chdir(t.TempDir())
	utf16LE := []byte{0xFF, 0xFE, 's', 0, 'e', 0, 'c', 0, 'r', 0, 'e', 0, 't', 0, '\r', 0, '\n', 0}
	if err := os.WriteFile("pw.txt", utf16LE, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readOperatorPassword("pw.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret" {
		t.Fatalf("password = %q, want secret (UTF-16 LE BOM should be stripped)", got)
	}
}

func TestRunSetupUnattendedCreatesSecureLocalInstall(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		if name != "go" {
			return nil
		}
		if len(args) == 1 && args[0] == "version" {
			_, _ = io.WriteString(stdout, "go version go1.26.5 test/amd64\n")
			return nil
		}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				return os.WriteFile(args[i+1], []byte("server"), 0600)
			}
		}
		return nil
	}}

	var out strings.Builder
	err := runSetup([]string{
		"--yes",
		"--start=false",
		"--agents=none",
		"--api=https://127.0.0.1:65534",
	}, runner, strings.NewReader(""), &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"config.env",
		"server.crt",
		"server.key",
		serverBinary(runtime.GOOS),
		sablectlBinary(runtime.GOOS),
		filepath.FromSlash(".sable/operator-password"),
		filepath.FromSlash(".sable/state.key"),
		filepath.FromSlash(".sable/install.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}
	if !strings.Contains(out.String(), "setup complete") || !strings.Contains(out.String(), "next: sablectl up") {
		t.Fatalf("unexpected setup output: %s", out.String())
	}
}

func TestRunSetupGuidedCreatesCountedAgentsWithSymmetricLabels(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		if name != "go" {
			return nil
		}
		if len(args) == 1 && args[0] == "version" {
			_, _ = io.WriteString(stdout, "go version go1.26.5 test/amd64\n")
			return nil
		}
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				return os.WriteFile(args[i+1], []byte("artifact"), 0600)
			}
		}
		return nil
	}}

	input := strings.Join([]string{
		"https://10.0.0.5:443",
		"2",
		"1",
		"",
		"",
		"",
		"",
		"",
		"",
		"n",
		"",
	}, "\n")
	var out strings.Builder
	err := runSetup([]string{
		"--api=https://127.0.0.1:65534",
	}, runner, strings.NewReader(input), &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.FromSlash("builds/linux01/agent-linux"),
		filepath.FromSlash("builds/linux02/agent-linux"),
		filepath.FromSlash("builds/windows01/agent.exe"),
		filepath.FromSlash("agents/linux02.env"),
		filepath.FromSlash("agents/windows01.env"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}
	primary, err := loadAgentConfig("config.env")
	if err != nil {
		t.Fatal(err)
	}
	if primary.Label != "linux01" || primary.Target != "linux" {
		t.Fatalf("primary = label %q target %q, want linux01/linux", primary.Label, primary.Target)
	}

	output := out.String()
	prompts := []string{
		"Agent beacon URL",
		"Total Linux agents",
		"Total Windows agents",
		"Linux agent 1 label",
		"Linux agent 2 label",
		"Windows agent 1 label",
	}
	previous := -1
	for _, prompt := range prompts {
		index := strings.Index(output, prompt)
		if index <= previous {
			t.Fatalf("prompt %q missing or out of order in output: %s", prompt, output)
		}
		previous = index
	}
	if strings.Contains(output, "Primary agent label") || strings.Contains(output, "Agent targets") || strings.Contains(output, "callback") {
		t.Fatalf("legacy setup terminology remained in guided output: %s", output)
	}
	if !strings.Contains(output, "Linux agents (2): linux01, linux02") ||
		!strings.Contains(output, "Windows agents (1): windows01") {
		t.Fatalf("counted setup plan missing labels: %s", output)
	}
}

func TestRunInstallCountFlagsCreateUniqueArtifacts(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				return os.WriteFile(args[i+1], []byte("artifact"), 0600)
			}
		}
		return nil
	}}
	var out strings.Builder
	err := runInstall([]string{
		"--url=https://10.0.0.5:443",
		"--linux-agents=2",
		"--windows-agents=1",
		"--server=false",
		"--register=false",
	}, runner, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		label  string
		target string
		path   string
	}{
		{label: "linux01", target: "linux", path: "config.env"},
		{label: "linux02", target: "linux", path: filepath.FromSlash("agents/linux02.env")},
		{label: "windows01", target: "windows", path: filepath.FromSlash("agents/windows01.env")},
	} {
		agent, err := loadAgentConfig(test.path)
		if err != nil {
			t.Fatalf("load %s: %v", test.path, err)
		}
		if agent.Label != test.label || agent.Target != test.target {
			t.Fatalf("%s = label %q target %q, want %s/%s", test.path, agent.Label, agent.Target, test.label, test.target)
		}
		if _, err := os.Stat(agentOutputPath(test.label, test.target)); err != nil {
			t.Fatalf("missing artifact for %s: %v", test.label, err)
		}
	}
}

func TestRunInstallRejectsMixedCountAndLegacyAgentFlags(t *testing.T) {
	err := runInstall([]string{
		"--linux-agents=1",
		"--agents=linux",
	}, testRunner{}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v, want mixed flag rejection", err)
	}
}

func TestRunInstallCountFlagsPreserveWindowsPrimaryWhenAddingLinux(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				return os.WriteFile(args[i+1], []byte("artifact"), 0600)
			}
		}
		return nil
	}}
	if err := runInstall([]string{
		"--url=https://10.0.0.5:443",
		"--agents=windows",
		"--server=false",
		"--register=false",
	}, runner, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := runInstall([]string{
		"--linux-agents=1",
		"--windows-agents=1",
		"--server=false",
		"--register=false",
	}, runner, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	primary, err := loadAgentConfig("config.env")
	if err != nil {
		t.Fatal(err)
	}
	if primary.Label != "main" || primary.Target != "windows" {
		t.Fatalf("primary changed: label %q target %q", primary.Label, primary.Target)
	}
	linux, err := loadAgentConfig(filepath.FromSlash("agents/linux01.env"))
	if err != nil {
		t.Fatal(err)
	}
	if linux.Label != "linux01" || linux.Target != "linux" {
		t.Fatalf("added agent = label %q target %q", linux.Label, linux.Target)
	}
}

func TestRunInstallCountFlagsCannotReduceExistingTotals(t *testing.T) {
	t.Chdir(t.TempDir())
	primary := strings.Join([]string{
		"AGENT_ID=12345678-1234-1234-1234-123456789abc",
		"AGENT_SECRET_HEX=" + strings.Repeat("a", 64),
		"CERT_FP_HEX=" + strings.Repeat("b", 64),
		"SERVER_URL=https://10.0.0.5:443",
		"AGENT_LABEL=main",
		"AGENT_TARGET=linux",
		"",
	}, "\n")
	if err := os.WriteFile("config.env", []byte(primary), 0600); err != nil {
		t.Fatal(err)
	}
	err := runInstall([]string{
		"--windows-agents=1",
		"--server=false",
		"--register=false",
	}, testRunner{}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot remove existing agent identities") {
		t.Fatalf("error = %v, want non-destructive count rejection", err)
	}
}

func TestRunSetupChecksRunningListenerBeforeGuidedQuestions(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.env", []byte("keep-current-config"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	apiURL := fmt.Sprintf("https://%s", listener.Addr())

	runnerCalled := false
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		runnerCalled = true
		return nil
	}}
	var out strings.Builder
	if err := runSetup([]string{"--api", apiURL}, runner, strings.NewReader("n\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if runnerCalled {
		t.Fatal("setup invoked the build runner after replacement was declined")
	}
	if got, err := os.ReadFile("config.env"); err != nil || string(got) != "keep-current-config" {
		t.Fatalf("current config changed after cancellation: data=%q err=%v", got, err)
	}
	output := out.String()
	if !strings.HasPrefix(output, "A process is already using the Sable API address") {
		t.Fatalf("running-listener warning was not the first setup interaction: %q", output)
	}
	if !strings.Contains(output, "were not changed") {
		t.Fatalf("missing safe-cancellation confirmation: %q", output)
	}
	if strings.Contains(output, "Agent beacon URL") {
		t.Fatalf("guided configuration started before replacement approval: %q", output)
	}
}

func TestRunSetupUnattendedRequiresExplicitReplaceForRunningListener(t *testing.T) {
	t.Chdir(t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	apiURL := fmt.Sprintf("https://%s", listener.Addr())

	var out strings.Builder
	err = runSetup([]string{
		"--yes",
		"--start=false",
		"--agents=none",
		"--api=" + apiURL,
	}, testRunner{}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("error = %v, want explicit --replace requirement", err)
	}
}

func TestRunSetupPreservesUnreadableStateUnlessExplicitlyApproved(t *testing.T) {
	t.Chdir(t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	apiURL := fmt.Sprintf("https://%s", listener.Addr())
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.env", []byte("existing-config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(".sable", 0700); err != nil {
		t.Fatal(err)
	}
	oldKey := []byte(strings.Repeat("a", 32))
	newKey := []byte(strings.Repeat("b", 32))
	store, err := session.NewPersistentStoreWithKey(defaultStatePath, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultStateKeyPath, []byte(fmt.Sprintf("%x\n", newKey)), 0600); err != nil {
		t.Fatal(err)
	}

	runnerCalled := false
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		runnerCalled = true
		return nil
	}}
	var out strings.Builder
	if err := runSetup([]string{"--api", apiURL}, runner, strings.NewReader("n\n"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if runnerCalled {
		t.Fatal("setup ran after unreadable-state recovery was declined")
	}
	if !strings.Contains(out.String(), "cannot be opened with the configured state key") ||
		!strings.Contains(out.String(), "were not changed") {
		t.Fatalf("missing unreadable-state safety warning: %q", out.String())
	}
	if strings.Contains(out.String(), "Agent beacon URL") {
		t.Fatalf("guided setup continued before state recovery approval: %q", out.String())
	}
	if _, err := os.Stat(defaultStatePath); err != nil {
		t.Fatalf("state was not preserved: %v", err)
	}
}

func TestArchivePersistentStatePreservesStateAndArtifactsTogether(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(defaultStatePath, []byte("encrypted-state"), 0600); err != nil {
		t.Fatal(err)
	}
	artifactDir := defaultStatePath + ".artifacts"
	if err := os.MkdirAll(artifactDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "artifact.blob"), []byte("blob"), 0600); err != nil {
		t.Fatal(err)
	}

	archived, err := archivePersistentState(defaultStatePath, time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 2 {
		t.Fatalf("archived paths = %v, want state and artifacts", archived)
	}
	for _, path := range archived {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing recovery backup %s: %v", path, err)
		}
	}
	if pathExists(defaultStatePath) || pathExists(artifactDir) {
		t.Fatal("active state paths remained after recovery archive")
	}
}

func TestValidatePersistentStateAllowsPlaintextBeforeKeyCreation(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := session.NewPersistentStore(defaultStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validatePersistentState(defaultStatePath, defaultStateKeyPath); err != nil {
		t.Fatalf("plaintext state should be eligible for encrypted migration: %v", err)
	}
}

func TestStartServerRefusesUnreadableStateBeforeLaunching(t *testing.T) {
	t.Chdir(t.TempDir())
	oldKey := []byte(strings.Repeat("c", 32))
	newKey := []byte(strings.Repeat("d", 32))
	store, err := session.NewPersistentStoreWithKey(defaultStatePath, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(".sable", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultStateKeyPath, []byte(fmt.Sprintf("%x\n", newKey)), 0600); err != nil {
		t.Fatal(err)
	}

	started := false
	runner := testRunner{start: func(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error) {
		started = true
		return &os.Process{Pid: 1234}, nil
	}}
	err = startServer(runner, serverBinary(runtime.GOOS), "password.txt", "", defaultStatePath, defaultStateKeyPath, filepath.FromSlash(".sable/server.log"))
	if err == nil || !strings.Contains(err.Error(), "persisted state is unreadable") {
		t.Fatalf("error = %v, want unreadable-state diagnosis", err)
	}
	if started {
		t.Fatal("server process launched with an incompatible state key")
	}
}

func TestRecordServerPIDUsesManagedPath(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := recordServerPID(4321); err != nil {
		t.Fatal(err)
	}
	pid, err := readServerPID()
	if err != nil {
		t.Fatal(err)
	}
	if pid != 4321 {
		t.Fatalf("pid = %d, want 4321", pid)
	}
	found := false
	foundStateKey := false
	foundArtifacts := false
	for _, target := range defaultResetTargets(runtime.GOOS, false) {
		if target == serverPIDPath {
			found = true
		}
		if target == defaultStateKeyPath {
			foundStateKey = true
		}
		if target == defaultStatePath+".artifacts" {
			foundArtifacts = true
		}
	}
	if !found {
		t.Fatalf("%s is not included in reset targets", serverPIDPath)
	}
	if !foundStateKey || !foundArtifacts {
		t.Fatalf("replacement reset must include default state key and artifacts: %v", defaultResetTargets(runtime.GOOS, false))
	}
}

func TestStartServerRecordsManagedProcess(t *testing.T) {
	t.Chdir(t.TempDir())
	runner := testRunner{start: func(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error) {
		return &os.Process{Pid: 6789}, nil
	}}
	if err := startServer(
		runner,
		serverBinary(runtime.GOOS),
		"password.txt",
		"",
		defaultStatePath,
		defaultStateKeyPath,
		filepath.FromSlash(".sable/server.log"),
	); err != nil {
		t.Fatal(err)
	}
	pid, err := readServerPID()
	if err != nil {
		t.Fatal(err)
	}
	if pid != 6789 {
		t.Fatalf("pid = %d, want 6789", pid)
	}
}

func TestStopServerForSetupUsesRecordedCredentials(t *testing.T) {
	t.Chdir(t.TempDir())
	const password = "current-secret"
	passwordPath := filepath.FromSlash(".sable/current-password")
	if err := os.MkdirAll(filepath.Dir(passwordPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/auth/login":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["password"] != password {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "setup-token"})
		case "/api/admin/shutdown":
			if r.Header.Get("Authorization") != "Bearer setup-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			go func() {
				time.Sleep(20 * time.Millisecond)
				server.Close()
			}()
		default:
			http.NotFound(w, r)
		}
	}))
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile("server.crt", certPEM, 0600); err != nil {
		server.Close()
		t.Fatal(err)
	}
	if err := recordServerPID(999999); err != nil {
		server.Close()
		t.Fatal(err)
	}

	var out strings.Builder
	if err := stopServerForSetup(server.URL, manifest{PasswordFile: passwordPath}, &out); err != nil {
		server.Close()
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stopped the running Sable server") {
		t.Fatalf("unexpected stop output: %q", out.String())
	}
	if _, err := os.Stat(serverPIDPath); !os.IsNotExist(err) {
		t.Fatalf("managed PID was not removed after graceful shutdown: %v", err)
	}
}

func TestRunAgentCreateBuildsAndDefersRegistrationWhenOffline(t *testing.T) {
	t.Chdir(t.TempDir())
	primary := strings.Join([]string{
		"AGENT_ID=12345678-1234-1234-1234-123456789abc",
		"AGENT_SECRET_HEX=" + strings.Repeat("a", 64),
		"CERT_FP_HEX=" + strings.Repeat("b", 64),
		"SERVER_URL=https://127.0.0.1:443",
		"AGENT_LABEL=main",
		"AGENT_TARGET=linux",
		"",
	}, "\n")
	if err := os.WriteFile("config.env", []byte(primary), 0600); err != nil {
		t.Fatal(err)
	}
	runner := testRunner{run: func(name string, args []string, env []string, stdout, stderr io.Writer) error {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" {
				if err := os.MkdirAll(filepath.Dir(args[i+1]), 0700); err != nil {
					return err
				}
				return os.WriteFile(args[i+1], []byte("agent"), 0600)
			}
		}
		return nil
	}}
	var out strings.Builder
	err := runAgentCreate([]string{
		"linux",
		"--label", "web01",
		"--api", "https://127.0.0.1:65534",
	}, runner, &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.FromSlash("agents/web01.env"),
		filepath.FromSlash("builds/web01/agent-linux"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}
	if !strings.Contains(out.String(), "registration deferred") {
		t.Fatalf("expected deferred registration message, got %s", out.String())
	}
}

func TestPromptChoiceRetriesInvalidInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("invalid\nboth\n"))
	var out strings.Builder
	got, err := promptChoice(scanner, &out, "Agents", "linux", []string{"linux", "windows", "both"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "both" || !strings.Contains(out.String(), "Choose one of") {
		t.Fatalf("choice=%q output=%q", got, out.String())
	}
}

func TestSetupHelpReturnsSuccessfully(t *testing.T) {
	var out strings.Builder
	if err := runSetup([]string{"--help"}, testRunner{}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "usage: sablectl setup") || !strings.Contains(out.String(), "-yes") {
		t.Fatalf("unexpected help output: %s", out.String())
	}
}

func TestPrintAgentDeploymentIncludesChecksumAndPlatformCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("agent.exe", []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printAgentDeployment(&out, "agent.exe", "windows")
	if strings.Contains(out.String(), "sha256: unavailable") || !strings.Contains(out.String(), "Copy-Item") || !strings.Contains(out.String(), "Invoke-Command") {
		t.Fatalf("unexpected Windows deployment output: %s", out.String())
	}

	out.Reset()
	printAgentDeployment(&out, "agent.exe", "linux")
	if !strings.Contains(out.String(), "scp agent.exe") || !strings.Contains(out.String(), "ssh user@<authorized-target>") {
		t.Fatalf("unexpected Linux deployment output: %s", out.String())
	}
}

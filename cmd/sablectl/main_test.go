package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type testRunner struct {
	run func(name string, args []string, env []string, stdout, stderr io.Writer) error
}

func (r testRunner) Run(name string, args []string, env []string, stdout, stderr io.Writer) error {
	if r.run != nil {
		return r.run(name, args, env, stdout, stderr)
	}
	return nil
}

func (testRunner) Start(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error) {
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

func TestNextStepsAfterInstallIncludesRegistration(t *testing.T) {
	t.Run("install only", func(t *testing.T) {
		got := nextStepsAfterInstall(installConfig{PasswordFile: "pw.txt"}, false)
		want := []string{"sablectl start --password-file pw.txt", "sablectl agent register --password-file pw.txt"}
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

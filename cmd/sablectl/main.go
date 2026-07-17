package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aelder202/sable/internal/agentlabel"
	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/operatorpw"
	"github.com/aelder202/sable/internal/securefile"
	"github.com/aelder202/sable/internal/session"
	"github.com/aelder202/sable/internal/tlspin"
	"github.com/google/uuid"
)

const (
	modulePath          = "github.com/aelder202/sable"
	manifestPath        = ".sable/install.json"
	serverPIDPath       = ".sable/server.pid"
	defaultStatePath    = "sable-state.json"
	defaultStateKeyPath = ".sable/state.key"
	defaultAPIURL       = "https://127.0.0.1:8443"
	defaultServerURL    = "https://127.0.0.1:443"
	defaultAgentLabel   = "main"
)

type manifest struct {
	Config                  string            `json:"config,omitempty"`
	State                   string            `json:"state,omitempty"`
	StateKeyFile            string            `json:"state_key_file,omitempty"`
	StateEncryptionDisabled bool              `json:"state_encryption_disabled,omitempty"`
	Cert                    string            `json:"cert,omitempty"`
	Key                     string            `json:"key,omitempty"`
	PasswordFile            string            `json:"password_file,omitempty"`
	Agents                  []string          `json:"agents,omitempty"`
	Builds                  []string          `json:"builds,omitempty"`
	Logs                    []string          `json:"logs,omitempty"`
	AgentTargets            map[string]string `json:"agent_targets,omitempty"`
	UpdatedAt               string            `json:"updated_at,omitempty"`
}

type agentConfig struct {
	ID           string
	SecretHex    string
	CertFPHex    string
	ServerURL    string
	Label        string
	Profile      string
	SleepSeconds string
	DNSDomain    string
	Target       string
}

type commandRunner interface {
	Run(name string, args []string, env []string, stdout, stderr io.Writer) error
	Start(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args []string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (execRunner) Start(name string, args []string, env []string, stdout, stderr io.Writer) (*os.Process, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

func runGoCommand(runner commandRunner, args, env []string, stdout, stderr io.Writer) error {
	env, err := prepareGoCommandEnv(runtime.GOOS, env)
	if err != nil {
		return fmt.Errorf("prepare Go build environment: %w", err)
	}
	return runner.Run("go", args, env, stdout, stderr)
}

func prepareGoCommandEnv(goos string, base []string) ([]string, error) {
	env := append([]string(nil), base...)
	if goos != "windows" {
		return env, nil
	}

	paths := []struct {
		name string
		path string
	}{
		{name: "GOCACHE", path: filepath.FromSlash(".sable/go-build/cache")},
		{name: "GOTMPDIR", path: filepath.FromSlash(".sable/go-build/tmp")},
	}
	for _, item := range paths {
		if environmentValue(env, item.name) != "" || strings.TrimSpace(os.Getenv(item.name)) != "" {
			continue
		}
		path, err := filepath.Abs(item.path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", item.name, err)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, fmt.Errorf("create %s directory: %w", item.name, err)
		}
		env = append(env, item.name+"="+path)
	}
	return env, nil
}

func environmentValue(env []string, name string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func main() {
	if err := runWithInput(os.Args[1:], execRunner{}, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sablectl:", err)
		os.Exit(1)
	}
}

func runWithInput(args []string, runner commandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "setup":
		return runSetup(args[1:], runner, stdin, stdout, stderr)
	case "install":
		return runInstall(args[1:], runner, stdout, stderr)
	case "start":
		return runStart(args[1:], runner, stdout, stderr)
	case "up":
		return runUp(args[1:], runner, stdout, stderr)
	case "down":
		return runDown(args[1:], stdout)
	case "status":
		return runStatus(args[1:], stdout)
	case "agent":
		return runAgent(args[1:], runner, stdout, stderr)
	case "rebuild":
		return runRebuild(args[1:], runner, stdout, stderr)
	case "update":
		return runUpdate(args[1:], runner, stdout, stderr)
	case "remove":
		return runRemove(args[1:], stdout)
	case "reset":
		return runReset(args[1:], stdout)
	case "doctor":
		return runDoctor(args[1:], runner, stdout)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: sablectl <command> [flags]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  setup               guided first-run install, start, registration, and health check")
	fmt.Fprintln(out, "  install             create config, certs, builds, and manifest")
	fmt.Fprintln(out, "  start               run the Sable server")
	fmt.Fprintln(out, "  up                  start the Sable server in the background")
	fmt.Fprintln(out, "  down                gracefully stop the background server")
	fmt.Fprintln(out, "  status              report whether the local server is reachable")
	fmt.Fprintln(out, "  agent create        create, build, and register an agent")
	fmt.Fprintln(out, "  agent add <target>  create a local agent identity")
	fmt.Fprintln(out, "  agent build <label> build a known agent")
	fmt.Fprintln(out, "  agent register      register known local agent identities")
	fmt.Fprintln(out, "  rebuild             rebuild server and known agents")
	fmt.Fprintln(out, "  update              git pull, then rebuild")
	fmt.Fprintln(out, "  remove              remove files tracked in .sable/install.json")
	fmt.Fprintln(out, "  reset               wipe install state and built artifacts for a clean reinstall")
	fmt.Fprintln(out, "  doctor              check local install health")
}

type installConfig struct {
	ServerURL    string
	Label        string
	WindowsLabel string
	Profile      string
	DNSDomain    string
	Agents       string
	BuildServer  bool
	Start        bool
	Register     bool
	Password     string
	PasswordFile string
	APIURL       string
	StatePath    string
	StateKeyPath string
}

type setupConfig struct {
	installConfig
	Yes                    bool
	EncryptState           bool
	Replace                bool
	ArchiveUnreadableState bool
}

func runSetup(args []string, runner commandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	m := loadManifestOrDefault()
	statePath := defaultString(m.State, defaultStatePath)
	existingInstall := pathExists(manifestPath) || pathExists("config.env")
	agents, windowsLabel := setupAgentDefaults(m)
	serverURL := defaultServerURL
	label := defaultAgentLabel
	profile := "default"
	dnsDomain := ""
	if existing, err := loadAgentConfig("config.env"); err == nil {
		serverURL = existing.ServerURL
		label = existing.Label
		profile = existing.Profile
		dnsDomain = existing.DNSDomain
	}

	cfg := setupConfig{
		installConfig: installConfig{
			ServerURL:    serverURL,
			Label:        label,
			WindowsLabel: windowsLabel,
			Profile:      profile,
			DNSDomain:    dnsDomain,
			Agents:       agents,
			BuildServer:  true,
			Start:        true,
			Register:     true,
			PasswordFile: defaultString(m.PasswordFile, filepath.FromSlash(".sable/operator-password")),
			APIURL:       defaultAPIURL,
			StatePath:    statePath,
			StateKeyPath: manifestStateKeyDefault(m),
		},
		EncryptState: !m.StateEncryptionDisabled,
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stdout)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "usage: sablectl setup [flags]")
		fmt.Fprintln(stdout, "Run without --yes for a guided setup, or provide flags with --yes for unattended setup.")
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.ServerURL, "url", cfg.ServerURL, "agent listener URL")
	fs.StringVar(&cfg.Label, "label", cfg.Label, "primary agent label")
	fs.StringVar(&cfg.WindowsLabel, "windows-label", cfg.WindowsLabel, "Windows agent label when --agents=both")
	fs.StringVar(&cfg.Profile, "profile", cfg.Profile, "agent profile: default, fast, quiet, dns")
	fs.StringVar(&cfg.DNSDomain, "dns-domain", cfg.DNSDomain, "DNS fallback domain when --profile=dns")
	fs.StringVar(&cfg.Agents, "agents", cfg.Agents, "agent artifacts to build: linux, windows, both, none")
	fs.StringVar(&cfg.PasswordFile, "password-file", cfg.PasswordFile, "operator password file to create or reuse")
	fs.StringVar(&cfg.APIURL, "api", cfg.APIURL, "loopback operator API URL")
	fs.StringVar(&cfg.StatePath, "state-file", cfg.StatePath, "server state file")
	fs.StringVar(&cfg.StateKeyPath, "state-key-file", cfg.StateKeyPath, "state encryption key file")
	fs.BoolVar(&cfg.EncryptState, "encrypt-state", cfg.EncryptState, "encrypt persisted state")
	fs.BoolVar(&cfg.Start, "start", cfg.Start, "start the server and register agents")
	fs.BoolVar(&cfg.Replace, "replace", false, "stop a running server and permanently replace the existing installation")
	fs.BoolVar(&cfg.ArchiveUnreadableState, "archive-unreadable-state", false, "preserve unreadable persisted state as a recovery backup and start with empty state")
	fs.BoolVar(&cfg.Yes, "yes", false, "accept defaults and run without prompts")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	serverStatus := detectSetupServer(cfg.APIURL)
	replaceExisting := cfg.Replace && existingInstall
	var scanner *bufio.Scanner
	if !cfg.Yes {
		scanner = bufio.NewScanner(stdin)
		if serverStatus.Running {
			printSetupReplacementWarning(stdout, cfg.APIURL, serverStatus.VerifiedSable)
			confirmed, err := promptBool(scanner, stdout, "Stop it and begin a clean setup", false)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(stdout, "Setup cancelled. The running server and current configuration were not changed.")
				return nil
			}
			if err := stopServerForSetup(cfg.APIURL, m, stdout); err != nil {
				return err
			}
			replaceExisting = true
			fmt.Fprintln(stdout, "Server stopped. Current configuration remains intact until you confirm the setup plan.")
		} else if existingInstall {
			fmt.Fprintln(stdout, "Existing Sable configuration detected. Setup will reuse identities and repair missing artifacts.")
		}
		if !replaceExisting {
			stateErr := validatePersistentState(cfg.StatePath, setupStateKeyPath(cfg))
			if stateErr != nil {
				printUnreadableStateWarning(stdout, cfg.StatePath, cfg.StateKeyPath, stateErr)
				confirmed, err := promptBool(scanner, stdout, "Archive the unreadable state and continue with empty state", false)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Fprintln(stdout, "Setup cancelled. Persisted state and the current key were not changed.")
					return nil
				}
				cfg.ArchiveUnreadableState = true
			}
		}
		fmt.Fprintln(stdout, "Sable guided setup")
		fmt.Fprintln(stdout, "Press Enter to accept the value shown in brackets.")
		var err error
		if cfg.ServerURL, err = promptValue(scanner, stdout, "Agent callback URL", cfg.ServerURL); err != nil {
			return err
		}
		if cfg.Agents, err = promptChoice(scanner, stdout, "Agent targets", cfg.Agents, []string{"linux", "windows", "both", "none"}); err != nil {
			return err
		}
		if cfg.Label, err = promptValue(scanner, stdout, "Primary agent label", cfg.Label); err != nil {
			return err
		}
		if cfg.Agents == "both" {
			if cfg.WindowsLabel, err = promptValue(scanner, stdout, "Windows agent label", cfg.WindowsLabel); err != nil {
				return err
			}
		}
		if cfg.Profile, err = promptChoice(scanner, stdout, "Beacon profile", cfg.Profile, []string{"default", "fast", "quiet", "dns"}); err != nil {
			return err
		}
		if cfg.Profile == "dns" {
			if cfg.DNSDomain, err = promptRequired(scanner, stdout, "DNS fallback domain", cfg.DNSDomain); err != nil {
				return err
			}
		}
		if cfg.PasswordFile, err = promptValue(scanner, stdout, "Operator password file", cfg.PasswordFile); err != nil {
			return err
		}
		if cfg.EncryptState, err = promptBool(scanner, stdout, "Encrypt persisted state", cfg.EncryptState); err != nil {
			return err
		}
		if cfg.Start, err = promptBool(scanner, stdout, "Start the server after building", cfg.Start); err != nil {
			return err
		}
		printSetupPlan(stdout, cfg)
		confirmed, err := promptBool(scanner, stdout, "Run setup", true)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Setup cancelled.")
			return nil
		}
	} else if serverStatus.Running {
		if !cfg.Replace {
			return fmt.Errorf("a server is already listening at %s; rerun with --replace to stop it and permanently replace the current installation", cfg.APIURL)
		}
		if err := stopServerForSetup(cfg.APIURL, m, stdout); err != nil {
			return err
		}
		replaceExisting = true
	}
	if cfg.Yes && !replaceExisting {
		if stateErr := validatePersistentState(cfg.StatePath, setupStateKeyPath(cfg)); stateErr != nil && !cfg.ArchiveUnreadableState {
			return fmt.Errorf("persisted state is unreadable with the configured key: %w; restore the matching key or rerun with --archive-unreadable-state to preserve the unreadable data and start empty", stateErr)
		}
	}

	if !cfg.EncryptState {
		cfg.StateKeyPath = ""
	}
	if err := validateSetupConfig(cfg); err != nil {
		return err
	}
	if replaceExisting {
		fmt.Fprintln(stdout, "Removing the previous Sable configuration and persisted data.")
		if err := resetInstallation(false, stdout, false); err != nil {
			return err
		}
	} else if cfg.ArchiveUnreadableState {
		if stateErr := validatePersistentState(cfg.StatePath, cfg.StateKeyPath); stateErr != nil {
			archived, err := archivePersistentState(cfg.StatePath, time.Now())
			if err != nil {
				return err
			}
			for _, path := range archived {
				fmt.Fprintf(stdout, "preserved for recovery: %s\n", filepath.ToSlash(path))
			}
		}
	} else if existing, err := loadAgentConfig("config.env"); err == nil {
		if cfg.ServerURL != existing.ServerURL || cfg.Label != existing.Label || cfg.Profile != existing.Profile || cfg.DNSDomain != existing.DNSDomain {
			return errors.New("setup cannot change an existing agent identity's URL, label, or profile; run sablectl reset first")
		}
	}
	installCfg := cfg.installConfig
	if err := runInstall(installArgs(installCfg), runner, stdout, stderr); err != nil {
		return err
	}
	controlBinary := sablectlBinary(runtime.GOOS)
	if err := runGoCommand(runner, []string{"build", "-o", controlBinary, "./cmd/sablectl"}, nil, stdout, stderr); err != nil {
		return fmt.Errorf("build sablectl: %w", err)
	}
	fmt.Fprintf(stdout, "built: %s\n", controlBinary)

	fmt.Fprintln(stdout, "health check:")
	if err := runDoctor([]string{"--api", cfg.APIURL}, runner, stdout); err != nil {
		return fmt.Errorf("setup completed but health checks failed: %w", err)
	}
	printSetupComplete(stdout, cfg)
	return nil
}

func setupAgentDefaults(m manifest) (string, string) {
	hasLinux := false
	hasWindows := false
	windowsLabel := "win01"
	envs, _ := knownAgentEnvPaths(m, "")
	for _, envPath := range envs {
		agent, err := loadAgentConfig(envPath)
		if err != nil {
			continue
		}
		target := agent.Target
		if target == "" {
			target = m.AgentTargets[filepath.ToSlash(envPath)]
		}
		switch target {
		case "windows":
			hasWindows = true
			if filepath.Clean(envPath) != filepath.Clean("config.env") {
				windowsLabel = agent.Label
			}
		case "linux":
			hasLinux = true
		}
	}
	switch {
	case hasLinux && hasWindows:
		return "both", windowsLabel
	case hasWindows:
		return "windows", windowsLabel
	default:
		return "linux", windowsLabel
	}
}

func validateSetupConfig(cfg setupConfig) error {
	if err := validateAgentServerURL(cfg.ServerURL); err != nil {
		return fmt.Errorf("invalid --url: %w", err)
	}
	if !validAgentTarget(cfg.Agents) {
		return fmt.Errorf("invalid --agents %q", cfg.Agents)
	}
	if err := agentlabel.Validate(cfg.Label); err != nil {
		return err
	}
	if cfg.Agents == "both" {
		if err := agentlabel.Validate(cfg.WindowsLabel); err != nil {
			return err
		}
	}
	if _, _, err := resolveProfile(cfg.Profile); err != nil {
		return err
	}
	if cfg.Profile == "dns" && strings.TrimSpace(cfg.DNSDomain) == "" {
		return errors.New("dns profile requires --dns-domain")
	}
	if strings.TrimSpace(cfg.PasswordFile) == "" {
		return errors.New("setup requires --password-file")
	}
	return requireLoopbackAPIURL(cfg.APIURL)
}

func installArgs(cfg installConfig) []string {
	return []string{
		"--url", cfg.ServerURL,
		"--label", cfg.Label,
		"--windows-label", cfg.WindowsLabel,
		"--profile", cfg.Profile,
		"--dns-domain", cfg.DNSDomain,
		"--agents", cfg.Agents,
		"--server=" + fmt.Sprint(cfg.BuildServer),
		"--start=" + fmt.Sprint(cfg.Start),
		"--register=" + fmt.Sprint(cfg.Register),
		"--password-file", cfg.PasswordFile,
		"--api", cfg.APIURL,
		"--state-file", cfg.StatePath,
		"--state-key-file", cfg.StateKeyPath,
	}
}

func promptValue(scanner *bufio.Scanner, out io.Writer, label, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return defaultValue, nil
	}
	value := strings.TrimSpace(scanner.Text())
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func promptRequired(scanner *bufio.Scanner, out io.Writer, label, defaultValue string) (string, error) {
	for {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("%s is required", strings.ToLower(label))
		}
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			value = defaultValue
		}
		if strings.TrimSpace(value) != "" {
			return value, nil
		}
		fmt.Fprintln(out, "A value is required.")
	}
}

func promptChoice(scanner *bufio.Scanner, out io.Writer, label, defaultValue string, choices []string) (string, error) {
	allowed := make(map[string]bool, len(choices))
	for _, choice := range choices {
		allowed[choice] = true
	}
	for {
		fmt.Fprintf(out, "%s (%s) [%s]: ", label, strings.Join(choices, "/"), defaultValue)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return defaultValue, nil
		}
		value := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if value == "" {
			value = defaultValue
		}
		if allowed[value] {
			return value, nil
		}
		fmt.Fprintf(out, "Choose one of: %s.\n", strings.Join(choices, ", "))
	}
}

func promptBool(scanner *bufio.Scanner, out io.Writer, label string, defaultValue bool) (bool, error) {
	hint := "y/N"
	if defaultValue {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(out, "%s [%s]: ", label, hint)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return false, err
			}
			return defaultValue, nil
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(out, "Enter yes or no.")
		}
	}
}

func printSetupPlan(out io.Writer, cfg setupConfig) {
	fmt.Fprintln(out, "Setup plan:")
	fmt.Fprintf(out, "  callback: %s\n", cfg.ServerURL)
	fmt.Fprintf(out, "  agents: %s\n", cfg.Agents)
	fmt.Fprintf(out, "  profile: %s\n", cfg.Profile)
	fmt.Fprintf(out, "  state encryption: %t\n", cfg.EncryptState)
	if cfg.ArchiveUnreadableState {
		fmt.Fprintln(out, "  unreadable state: preserve as recovery backup and start empty")
	}
	fmt.Fprintf(out, "  start now: %t\n", cfg.Start)
}

func printUnreadableStateWarning(out io.Writer, statePath, stateKeyPath string, stateErr error) {
	fmt.Fprintf(out, "Persisted state at %s cannot be opened with the configured state key %s.\n", filepath.ToSlash(statePath), filepath.ToSlash(defaultString(stateKeyPath, "none")))
	fmt.Fprintf(out, "Reason: %v\n", stateErr)
	fmt.Fprintln(out, "The state will not be overwritten. Continuing will rename the state and artifact directory as recovery backups, then start with empty state.")
	fmt.Fprintln(out, "Recovery backups remain encrypted and require the original matching key.")
}

func setupStateKeyPath(cfg setupConfig) string {
	if !cfg.EncryptState {
		return ""
	}
	return normalizeStateKeyPath(cfg.StateKeyPath)
}

type setupServerStatus struct {
	Running       bool
	VerifiedSable bool
}

func detectSetupServer(apiURL string) setupServerStatus {
	if apiReachable(apiURL) {
		return setupServerStatus{Running: true, VerifiedSable: true}
	}
	return setupServerStatus{Running: apiListenerReachable(apiURL)}
}

func apiListenerReachable(apiURL string) bool {
	if requireLoopbackAPIURL(apiURL) != nil {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return false
	}
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	conn, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func printSetupReplacementWarning(out io.Writer, apiURL string, verifiedSable bool) {
	if verifiedSable {
		fmt.Fprintf(out, "Running Sable server detected at %s.\n", apiURL)
	} else {
		fmt.Fprintf(out, "A process is already using the Sable API address %s.\n", apiURL)
	}
	fmt.Fprintln(out, "WARNING: Continuing will stop that server and create a clean Sable installation.")
	fmt.Fprintln(out, "After final confirmation, current configuration, agent identities, persisted state and artifacts, keys, credentials, logs, and builds will be permanently removed.")
}

func stopServerForSetup(apiURL string, m manifest, out io.Writer) error {
	if apiReachable(apiURL) {
		for _, password := range knownOperatorPasswords(m) {
			client, err := apiClient()
			if err != nil {
				break
			}
			token, err := login(client, apiURL, password)
			if err != nil {
				continue
			}
			if err := requestShutdown(client, apiURL, token); err != nil {
				return err
			}
			if err := waitForServerStop(apiURL, 10*time.Second); err != nil {
				return err
			}
			if err := waitForManagedServerExit(15 * time.Second); err != nil {
				return err
			}
			removeServerPID()
			fmt.Fprintln(out, "Stopped the running Sable server.")
			return nil
		}
	}

	if err := terminateManagedServer(apiURL); err == nil {
		fmt.Fprintln(out, "Stopped the running Sable server.")
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return errors.New("the listener could not be stopped safely because it has no managed Sable PID and the recorded operator credentials did not authenticate; stop it with `sablectl down` or set SABLE_OPERATOR_PASSWORD, then rerun setup")
}

func knownOperatorPasswords(m manifest) []string {
	passwords := []string{}
	paths := cleanList([]string{m.PasswordFile, filepath.FromSlash(".sable/operator-password")})
	for _, path := range paths {
		if password, err := readOperatorPassword(path, ""); err == nil {
			addUnique(&passwords, password)
		}
	}
	if password := strings.TrimSpace(os.Getenv("SABLE_OPERATOR_PASSWORD")); password != "" {
		addUnique(&passwords, password)
	}
	return passwords
}

func waitForServerStop(apiURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !apiListenerReachable(apiURL) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("server is still listening at %s after %s", apiURL, timeout)
}

func waitForManagedServerExit(timeout time.Duration) error {
	pid, err := readServerPID()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read managed Sable PID while waiting for shutdown: %w", err)
	}
	if err := waitForProcessExit(pid, timeout); err != nil {
		return fmt.Errorf("wait for managed Sable server process %d: %w", pid, err)
	}
	return nil
}

func printSetupComplete(out io.Writer, cfg setupConfig) {
	fmt.Fprintln(out, "setup complete")
	fmt.Fprintf(out, "console: %s\n", cfg.APIURL)
	fmt.Fprintf(out, "credentials: %s\n", filepath.ToSlash(cfg.PasswordFile))
	if !cfg.Start {
		fmt.Fprintln(out, "next: sablectl up")
	}
	m := loadManifestOrDefault()
	fmt.Fprintln(out, "agent artifacts:")
	for _, build := range m.Builds {
		if strings.HasPrefix(filepath.ToSlash(build), "builds/") {
			printAgentDeployment(out, build, targetForArtifact(build))
		}
	}
	fmt.Fprintln(out, "deploy only to systems you own or are authorized to test")
}

func printAgentDeployment(out io.Writer, path, target string) {
	slashPath := filepath.ToSlash(path)
	checksum := "unavailable"
	if file, err := os.Open(path); err == nil {
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err == nil {
			checksum = hex.EncodeToString(hash.Sum(nil))
		}
		_ = file.Close()
	}
	fmt.Fprintf(out, "  %s\n", slashPath)
	fmt.Fprintf(out, "    sha256: %s\n", checksum)
	if target == "windows" {
		fmt.Fprintf(out, "    transfer: Copy-Item %s \\\\<authorized-target>\\C$\\Temp\\sable-agent.exe\n", path)
		fmt.Fprintln(out, "    start: Invoke-Command -ComputerName <authorized-target> { Start-Process C:\\Temp\\sable-agent.exe -WindowStyle Hidden }")
		return
	}
	fmt.Fprintf(out, "    transfer: scp %s user@<authorized-target>:/tmp/sable-agent\n", slashPath)
	fmt.Fprintln(out, "    start: ssh user@<authorized-target> 'chmod +x /tmp/sable-agent && nohup /tmp/sable-agent >/dev/null 2>&1 &'")
}

func targetForArtifact(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".exe") {
		return "windows"
	}
	return "linux"
}

func runInstall(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	m := loadManifestOrDefault()
	statePath := m.State
	if statePath == "" {
		statePath = defaultStatePath
	}
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := installConfig{
		Label:        defaultAgentLabel,
		Agents:       "linux",
		BuildServer:  true,
		Register:     true,
		APIURL:       defaultAPIURL,
		StatePath:    statePath,
		StateKeyPath: manifestStateKeyDefault(m),
	}
	fs.StringVar(&cfg.ServerURL, "url", "", "agent listener URL, for example https://10.0.0.5:443")
	fs.StringVar(&cfg.ServerURL, "server-url", "", "alias for --url")
	fs.StringVar(&cfg.Label, "label", cfg.Label, "primary agent label")
	fs.StringVar(&cfg.WindowsLabel, "windows-label", "", "Windows agent label when --agents both")
	fs.StringVar(&cfg.Profile, "profile", "", "agent profile: default, fast, quiet, dns")
	fs.StringVar(&cfg.DNSDomain, "dns-domain", "", "DNS fallback domain when profile=dns")
	fs.StringVar(&cfg.Agents, "agents", cfg.Agents, "agent artifacts to build: linux, windows, both, none")
	fs.BoolVar(&cfg.BuildServer, "server", cfg.BuildServer, "build server binary")
	fs.BoolVar(&cfg.Start, "start", false, "start server after building")
	fs.BoolVar(&cfg.Register, "register", cfg.Register, "register generated agents when the server is reachable")
	fs.StringVar(&cfg.Password, "password", "", "operator password for --start or registration")
	fs.StringVar(&cfg.PasswordFile, "password-file", "", "operator password file to create or use")
	fs.StringVar(&cfg.APIURL, "api", cfg.APIURL, "operator API URL for registration")
	fs.StringVar(&cfg.StatePath, "state-file", cfg.StatePath, "server state file")
	fs.StringVar(&cfg.StateKeyPath, "state-key-file", cfg.StateKeyPath, "state encryption key file; use 'none' to opt out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	cfg.StateKeyPath = normalizeStateKeyPath(cfg.StateKeyPath)
	if !statePersistenceEnabled(cfg.StatePath) {
		cfg.StateKeyPath = ""
	}
	if !validAgentTarget(cfg.Agents) {
		return fmt.Errorf("invalid --agents %q", cfg.Agents)
	}
	if err := requireLoopbackAPIURL(cfg.APIURL); err != nil {
		return err
	}
	if statePersistenceEnabled(cfg.StatePath) && cfg.StateKeyPath != "" {
		if err := ensureStateKeyFile(cfg.StateKeyPath); err != nil {
			return err
		}
	}

	primary, created, err := ensurePrimaryConfig(cfg)
	if err != nil {
		return err
	}
	if created {
		m.Config = "config.env"
		m.Cert = "server.crt"
		m.Key = "server.key"
	}
	m.State = cfg.StatePath
	m.StateKeyFile = cfg.StateKeyPath
	m.StateEncryptionDisabled = statePersistenceEnabled(cfg.StatePath) && cfg.StateKeyPath == ""
	addAgentPath(&m, "config.env", primary.Target)

	agentBuilds, err := ensureInstallAgents(primary, cfg)
	if err != nil {
		return err
	}
	for _, build := range agentBuilds {
		addAgentPath(&m, build.envPath, build.agent.Target)
	}

	changed := make([]string, 0)
	if cfg.BuildServer {
		out, err := buildServer(runner, io.Discard, stderr)
		if err != nil {
			return err
		}
		changed = append(changed, out)
		addUnique(&m.Builds, out)
	}
	for _, build := range agentBuilds {
		out, err := buildAgent(runner, build.agent, build.agent.Target, io.Discard, stderr)
		if err != nil {
			return err
		}
		changed = append(changed, out)
		addUnique(&m.Builds, out)
	}

	password, err := resolvePasswordFile(cfg.PasswordFile, cfg.Password)
	if err != nil {
		return err
	}
	if cfg.PasswordFile != "" {
		m.PasswordFile = cfg.PasswordFile
	}

	if cfg.Start {
		if password == "" && cfg.PasswordFile == "" {
			return errors.New("--start requires --password-file or --password")
		}
		logPath := filepath.Join(".sable", "server.log")
		if err := startServer(runner, serverBinary(runtime.GOOS), cfg.PasswordFile, password, cfg.StatePath, cfg.StateKeyPath, logPath); err != nil {
			return err
		}
		addUnique(&m.Logs, logPath)
		waitForAPI(cfg.APIURL, 10*time.Second)
		if !apiReachable(cfg.APIURL) {
			return fmt.Errorf("server did not become reachable at %s; see %s", cfg.APIURL, filepath.ToSlash(logPath))
		}
	}

	registered := false
	if cfg.Register && password != "" && apiReachable(cfg.APIURL) {
		envs := envPathsForBuilds(agentBuilds)
		if len(envs) == 0 {
			envs = []string{"config.env"}
		}
		if err := registerEnvFiles(cfg.APIURL, password, envs); err != nil {
			return err
		}
		registered = true
	}

	if err := saveManifest(m); err != nil {
		return err
	}

	printInstallSummary(stdout, m, changed, registered, cfg)
	return nil
}

type plannedAgentBuild struct {
	agent   agentConfig
	envPath string
}

func ensurePrimaryConfig(cfg installConfig) (agentConfig, bool, error) {
	if existing, err := loadAgentConfig("config.env"); err == nil {
		return existing, false, nil
	} else if !os.IsNotExist(err) {
		return agentConfig{}, false, fmt.Errorf("read config.env: %w", err)
	}

	serverURL := strings.TrimSpace(cfg.ServerURL)
	if serverURL == "" {
		return agentConfig{}, false, errors.New("--url is required when config.env does not exist")
	}
	if err := validateAgentServerURL(serverURL); err != nil {
		return agentConfig{}, false, fmt.Errorf("invalid --url: %w", err)
	}
	label := strings.TrimSpace(cfg.Label)
	if label == "" {
		label = defaultAgentLabel
	}
	if err := agentlabel.Validate(label); err != nil {
		return agentConfig{}, false, err
	}
	profile, sleep, err := resolveProfile(cfg.Profile)
	if err != nil {
		return agentConfig{}, false, err
	}
	dnsDomain := strings.TrimSpace(cfg.DNSDomain)
	if profile == "dns" && dnsDomain == "" {
		return agentConfig{}, false, errors.New("dns profile requires --dns-domain")
	}
	_, fp, err := listener.LoadOrCreateCert("server.crt", "server.key")
	if err != nil {
		return agentConfig{}, false, fmt.Errorf("create certificate: %w", err)
	}
	secret, err := randomHex(32)
	if err != nil {
		return agentConfig{}, false, err
	}
	agent := agentConfig{
		ID:           uuid.New().String(),
		SecretHex:    secret,
		CertFPHex:    fp,
		ServerURL:    serverURL,
		Label:        label,
		Profile:      profile,
		SleepSeconds: sleep,
		DNSDomain:    dnsDomain,
		Target:       primaryTarget(cfg.Agents),
	}
	if err := securefile.WriteFile("config.env", buildConfigEnv(agent)); err != nil {
		return agentConfig{}, false, fmt.Errorf("write config.env: %w", err)
	}
	return agent, true, nil
}

func ensureInstallAgents(primary agentConfig, cfg installConfig) ([]plannedAgentBuild, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Agents)) {
	case "none":
		return nil, nil
	case "linux", "windows":
		primary.Target = strings.ToLower(cfg.Agents)
		return []plannedAgentBuild{{agent: primary, envPath: "config.env"}}, nil
	case "both":
		primary.Target = "linux"
		windows, path, err := ensureAdditionalAgent(primary, cfg.WindowsLabel, "windows")
		if err != nil {
			return nil, err
		}
		return []plannedAgentBuild{
			{agent: primary, envPath: "config.env"},
			{agent: windows, envPath: path},
		}, nil
	default:
		return nil, fmt.Errorf("invalid --agents %q", cfg.Agents)
	}
}

func ensureAdditionalAgent(primary agentConfig, label, target string) (agentConfig, string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		label = defaultAdditionalLabel(primary.Label, target)
	}
	if err := agentlabel.Validate(label); err != nil {
		return agentConfig{}, "", err
	}
	if label == primary.Label {
		return agentConfig{}, "", fmt.Errorf("agent label %q is already used by config.env", label)
	}
	envPath := filepath.Join("agents", label+".env")
	if existing, err := loadAgentConfig(envPath); err == nil {
		if existing.Target != "" && existing.Target != target {
			return agentConfig{}, "", fmt.Errorf("agent %q already targets %s", label, existing.Target)
		}
		existing.Target = target
		return existing, envPath, nil
	} else if !os.IsNotExist(err) {
		return agentConfig{}, "", fmt.Errorf("read %s: %w", envPath, err)
	}
	secret, err := randomHex(32)
	if err != nil {
		return agentConfig{}, "", err
	}
	agent := agentConfig{
		ID:           uuid.New().String(),
		SecretHex:    secret,
		CertFPHex:    primary.CertFPHex,
		ServerURL:    primary.ServerURL,
		Label:        label,
		Profile:      primary.Profile,
		SleepSeconds: primary.SleepSeconds,
		DNSDomain:    primary.DNSDomain,
		Target:       target,
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		return agentConfig{}, "", fmt.Errorf("create agents directory: %w", err)
	}
	if err := securefile.WriteFile(envPath, buildConfigEnv(agent)); err != nil {
		return agentConfig{}, "", fmt.Errorf("write %s: %w", envPath, err)
	}
	return agent, envPath, nil
}

func runStart(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	m := loadManifestOrDefault()
	statePath := m.State
	if statePath == "" {
		statePath = defaultStatePath
	}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	passwordFile := fs.String("password-file", "", "operator password file")
	password := fs.String("password", "", "operator password")
	stateFile := fs.String("state-file", statePath, "server state file")
	stateKeyFile := fs.String("state-key-file", manifestStateKeyDefault(m), "state encryption key file; use 'none' to opt out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	stateKeyPath := normalizeStateKeyPath(*stateKeyFile)
	if !statePersistenceEnabled(*stateFile) {
		stateKeyPath = ""
	}
	binary := serverBinary(runtime.GOOS)
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("%s not found; run sablectl rebuild --server-only", binary)
	}
	resolvedFile := passwordFileOrManifest(*passwordFile, m)
	args = []string{"--state-file", *stateFile}
	if statePersistenceEnabled(*stateFile) && stateKeyPath != "" {
		if err := ensureStateKeyFile(stateKeyPath); err != nil {
			return err
		}
	}
	args = append(args, "--state-key-file", defaultString(stateKeyPath, "none"))
	env := []string{}
	switch {
	case resolvedFile != "":
		args = append(args, "--password-file", resolvedFile)
	case strings.TrimSpace(*password) != "":
		env = append(env, "SABLE_OPERATOR_PASSWORD="+strings.TrimSpace(*password))
	case strings.TrimSpace(os.Getenv("SABLE_OPERATOR_PASSWORD")) != "":
		env = append(env, "SABLE_OPERATOR_PASSWORD="+strings.TrimSpace(os.Getenv("SABLE_OPERATOR_PASSWORD")))
	default:
		return errors.New("supply --password-file, --password, or SABLE_OPERATOR_PASSWORD")
	}
	fmt.Fprintf(stdout, "starting %s\n", binary)
	return runner.Run("./"+binary, args, env, stdout, stderr)
}

func runUp(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	m := loadManifestOrDefault()
	statePath := defaultString(m.State, defaultStatePath)
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	passwordFile := fs.String("password-file", "", "operator password file")
	password := fs.String("password", "", "operator password")
	stateFile := fs.String("state-file", statePath, "server state file")
	stateKeyFile := fs.String("state-key-file", manifestStateKeyDefault(m), "state encryption key file; use 'none' to opt out")
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	stateKeyPath := normalizeStateKeyPath(*stateKeyFile)
	if !statePersistenceEnabled(*stateFile) {
		stateKeyPath = ""
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	resolvedFile := passwordFileOrManifest(*passwordFile, m)
	resolvedPassword, err := readOperatorPassword(resolvedFile, *password)
	if err != nil {
		return err
	}
	if apiReachable(*apiURL) {
		fmt.Fprintf(stdout, "server already running at %s\n", *apiURL)
		return registerKnownAgents(*apiURL, resolvedPassword, m, stdout)
	}
	binary := serverBinary(runtime.GOOS)
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("%s not found; run sablectl rebuild --server-only", binary)
	}
	if statePersistenceEnabled(*stateFile) && stateKeyPath != "" {
		if err := ensureStateKeyFile(stateKeyPath); err != nil {
			return err
		}
	}
	logPath := filepath.Join(".sable", "server.log")
	if err := startServer(runner, binary, resolvedFile, resolvedPassword, *stateFile, stateKeyPath, logPath); err != nil {
		return err
	}
	waitForAPI(*apiURL, 10*time.Second)
	if !apiReachable(*apiURL) {
		return fmt.Errorf("server did not become reachable at %s; see %s", *apiURL, filepath.ToSlash(logPath))
	}
	addUnique(&m.Logs, logPath)
	m.State = *stateFile
	m.StateKeyFile = stateKeyPath
	m.StateEncryptionDisabled = statePersistenceEnabled(*stateFile) && stateKeyPath == ""
	if err := saveManifest(m); err != nil {
		return err
	}
	if err := registerKnownAgents(*apiURL, resolvedPassword, m, stdout); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "server running at %s\n", *apiURL)
	fmt.Fprintf(stdout, "log: %s\n", filepath.ToSlash(logPath))
	return nil
}

func runDown(args []string, stdout io.Writer) error {
	m := loadManifestOrDefault()
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	passwordFile := fs.String("password-file", "", "operator password file")
	password := fs.String("password", "", "operator password")
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	if !apiListenerReachable(*apiURL) {
		if err := waitForManagedServerExit(2 * time.Second); err != nil {
			if terminateErr := terminateManagedServer(*apiURL); terminateErr != nil {
				return fmt.Errorf("the API listener is closed, but the managed server process has not exited: %v", err)
			}
		}
		fmt.Fprintln(stdout, "server stopped")
		removeServerPID()
		return nil
	}
	resolvedPassword, err := readOperatorPassword(passwordFileOrManifest(*passwordFile, m), *password)
	if err != nil {
		return stopManagedServerAfterAuthFailure(*apiURL, err, stdout)
	}
	client, err := apiClient()
	if err != nil {
		return stopManagedServerAfterAuthFailure(*apiURL, err, stdout)
	}
	token, err := login(client, *apiURL, resolvedPassword)
	if err != nil {
		return stopManagedServerAfterAuthFailure(*apiURL, err, stdout)
	}
	if err := requestShutdown(client, *apiURL, token); err != nil {
		return stopManagedServerAfterAuthFailure(*apiURL, err, stdout)
	}
	if err := waitForServerStop(*apiURL, 10*time.Second); err != nil {
		return err
	}
	if err := waitForManagedServerExit(15 * time.Second); err != nil {
		return err
	}
	removeServerPID()
	fmt.Fprintln(stdout, "server stopped")
	return nil
}

func stopManagedServerAfterAuthFailure(apiURL string, authErr error, stdout io.Writer) error {
	if err := terminateManagedServer(apiURL); err != nil {
		return fmt.Errorf("authenticated shutdown unavailable (%v); managed server stop failed: %w", authErr, err)
	}
	fmt.Fprintln(stdout, "server stopped")
	return nil
}

func runStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	if apiReachable(*apiURL) {
		fmt.Fprintf(stdout, "running: %s\n", *apiURL)
		return nil
	}
	fmt.Fprintln(stdout, "stopped")
	return nil
}

func registerKnownAgents(apiURL, password string, m manifest, stdout io.Writer) error {
	envs, err := knownAgentEnvPaths(m, "")
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		return nil
	}
	if err := registerEnvFiles(apiURL, password, envs); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "registered %d local agent identity(s)\n", len(envs))
	return nil
}

func runAgent(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: sablectl agent <create|add|build|register>")
	}
	switch args[0] {
	case "create":
		return runAgentCreate(args[1:], runner, stdout, stderr)
	case "add":
		return runAgentAdd(args[1:], stdout)
	case "build":
		return runAgentBuild(args[1:], runner, stdout, stderr)
	case "register":
		return runAgentRegister(args[1:], stdout)
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func runAgentCreate(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	label := fs.String("label", "", "agent label")
	offlinePEAS := fs.Bool("offline-peas", false, "update PEAS cache before building")
	register := fs.Bool("register", true, "register when the local server is reachable")
	password := fs.String("password", "", "operator password")
	passwordFile := fs.String("password-file", "", "operator password file")
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: sablectl agent create <linux|windows> --label <name>")
	}
	target := strings.ToLower(positional[0])
	if target != "linux" && target != "windows" {
		return fmt.Errorf("invalid agent target %q", target)
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	primary, err := loadAgentConfig("config.env")
	if err != nil {
		return fmt.Errorf("load config.env: %w", err)
	}
	agent, envPath, err := ensureAdditionalAgent(primary, *label, target)
	if err != nil {
		return err
	}
	m := loadManifestOrDefault()
	addAgentPath(&m, envPath, target)
	if err := saveManifest(m); err != nil {
		return err
	}
	if *offlinePEAS {
		if err := runGoCommand(runner, []string{"run", "./tools/updatepeas"}, nil, stdout, stderr); err != nil {
			return fmt.Errorf("update PEAS cache: %w", err)
		}
	}
	out, err := buildAgent(runner, agent, target, stdout, stderr)
	if err != nil {
		return err
	}
	addUnique(&m.Builds, out)
	if err := saveManifest(m); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "agent: %s\n", agent.Label)
	fmt.Fprintf(stdout, "env: %s\n", filepath.ToSlash(envPath))
	fmt.Fprintf(stdout, "built: %s\n", filepath.ToSlash(out))
	printAgentDeployment(stdout, out, target)
	if !*register {
		return nil
	}
	if !apiReachable(*apiURL) {
		fmt.Fprintln(stdout, "registration deferred: start the server with sablectl up")
		return nil
	}
	resolvedPassword, err := readOperatorPassword(passwordFileOrManifest(*passwordFile, m), *password)
	if err != nil {
		return err
	}
	if err := registerEnvFiles(*apiURL, resolvedPassword, []string{envPath}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "registered: %s (%s)\n", agent.Label, agent.ID)
	return nil
}

func runAgentAdd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	label := fs.String("label", "", "agent label")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: sablectl agent add <linux|windows> --label <name>")
	}
	target := strings.ToLower(positional[0])
	if target != "linux" && target != "windows" {
		return fmt.Errorf("invalid agent target %q", target)
	}
	primary, err := loadAgentConfig("config.env")
	if err != nil {
		return fmt.Errorf("load config.env: %w", err)
	}
	agent, envPath, err := ensureAdditionalAgent(primary, *label, target)
	if err != nil {
		return err
	}
	m := loadManifestOrDefault()
	addAgentPath(&m, envPath, target)
	if err := saveManifest(m); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "agent: %s\n", agent.Label)
	fmt.Fprintf(stdout, "env: %s\n", filepath.ToSlash(envPath))
	fmt.Fprintf(stdout, "next: sablectl agent build %s\n", agent.Label)
	return nil
}

func runAgentBuild(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent build", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	targetFlag := fs.String("target", "", "override target: linux or windows")
	offlinePEAS := fs.Bool("offline-peas", false, "update PEAS cache before building")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: sablectl agent build <label>")
	}
	envPath, agent, err := findAgentByLabel(positional[0])
	if err != nil {
		return err
	}
	target := strings.TrimSpace(*targetFlag)
	if target == "" {
		target = agent.Target
	}
	if target == "" {
		target = "linux"
	}
	if target != "linux" && target != "windows" {
		return fmt.Errorf("invalid target %q", target)
	}
	if *offlinePEAS {
		if err := runGoCommand(runner, []string{"run", "./tools/updatepeas"}, nil, stdout, stderr); err != nil {
			return fmt.Errorf("update PEAS cache: %w", err)
		}
	}
	out, err := buildAgent(runner, agent, target, stdout, stderr)
	if err != nil {
		return err
	}
	m := loadManifestOrDefault()
	addAgentPath(&m, envPath, target)
	addUnique(&m.Builds, out)
	if err := saveManifest(m); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "built: %s\n", filepath.ToSlash(out))
	return nil
}

func runAgentRegister(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("agent register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	password := fs.String("password", "", "operator password")
	passwordFile := fs.String("password-file", "", "operator password file")
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New("usage: sablectl agent register [label|all] --password-file ./pw.txt")
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	selector := "all"
	if len(positional) == 1 {
		selector = positional[0]
	}
	m := loadManifestOrDefault()
	resolvedPassword, err := readOperatorPassword(passwordFileOrManifest(*passwordFile, m), *password)
	if err != nil {
		return err
	}
	envs, err := knownAgentEnvPaths(m, "")
	if err != nil {
		return err
	}
	if selector != "all" {
		envPath, _, err := findAgentByLabel(selector)
		if err != nil {
			return err
		}
		envs = []string{envPath}
	}
	if len(envs) == 0 {
		return errors.New("no local agent env files found")
	}
	if err := registerEnvFiles(*apiURL, resolvedPassword, envs); err != nil {
		return err
	}
	for _, envPath := range envs {
		agent, err := loadAgentConfig(envPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "registered: %s (%s)\n", agent.Label, agent.ID)
	}
	return nil
}

func runRebuild(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverOnly := fs.Bool("server-only", false, "rebuild only the server")
	agentsOnly := fs.Bool("agents", false, "rebuild known agents only")
	agentLabel := fs.String("agent", "", "rebuild one agent label")
	offlinePEAS := fs.Bool("offline-peas", false, "update PEAS cache before rebuilding agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *serverOnly && (*agentsOnly || *agentLabel != "") {
		return errors.New("--server-only cannot be combined with agent rebuild flags")
	}

	m := loadManifestOrDefault()
	changed := []string{}
	if !*agentsOnly && *agentLabel == "" {
		if *serverOnly || serverNeedsRebuild(serverBinary(runtime.GOOS)) {
			out, err := buildServer(runner, stdout, stderr)
			if err != nil {
				return err
			}
			changed = append(changed, out)
			addUnique(&m.Builds, out)
		}
	}
	if !*serverOnly {
		if *offlinePEAS {
			if err := runGoCommand(runner, []string{"run", "./tools/updatepeas"}, nil, stdout, stderr); err != nil {
				return fmt.Errorf("update PEAS cache: %w", err)
			}
		}
		envs, err := knownAgentEnvPaths(m, *agentLabel)
		if err != nil {
			return err
		}
		for _, envPath := range envs {
			agent, err := loadAgentConfig(envPath)
			if err != nil {
				return err
			}
			target := agent.Target
			if target == "" {
				target = targetForEnv(m, envPath)
			}
			if target == "" {
				target = "linux"
			}
			out, err := buildAgent(runner, agent, target, stdout, stderr)
			if err != nil {
				return err
			}
			changed = append(changed, out)
			addAgentPath(&m, envPath, target)
			addUnique(&m.Builds, out)
		}
	}
	if err := saveManifest(m); err != nil {
		return err
	}
	printChanged(stdout, changed)
	return nil
}

func runUpdate(args []string, runner commandRunner, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	offlinePEAS := fs.Bool("offline-peas", false, "update PEAS cache before rebuilding agents")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := runner.Run("git", []string{"pull", "--ff-only"}, nil, stdout, stderr); err != nil {
		return fmt.Errorf("git pull: %w", err)
	}
	rebuildArgs := []string{}
	if *offlinePEAS {
		rebuildArgs = append(rebuildArgs, "--offline-peas")
	}
	return runRebuild(rebuildArgs, runner, stdout, stderr)
}

func runRemove(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keepState := fs.Bool("keep-state", false, "preserve the state file")
	all := fs.Bool("all", false, "also remove the manifest and empty install directories")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}
	targets := manifestTargets(m, *keepState)
	removed := []string{}
	for _, target := range targets {
		if err := removeTrackedPath(target); err != nil {
			return err
		}
		removed = append(removed, filepath.ToSlash(target))
	}
	if err := removeTrackedPath(manifestPath); err != nil {
		return err
	}
	removed = append(removed, manifestPath)
	if *all {
		pruneEmptyDir(".sable")
		pruneEmptyDir("agents")
		pruneEmptyDir("builds")
	}
	sort.Strings(removed)
	for _, path := range removed {
		fmt.Fprintf(stdout, "removed: %s\n", path)
	}
	return nil
}

func runReset(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keepState := fs.Bool("keep-state", false, "preserve the server state file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return resetInstallation(*keepState, stdout, true)
}

func resetInstallation(keepState bool, stdout io.Writer, showNext bool) error {
	if err := ensureServerStoppedForReset(); err != nil {
		return err
	}
	targets := defaultResetTargets(runtime.GOOS, keepState)
	if m, err := loadManifest(); err == nil {
		targets = append(targets, m.Config, m.Cert, m.Key, m.PasswordFile)
		if !keepState {
			targets = append(targets, m.State, m.StateKeyFile)
		}
		targets = append(targets, m.Agents...)
		targets = append(targets, m.Builds...)
		targets = append(targets, m.Logs...)
	}
	targets = append(targets, manifestPath)

	removed, failed := wipePaths(cleanList(targets))
	pruneEmptyDir(".sable")
	pruneEmptyDir("agents")
	pruneEmptyDir("builds")
	for _, path := range removed {
		fmt.Fprintf(stdout, "removed: %s\n", path)
	}
	for _, f := range failed {
		fmt.Fprintf(stdout, "skipped: %s (%s)\n", f.Path, f.Reason)
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %d path(s); stop running processes (sable-server, agents) and rerun reset", len(failed))
	}
	if len(removed) == 0 {
		fmt.Fprintln(stdout, "nothing to remove")
		return nil
	}
	if showNext {
		fmt.Fprintln(stdout, "next: sablectl install --url https://<your-server-ip>:443 --password-file ./pw.txt")
	}
	return nil
}

func ensureServerStoppedForReset() error {
	if apiListenerReachable(defaultAPIURL) {
		return errors.New("the Sable API listener is still running; run `sablectl down` before reset (nothing was removed)")
	}
	pid, err := readServerPID()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot verify the managed server is stopped: %w", err)
	}
	if err := waitForProcessExit(pid, 50*time.Millisecond); err != nil {
		return fmt.Errorf("managed Sable server process %d is still running; run `sablectl down` before reset (nothing was removed)", pid)
	}
	removeServerPID()
	return nil
}

func defaultResetTargets(goos string, keepState bool) []string {
	targets := []string{
		"config.env",
		"server.crt",
		"server.key",
		serverPIDPath,
		defaultStateKeyPath,
		"agents",
		"builds",
		serverBinary(goos),
	}
	if !keepState {
		targets = append(targets, defaultStatePath, defaultStatePath+".artifacts")
	}
	return targets
}

type wipeFailure struct {
	Path   string
	Reason string
}

// wipePaths removes each path best-effort and reports both successes and
// failures. Continuing past errors matters on Windows where a running
// sable-server.exe can't be deleted: we still want to clear config.env, certs,
// and state so the operator only has to deal with the live process.
func wipePaths(paths []string) ([]string, []wipeFailure) {
	removed := []string{}
	failed := []wipeFailure{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		slash := filepath.ToSlash(path)
		existed := pathExists(path)
		if err := removeTrackedPath(path); err != nil {
			failed = append(failed, wipeFailure{Path: slash, Reason: err.Error()})
			continue
		}
		if existed {
			removed = append(removed, slash)
		}
	}
	sort.Strings(removed)
	sort.Slice(failed, func(i, j int) bool { return failed[i].Path < failed[j].Path })
	return removed, failed
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runDoctor(args []string, runner commandRunner, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	apiURL := fs.String("api", defaultAPIURL, "operator API URL")
	fixPermissions := fs.Bool("fix-permissions", false, "tighten ACLs/modes on existing sensitive local files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := requireLoopbackAPIURL(*apiURL); err != nil {
		return err
	}
	checks := []doctorCheck{
		checkGo(runner),
		checkPort("agent https", "tcp", ":443"),
		checkPort("operator api", "tcp", "127.0.0.1:8443"),
		checkFiles(),
		checkCertConfigMatch(),
		checkStateWritable(),
		checkSensitivePermissions(*fixPermissions),
		checkServerFresh(),
		checkAgentEnvs(),
		checkServerAgents(*apiURL),
	}
	failed := false
	for _, check := range checks {
		status := "ok"
		if check.Warn {
			status = "warn"
		}
		if check.Err != "" {
			status = "fail"
			failed = true
		}
		message := check.Message
		if check.Err != "" {
			message = check.Err
		}
		fmt.Fprintf(stdout, "%-5s %s", status, check.Name)
		if message != "" {
			fmt.Fprintf(stdout, " - %s", message)
		}
		fmt.Fprintln(stdout)
	}
	if failed {
		return errors.New("doctor found failures")
	}
	return nil
}

func buildServer(runner commandRunner, stdout, stderr io.Writer) (string, error) {
	out := serverBinary(runtime.GOOS)
	if err := runGoCommand(runner, []string{"build", "-o", out, "./cmd/server"}, nil, stdout, stderr); err != nil {
		return "", fmt.Errorf("build server: %w", err)
	}
	return out, nil
}

func buildAgent(runner commandRunner, agent agentConfig, target string, stdout, stderr io.Writer) (string, error) {
	out := agentOutputPath(agent.Label, target)
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return "", fmt.Errorf("create build directory: %w", err)
	}
	env := []string{"GOOS=" + target, "GOARCH=amd64"}
	args := []string{"build", "-ldflags", agentLDFlags(agent), "-o", out, "./cmd/agent"}
	if err := runGoCommand(runner, args, env, stdout, stderr); err != nil {
		return "", fmt.Errorf("build %s agent %s: %w", target, agent.Label, err)
	}
	if err := securefile.Restrict(out); err != nil {
		return "", fmt.Errorf("restrict %s agent %s: %w", target, agent.Label, err)
	}
	return out, nil
}

func startServer(runner commandRunner, binary, passwordFile, password, statePath, stateKeyPath, logPath string) error {
	if err := validatePersistentState(statePath, stateKeyPath); err != nil {
		return fmt.Errorf("persisted state is unreadable with the configured key: %w; restore the matching key or run `sablectl setup` to preserve the unreadable state as a recovery backup and start empty", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open server log: %w", err)
	}
	defer logFile.Close()
	args := []string{"--state-file", statePath, "--state-key-file", defaultString(stateKeyPath, "none")}
	env := []string{}
	if passwordFile != "" {
		args = append(args, "--password-file", passwordFile)
	} else {
		env = append(env, "SABLE_OPERATOR_PASSWORD="+password)
	}
	process, err := runner.Start("./"+binary, args, env, logFile, logFile)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	if process == nil {
		return errors.New("start server: process handle was not returned")
	}
	if err := recordServerPID(process.Pid); err != nil {
		_ = process.Kill()
		return err
	}
	return nil
}

func recordServerPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("record server PID: invalid PID %d", pid)
	}
	if err := os.MkdirAll(filepath.Dir(serverPIDPath), 0700); err != nil {
		return fmt.Errorf("create server PID directory: %w", err)
	}
	if err := securefile.WriteFile(serverPIDPath, []byte(fmt.Sprintf("%d\n", pid))); err != nil {
		return fmt.Errorf("record server PID: %w", err)
	}
	return nil
}

func readServerPID() (int, error) {
	data, err := os.ReadFile(serverPIDPath)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0, errors.New("managed Sable PID file is invalid")
	}
	return pid, nil
}

func removeServerPID() {
	_ = os.Remove(serverPIDPath)
}

func terminateManagedServer(apiURL string) error {
	pid, err := readServerPID()
	if err != nil {
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find managed Sable server process %d: %w", pid, err)
	}
	if err := process.Kill(); err != nil {
		return fmt.Errorf("stop managed Sable server process %d: %w", pid, err)
	}
	if err := waitForServerStop(apiURL, 10*time.Second); err != nil {
		return err
	}
	if err := waitForProcessExit(pid, 15*time.Second); err != nil {
		return fmt.Errorf("wait for managed Sable server process %d: %w", pid, err)
	}
	removeServerPID()
	return nil
}

func registerEnvFiles(apiURL, password string, paths []string) error {
	client, err := apiClient()
	if err != nil {
		return err
	}
	token, err := login(client, apiURL, password)
	if err != nil {
		return err
	}
	registered, err := listAgents(client, apiURL, token)
	if err != nil {
		return err
	}
	unique := make(map[string]bool)
	for _, path := range paths {
		if unique[path] {
			continue
		}
		unique[path] = true
		agent, err := loadAgentConfig(path)
		if err != nil {
			return err
		}
		if displayName, exists := registered[agent.ID]; exists && displayName != "" {
			continue
		}
		if err := registerAgent(client, apiURL, token, agent.ID, agent.SecretHex, agent.Label); err != nil {
			return err
		}
	}
	return nil
}

func apiClient() (*http.Client, error) {
	return tlspin.NewClientFromCert("server.crt", 5*time.Second)
}

func login(client *http.Client, apiURL, password string) (string, error) {
	if err := requireLoopbackAPIURL(apiURL); err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post(strings.TrimRight(apiURL, "/")+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("login failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed (HTTP %d)", resp.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", errors.New("login returned no token")
	}
	return result.Token, nil
}

func listAgents(client *http.Client, apiURL, token string) (map[string]string, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/agents", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list agents failed (HTTP %d)", resp.StatusCode)
	}
	var agents []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(agents))
	for _, agent := range agents {
		ids[agent.ID] = agent.DisplayName
	}
	return ids, nil
}

func registerAgent(client *http.Client, apiURL, token, agentID, secretHex, displayName string) error {
	body, _ := json.Marshal(map[string]string{"id": agentID, "secret_hex": secretHex, "display_name": displayName})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+"/api/agents", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("register %s failed (HTTP %d): %s", agentID, resp.StatusCode, strings.TrimSpace(string(data)))
}

func requestShutdown(client *http.Client, apiURL, token string) error {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+"/api/admin/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("shutdown failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("shutdown failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func apiReachable(apiURL string) bool {
	if requireLoopbackAPIURL(apiURL) != nil {
		return false
	}
	client, err := apiClient()
	if err != nil {
		return false
	}
	resp, err := client.Get(strings.TrimRight(apiURL, "/") + "/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func requireLoopbackAPIURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid API URL: %w", err)
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("API URL must be an HTTPS loopback origin without credentials, path, query, or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("operator API must use a loopback host, got %q", host)
	}
	return nil
}

func validateAgentServerURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("agent URL must be an absolute https:// origin")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("agent URL must not include credentials, a path, query, or fragment")
	}
	return nil
}

func ensureStateKeyFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err == nil {
		return validateAndRestrictStateKey(path, data)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read state key file: %w", err)
	}
	key, err := randomHex(32)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := securefile.WriteFile(path, []byte(key+"\n")); err != nil {
		return fmt.Errorf("create state key file: %w", err)
	}
	return nil
}

func validatePersistentState(statePath, stateKeyPath string) error {
	if !statePersistenceEnabled(statePath) {
		return nil
	}
	var key []byte
	stateKeyPath = normalizeStateKeyPath(stateKeyPath)
	if stateKeyPath != "" {
		data, err := os.ReadFile(stateKeyPath)
		if err != nil {
			if os.IsNotExist(err) {
				return session.ValidatePersistentState(statePath, nil)
			}
			return fmt.Errorf("read state key file: %w", err)
		}
		key, err = decodeStateKeyData(data)
		if err != nil {
			return fmt.Errorf("read state key file: %w", err)
		}
	}
	return session.ValidatePersistentState(statePath, key)
}

func decodeStateKeyData(data []byte) ([]byte, error) {
	if len(data) == 32 {
		return append([]byte(nil), data...), nil
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("state key must contain 32 raw bytes or 64 hexadecimal characters")
	}
	return decoded, nil
}

func archivePersistentState(statePath string, now time.Time) ([]string, error) {
	if !statePersistenceEnabled(statePath) || !pathExists(statePath) {
		return nil, nil
	}
	suffix := ".recovery-" + now.UTC().Format("20060102T150405Z")
	stateBackup := statePath + suffix
	for counter := 2; pathExists(stateBackup) || pathExists(stateBackup+".artifacts"); counter++ {
		stateBackup = fmt.Sprintf("%s%s-%d", statePath, suffix, counter)
	}
	if err := os.Rename(statePath, stateBackup); err != nil {
		return nil, fmt.Errorf("preserve unreadable state: %w", err)
	}
	archived := []string{stateBackup}
	artifactPath := statePath + ".artifacts"
	if pathExists(artifactPath) {
		artifactBackup := stateBackup + ".artifacts"
		if err := os.Rename(artifactPath, artifactBackup); err != nil {
			_ = os.Rename(stateBackup, statePath)
			return nil, fmt.Errorf("preserve unreadable state artifacts: %w", err)
		}
		archived = append(archived, artifactBackup)
	}
	return archived, nil
}

func normalizeStateKeyPath(path string) string {
	path = strings.TrimSpace(path)
	switch strings.ToLower(path) {
	case "", "none", "off", "disabled":
		return ""
	default:
		return path
	}
}

func manifestStateKeyDefault(m manifest) string {
	if m.StateEncryptionDisabled {
		return "none"
	}
	return defaultString(m.StateKeyFile, filepath.FromSlash(defaultStateKeyPath))
}

func statePersistenceEnabled(path string) bool {
	switch strings.ToLower(strings.TrimSpace(path)) {
	case "", "none", "off", "disabled":
		return false
	default:
		return true
	}
}

func validateAndRestrictStateKey(path string, data []byte) error {
	if !validStateKeyData(data) {
		return fmt.Errorf("%s must contain 32 raw bytes or 64 hexadecimal characters", path)
	}
	return securefile.Restrict(path)
}

func validStateKeyData(data []byte) bool {
	_, err := decodeStateKeyData(data)
	return err == nil
}

func waitForAPI(apiURL string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if apiReachable(apiURL) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// parseInterspersed parses flags that may appear before, between, or after
// positional arguments. The standard flag package stops at the first non-flag
// token, which forces every subcommand caller to put flags before positionals.
// This loop pulls one positional at a time and resumes flag parsing on what is
// left, so "agent register main --password-file ./pw.txt" works.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	remaining := args
	for {
		if err := fs.Parse(remaining); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		remaining = fs.Args()[1:]
	}
}

// passwordFileOrManifest returns the explicit --password-file path when
// provided, otherwise the path recorded in the install manifest. This lets
// `start` and `agent register` reuse the password file from `install` without
// the operator retyping it on every invocation.
func passwordFileOrManifest(explicit string, m manifest) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	if strings.TrimSpace(m.PasswordFile) == "" {
		return ""
	}
	if _, err := os.Stat(m.PasswordFile); err != nil {
		return ""
	}
	return m.PasswordFile
}

func resolvePasswordFile(path, password string) (string, error) {
	password = strings.TrimSpace(password)
	if path == "" {
		return password, nil
	}
	if data, err := os.ReadFile(path); err == nil {
		return operatorpw.Normalize(data), nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read password file: %w", err)
	}
	if password == "" {
		generated, err := randomHex(18)
		if err != nil {
			return "", err
		}
		password = generated
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", fmt.Errorf("create password directory: %w", err)
		}
	}
	if err := securefile.WriteFile(path, []byte(password+"\n")); err != nil {
		return "", fmt.Errorf("write password file: %w", err)
	}
	return password, nil
}

func readOperatorPassword(path, password string) (string, error) {
	password = strings.TrimSpace(password)
	if password != "" {
		return password, nil
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		password = operatorpw.Normalize(data)
		if password == "" {
			return "", errors.New("password file is empty")
		}
		return password, nil
	}
	password = strings.TrimSpace(os.Getenv("SABLE_OPERATOR_PASSWORD"))
	if password != "" {
		return password, nil
	}
	return "", errors.New("supply --password-file, --password, or SABLE_OPERATOR_PASSWORD")
}

func buildConfigEnv(agent agentConfig) []byte {
	lines := []string{
		"AGENT_ID=" + agent.ID,
		"AGENT_SECRET_HEX=" + agent.SecretHex,
		"CERT_FP_HEX=" + agent.CertFPHex,
		"SERVER_URL=" + agent.ServerURL,
		"AGENT_LABEL=" + agent.Label,
		"AGENT_PROFILE=" + defaultString(agent.Profile, "default"),
		"SLEEP_SECONDS=" + defaultString(agent.SleepSeconds, "30"),
	}
	if strings.TrimSpace(agent.DNSDomain) != "" {
		lines = append(lines, "DNS_DOMAIN="+agent.DNSDomain)
	}
	if strings.TrimSpace(agent.Target) != "" {
		lines = append(lines, "AGENT_TARGET="+agent.Target)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func loadAgentConfig(path string) (agentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentConfig{}, err
	}
	values := parseEnv(data)
	agent := agentConfig{
		ID:           values["AGENT_ID"],
		SecretHex:    values["AGENT_SECRET_HEX"],
		CertFPHex:    values["CERT_FP_HEX"],
		ServerURL:    values["SERVER_URL"],
		Label:        values["AGENT_LABEL"],
		Profile:      values["AGENT_PROFILE"],
		SleepSeconds: values["SLEEP_SECONDS"],
		DNSDomain:    values["DNS_DOMAIN"],
		Target:       values["AGENT_TARGET"],
	}
	if agent.Label == "" {
		agent.Label = agentlabel.FromUUIDPrefix(agent.ID)
	}
	if agent.Profile == "" {
		agent.Profile = "default"
	}
	if agent.SleepSeconds == "" {
		agent.SleepSeconds = "30"
	}
	if agent.ID == "" || agent.SecretHex == "" || agent.CertFPHex == "" || agent.ServerURL == "" || agent.Label == "" {
		return agentConfig{}, fmt.Errorf("%s is missing required agent build values", path)
	}
	return agent, nil
}

func parseEnv(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return values
}

func loadManifestOrDefault() manifest {
	m, err := loadManifest()
	if err == nil {
		return m
	}
	return manifest{
		Config:       "config.env",
		State:        defaultStatePath,
		Cert:         "server.crt",
		Key:          "server.key",
		AgentTargets: map[string]string{},
	}
}

func loadManifest() (manifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if m.AgentTargets == nil {
		m.AgentTargets = map[string]string{}
	}
	return m, nil
}

func saveManifest(m manifest) error {
	if m.AgentTargets == nil {
		m.AgentTargets = map[string]string{}
	}
	m.Agents = cleanList(m.Agents)
	m.Builds = cleanList(m.Builds)
	m.Logs = cleanList(m.Logs)
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return securefile.WriteFile(manifestPath, append(data, '\n'))
}

func addAgentPath(m *manifest, path, target string) {
	if path == "" {
		return
	}
	addUnique(&m.Agents, filepath.ToSlash(path))
	if m.AgentTargets == nil {
		m.AgentTargets = map[string]string{}
	}
	if target != "" {
		m.AgentTargets[filepath.ToSlash(path)] = target
	}
}

func addUnique(values *[]string, value string) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func cleanList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = filepath.ToSlash(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func manifestTargets(m manifest, keepState bool) []string {
	targets := []string{m.Config, m.Cert, m.Key, m.PasswordFile}
	if !keepState {
		targets = append(targets, m.State, m.StateKeyFile)
	}
	targets = append(targets, m.Agents...)
	targets = append(targets, m.Builds...)
	targets = append(targets, m.Logs...)
	return cleanList(targets)
}

func removeTrackedPath(path string) error {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove %s", path)
	}
	absRoot, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return err
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing to remove outside workspace: %s", path)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(path)
}

func pruneEmptyDir(path string) {
	_ = os.Remove(path)
}

func knownAgentEnvPaths(m manifest, label string) ([]string, error) {
	if label != "" {
		envPath, _, err := findAgentByLabel(label)
		if err != nil {
			return nil, err
		}
		return []string{envPath}, nil
	}
	envs := append([]string{}, m.Agents...)
	if len(envs) == 0 {
		if _, err := os.Stat("config.env"); err == nil {
			envs = append(envs, "config.env")
		}
		matches, _ := filepath.Glob(filepath.Join("agents", "*.env"))
		envs = append(envs, matches...)
	}
	return cleanList(envs), nil
}

func findAgentByLabel(label string) (string, agentConfig, error) {
	candidates := []string{"config.env", filepath.Join("agents", label+".env")}
	matches, _ := filepath.Glob(filepath.Join("agents", "*.env"))
	candidates = append(candidates, matches...)
	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] {
			continue
		}
		seen[path] = true
		agent, err := loadAgentConfig(path)
		if err != nil {
			continue
		}
		if agent.Label == label {
			return path, agent, nil
		}
	}
	return "", agentConfig{}, fmt.Errorf("agent label %q not found", label)
}

func targetForEnv(m manifest, envPath string) string {
	if m.AgentTargets == nil {
		return ""
	}
	return m.AgentTargets[filepath.ToSlash(envPath)]
}

func envPathsForBuilds(builds []plannedAgentBuild) []string {
	paths := []string{}
	for _, build := range builds {
		paths = append(paths, build.envPath)
	}
	return cleanList(paths)
}

func serverNeedsRebuild(binary string) bool {
	info, err := os.Stat(binary)
	if err != nil {
		return true
	}
	newest := newestModTime([]string{"cmd/server", "internal", "web", "go.mod", "go.sum"})
	return newest.After(info.ModTime())
}

func newestModTime(paths []string) time.Time {
	var newest time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			continue
		}
		_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
	}
	return newest
}

func printInstallSummary(out io.Writer, m manifest, changed []string, registered bool, cfg installConfig) {
	fmt.Fprintln(out, "artifacts:")
	for _, path := range cleanList(append([]string{m.Config, m.State, m.StateKeyFile, m.Cert, m.Key, m.PasswordFile}, append(m.Agents, changed...)...)) {
		if path != "" {
			fmt.Fprintf(out, "  %s\n", filepath.ToSlash(path))
		}
	}
	for _, line := range nextStepsAfterInstall(cfg, registered) {
		fmt.Fprintf(out, "next: %s\n", line)
	}
}

func nextStepsAfterInstall(cfg installConfig, registered bool) []string {
	if cfg.Start && registered {
		return []string{"transfer built agents to authorized targets"}
	}
	if cfg.Start {
		return []string{"sablectl agent register --password-file " + filepath.ToSlash(cfg.PasswordFile)}
	}
	return []string{"sablectl up"}
}

func printChanged(out io.Writer, changed []string) {
	changed = cleanList(changed)
	if len(changed) == 0 {
		fmt.Fprintln(out, "changed: none")
		return
	}
	fmt.Fprintln(out, "changed:")
	for _, path := range changed {
		fmt.Fprintf(out, "  %s\n", filepath.ToSlash(path))
	}
}

func resolveProfile(value string) (string, string, error) {
	name := strings.ToLower(strings.TrimSpace(value))
	if name == "" {
		name = "default"
	}
	switch name {
	case "default":
		return name, "30", nil
	case "fast":
		return name, "5", nil
	case "quiet":
		return name, "120", nil
	case "dns":
		return name, "60", nil
	default:
		return "", "", fmt.Errorf("unknown profile %q", value)
	}
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func validAgentTarget(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "linux", "windows", "both", "none":
		return true
	default:
		return false
	}
}

func primaryTarget(agentTargets string) string {
	switch strings.ToLower(strings.TrimSpace(agentTargets)) {
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func defaultAdditionalLabel(primaryLabel, target string) string {
	candidates := []string{target, primaryLabel + "-" + target}
	for _, candidate := range candidates {
		if err := agentlabel.Validate(candidate); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join("agents", candidate+".env")); os.IsNotExist(err) {
			return candidate
		}
	}
	return target
}

func serverBinary(goos string) string {
	if goos == "windows" {
		return "sable-server.exe"
	}
	return "sable-server"
}

func sablectlBinary(goos string) string {
	if goos == "windows" {
		return "sablectl.exe"
	}
	return "sablectl"
}

func agentOutputPath(label, target string) string {
	if target == "windows" {
		return filepath.Join("builds", label, "agent.exe")
	}
	return filepath.Join("builds", label, "agent-"+target)
}

func agentLDFlags(agent agentConfig) string {
	pairs := []string{
		"-s -w",
		"-X " + modulePath + "/internal/agent.AgentID=" + agent.ID,
		"-X " + modulePath + "/internal/agent.SecretHex=" + agent.SecretHex,
		"-X " + modulePath + "/internal/agent.ServerURL=" + agent.ServerURL,
		"-X " + modulePath + "/internal/agent.CertFingerprintHex=" + agent.CertFPHex,
		"-X " + modulePath + "/internal/agent.SleepSecondsStr=" + agent.SleepSeconds,
		"-X " + modulePath + "/internal/agent.DNSDomainStr=" + agent.DNSDomain,
	}
	return strings.Join(pairs, " ")
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type doctorCheck struct {
	Name    string
	Message string
	Err     string
	Warn    bool
}

func checkGo(runner commandRunner) doctorCheck {
	var out bytes.Buffer
	if err := runner.Run("go", []string{"version"}, nil, &out, &out); err != nil {
		return doctorCheck{Name: "go", Err: err.Error()}
	}
	version := strings.TrimSpace(out.String())
	required := requiredGoVersion()
	if required != "" && !goVersionAtLeast(version, required) {
		return doctorCheck{Name: "go", Message: version + " (go.mod requires " + required + ")", Warn: true}
	}
	return doctorCheck{Name: "go", Message: version}
}

func goVersionAtLeast(output, required string) bool {
	var actual string
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, "go") && len(field) > len("go") && field[2] >= '0' && field[2] <= '9' {
			actual = strings.TrimPrefix(field, "go")
			break
		}
	}
	if actual == "" {
		return false
	}
	return compareDottedVersion(actual, required) >= 0
}

func compareDottedVersion(actual, required string) int {
	a := versionParts(actual)
	r := versionParts(required)
	for len(a) < len(r) {
		a = append(a, 0)
	}
	for len(r) < len(a) {
		r = append(r, 0)
	}
	for i := range a {
		if a[i] > r[i] {
			return 1
		}
		if a[i] < r[i] {
			return -1
		}
	}
	return 0
}

func versionParts(value string) []int {
	parts := []int{}
	for _, part := range strings.Split(value, ".") {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		parts = append(parts, n)
	}
	return parts
}

func requiredGoVersion() string {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			return fields[1]
		}
	}
	return ""
}

func checkPort(name, network, address string) doctorCheck {
	ln, err := net.Listen(network, address)
	if err != nil {
		return doctorCheck{Name: name, Message: "not available: " + err.Error(), Warn: true}
	}
	_ = ln.Close()
	return doctorCheck{Name: name, Message: "available"}
}

func checkFiles() doctorCheck {
	m := loadManifestOrDefault()
	missing := []string{}
	for _, path := range []string{m.Config, m.Cert, m.Key} {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return doctorCheck{Name: "files", Err: "missing " + strings.Join(missing, ", ")}
	}
	return doctorCheck{Name: "files", Message: "config, cert, and key exist"}
}

func checkCertConfigMatch() doctorCheck {
	certPEM, err := os.ReadFile("server.crt")
	if err != nil {
		return doctorCheck{Name: "cert", Err: err.Error()}
	}
	keyPEM, err := os.ReadFile("server.key")
	if err != nil {
		return doctorCheck{Name: "cert", Err: err.Error()}
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return doctorCheck{Name: "cert", Err: err.Error()}
	}
	if len(cert.Certificate) == 0 {
		return doctorCheck{Name: "cert", Err: "empty certificate chain"}
	}
	sum := sha256.Sum256(cert.Certificate[0])
	fp := hex.EncodeToString(sum[:])
	agent, err := loadAgentConfig("config.env")
	if err != nil {
		return doctorCheck{Name: "cert", Err: err.Error()}
	}
	if !strings.EqualFold(agent.CertFPHex, fp) {
		return doctorCheck{Name: "cert", Err: "config.env fingerprint does not match server.crt"}
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return doctorCheck{Name: "cert", Err: err.Error()}
	}
	remaining := time.Until(leaf.NotAfter)
	if remaining <= 0 {
		return doctorCheck{Name: "cert", Err: "certificate expired " + leaf.NotAfter.Format(time.RFC3339)}
	}
	if remaining <= 30*24*time.Hour {
		return doctorCheck{Name: "cert", Message: "fingerprint matches; expires " + leaf.NotAfter.Format(time.RFC3339), Warn: true}
	}
	return doctorCheck{Name: "cert", Message: "fingerprint matches; expires " + leaf.NotAfter.Format(time.RFC3339)}
}

func checkStateWritable() doctorCheck {
	m := loadManifestOrDefault()
	path := m.State
	if path == "" {
		path = defaultStatePath
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return doctorCheck{Name: "state", Err: err.Error()}
	}
	if err := f.Close(); err != nil {
		return doctorCheck{Name: "state", Err: err.Error()}
	}
	if err := securefile.Restrict(path); err != nil {
		return doctorCheck{Name: "state", Err: err.Error()}
	}
	message := filepath.ToSlash(path) + " writable"
	if m.StateKeyFile != "" {
		keyData, err := os.ReadFile(m.StateKeyFile)
		if err != nil {
			return doctorCheck{Name: "state", Err: "state key: " + err.Error()}
		}
		if !validStateKeyData(keyData) {
			return doctorCheck{Name: "state", Err: "state key must contain 32 raw bytes or 64 hexadecimal characters"}
		}
		message += "; encryption key valid"
	}
	return doctorCheck{Name: "state", Message: message}
}

func checkSensitivePermissions(fix bool) doctorCheck {
	m := loadManifestOrDefault()
	warnings := []string{}
	errorsSeen := []string{}
	for _, path := range sensitiveLocalPaths(m) {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		if fix {
			if err := securefile.Restrict(path); err != nil {
				errorsSeen = append(errorsSeen, filepath.ToSlash(path)+": "+err.Error())
				continue
			}
		}
		warning, err := securefile.PermissionWarning(path)
		if err != nil {
			errorsSeen = append(errorsSeen, filepath.ToSlash(path)+": "+err.Error())
			continue
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	if len(errorsSeen) > 0 {
		return doctorCheck{Name: "permissions", Message: strings.Join(errorsSeen, "; "), Warn: true}
	}
	if len(warnings) > 0 {
		return doctorCheck{Name: "permissions", Message: strings.Join(warnings, "; "), Warn: true}
	}
	if fix {
		return doctorCheck{Name: "permissions", Message: "sensitive files hardened"}
	}
	return doctorCheck{Name: "permissions", Message: "sensitive files are owner/admin scoped"}
}

func sensitiveLocalPaths(m manifest) []string {
	paths := []string{m.Config, m.State, m.StateKeyFile, m.Cert, m.Key, m.PasswordFile, manifestPath}
	paths = append(paths, m.Agents...)
	paths = append(paths, m.Builds...)
	return cleanList(paths)
}

func checkServerFresh() doctorCheck {
	binary := serverBinary(runtime.GOOS)
	if _, err := os.Stat(binary); err != nil {
		return doctorCheck{Name: "server build", Err: binary + " missing"}
	}
	if serverNeedsRebuild(binary) {
		return doctorCheck{Name: "server build", Message: binary + " is older than source/web assets", Warn: true}
	}
	return doctorCheck{Name: "server build", Message: binary + " current"}
}

func checkAgentEnvs() doctorCheck {
	m := loadManifestOrDefault()
	envs, _ := knownAgentEnvPaths(m, "")
	if len(envs) == 0 {
		return doctorCheck{Name: "agents", Message: "no local agent env files", Warn: true}
	}
	for _, envPath := range envs {
		agent, err := loadAgentConfig(envPath)
		if err != nil {
			return doctorCheck{Name: "agents", Err: err.Error()}
		}
		if _, err := hex.DecodeString(agent.SecretHex); err != nil || len(agent.SecretHex) != 64 {
			return doctorCheck{Name: "agents", Err: envPath + " has invalid AGENT_SECRET_HEX"}
		}
		if _, err := hex.DecodeString(agent.CertFPHex); err != nil || len(agent.CertFPHex) != 64 {
			return doctorCheck{Name: "agents", Err: envPath + " has invalid CERT_FP_HEX"}
		}
	}
	return doctorCheck{Name: "agents", Message: fmt.Sprintf("%d env file(s) valid", len(envs))}
}

func checkServerAgents(apiURL string) doctorCheck {
	if !apiReachable(apiURL) {
		return doctorCheck{Name: "registration", Message: "server not reachable; skipped", Warn: true}
	}
	return doctorCheck{Name: "registration", Message: "server reachable; run with password-backed install/start to verify IDs"}
}

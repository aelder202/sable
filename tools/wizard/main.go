package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aelder202/sable/internal/agentlabel"
	"github.com/aelder202/sable/internal/listener"
	"github.com/google/uuid"
)

const modulePath = "github.com/aelder202/sable"

type wizardConfig struct {
	ServerURL    string
	Label        string
	Profile      string
	DNSDomain    string
	WindowsLabel string
	BuildServer  bool
	AgentTargets string
	AssumeYes    bool
	WipeClean    bool
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
}

type commandRunner interface {
	Run(name string, args []string, env []string) error
}

type execRunner struct{}

func (execRunner) Run(name string, args []string, env []string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := runWizard(os.Stdin, os.Stdout, execRunner{}, cfg); err != nil {
		log.Fatal(err)
	}
}

func parseFlags(args []string) (*wizardConfig, error) {
	fs := flag.NewFlagSet("wizard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfg := &wizardConfig{
		BuildServer:  true,
		AgentTargets: "linux",
	}
	fs.StringVar(&cfg.ServerURL, "server-url", "", "agent listener URL, for example https://203.0.113.10:443")
	fs.StringVar(&cfg.Label, "label", "", "initial agent label")
	fs.StringVar(&cfg.WindowsLabel, "windows-label", "", "label for the separate Windows identity when --agents both is used")
	fs.StringVar(&cfg.Profile, "profile", "", "agent profile: default, fast, quiet, dns")
	fs.StringVar(&cfg.DNSDomain, "dns-domain", "", "DNS fallback domain when profile=dns")
	fs.BoolVar(&cfg.BuildServer, "server", true, "build the host-native server binary")
	fs.StringVar(&cfg.AgentTargets, "agents", "linux", "agent artifacts to build: linux, windows, both, none")
	fs.BoolVar(&cfg.AssumeYes, "yes", false, "accept defaults and do not prompt")
	fs.BoolVar(&cfg.WipeClean, "wipe-clean", false, "remove existing builds/, agents/, and config.env before running the wizard")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return cfg, nil
}

func performWipeClean(reader *bufio.Reader, out io.Writer) (bool, error) {
	candidates := []struct{ path, label string }{
		{"config.env", "config.env"},
		{"builds", "builds/"},
		{"agents", "agents/"},
	}

	var targets []string
	for _, c := range candidates {
		if _, err := os.Stat(c.path); err == nil {
			targets = append(targets, c.label)
		}
	}

	if len(targets) == 0 {
		fmt.Fprintln(out, "[*] Nothing to clean — no existing builds, agents, or config.env found.")
		fmt.Fprintln(out)
		return true, nil
	}

	fmt.Fprintln(out, "The following will be permanently removed:")
	for _, t := range targets {
		fmt.Fprintf(out, "  - %s\n", t)
	}
	if !promptYesNo(reader, out, "Proceed with clean", false) {
		fmt.Fprintln(out, "[*] Wipe-clean cancelled. No changes made.")
		return false, nil
	}

	for _, c := range candidates {
		if _, err := os.Stat(c.path); err != nil {
			continue
		}
		if err := os.RemoveAll(c.path); err != nil {
			return false, fmt.Errorf("remove %s: %w", c.label, err)
		}
		fmt.Fprintf(out, "[-] Removed %s\n", c.label)
	}
	fmt.Fprintln(out)
	return true, nil
}

func runWizard(in io.Reader, out io.Writer, runner commandRunner, cfg *wizardConfig) error {
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, "Sable setup wizard")
	fmt.Fprintln(out, "This wizard prepares local config and build artifacts only. Remote deployment remains an explicit operator step.")
	fmt.Fprintln(out)

	if cfg.WipeClean {
		proceed, err := performWipeClean(reader, out)
		if err != nil {
			return err
		}
		if !proceed {
			return nil
		}
	}

	agent, created, err := ensureConfig(reader, out, cfg)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(out, "[+] Created config.env for label %q\n", agent.Label)
	} else {
		fmt.Fprintf(out, "[*] Using existing config.env for label %q\n", agent.Label)
	}

	plan, err := resolveBuildPlan(reader, out, cfg)
	if err != nil {
		return err
	}
	if err := prepareAgentBuilds(reader, out, in, agent, &plan, cfg); err != nil {
		return err
	}
	if err := runBuildPlan(out, runner, agent, plan); err != nil {
		return err
	}

	printNextSteps(out, agent, plan)
	return nil
}

type buildPlan struct {
	Server       bool
	AgentTarget  string
	AgentBuilds  []agentBuild
	ExtraEnvPath string
}

type agentBuild struct {
	Target  string
	Agent   agentConfig
	EnvPath string
	Primary bool
}

func ensureConfig(reader *bufio.Reader, out io.Writer, cfg *wizardConfig) (agentConfig, bool, error) {
	if existing, err := loadAgentConfig("config.env"); err == nil {
		return existing, false, nil
	} else if !os.IsNotExist(err) {
		return agentConfig{}, false, fmt.Errorf("read config.env: %w", err)
	}

	if cfg.AssumeYes && strings.TrimSpace(cfg.ServerURL) == "" {
		return agentConfig{}, false, fmt.Errorf("--server-url is required with --yes when config.env does not exist")
	}

	serverURL := strings.TrimSpace(cfg.ServerURL)
	if serverURL == "" {
		serverURL = prompt(reader, out, "Agent listener URL", "https://127.0.0.1:443")
	}
	if serverURL == "" {
		return agentConfig{}, false, fmt.Errorf("server URL is required")
	}

	label := strings.TrimSpace(cfg.Label)
	if label == "" {
		if cfg.AssumeYes {
			label = "main"
		} else {
			label = prompt(reader, out, "Initial agent label", "main")
		}
	}
	if err := agentlabel.Validate(label); err != nil {
		return agentConfig{}, false, err
	}

	profile, sleep, err := resolveProfile(cfg.Profile)
	if err != nil {
		return agentConfig{}, false, err
	}
	if cfg.Profile == "" && !cfg.AssumeYes {
		profileInput := prompt(reader, out, "Agent profile [default, fast, quiet, dns]", profile)
		profile, sleep, err = resolveProfile(profileInput)
		if err != nil {
			return agentConfig{}, false, err
		}
	}

	dnsDomain := strings.TrimSpace(cfg.DNSDomain)
	if profile == "dns" && dnsDomain == "" {
		dnsDomain = prompt(reader, out, "DNS fallback domain", "")
	}
	if profile == "dns" && dnsDomain == "" {
		return agentConfig{}, false, fmt.Errorf("dns profile requires a DNS fallback domain")
	}

	agentID := uuid.New().String()
	secretHex, err := randomHex(32)
	if err != nil {
		return agentConfig{}, false, err
	}
	_, fp, err := listener.LoadOrCreateCert("server.crt", "server.key")
	if err != nil {
		return agentConfig{}, false, fmt.Errorf("create certificate: %w", err)
	}

	agent := agentConfig{
		ID:           agentID,
		SecretHex:    secretHex,
		CertFPHex:    fp,
		ServerURL:    serverURL,
		Label:        label,
		Profile:      profile,
		SleepSeconds: sleep,
		DNSDomain:    dnsDomain,
	}
	if err := os.WriteFile("config.env", buildConfigEnv(agent), 0600); err != nil {
		return agentConfig{}, false, fmt.Errorf("write config.env: %w", err)
	}
	return agent, true, nil
}

func resolveBuildPlan(reader *bufio.Reader, out io.Writer, cfg *wizardConfig) (buildPlan, error) {
	plan := buildPlan{
		Server:      cfg.BuildServer,
		AgentTarget: strings.ToLower(strings.TrimSpace(cfg.AgentTargets)),
	}
	if plan.AgentTarget == "" {
		plan.AgentTarget = "linux"
	}
	if !cfg.AssumeYes {
		plan.Server = promptYesNo(reader, out, "Build server binary now", plan.Server)
		plan.AgentTarget = strings.ToLower(prompt(reader, out, "Build agent artifacts [linux, windows, both, none]", plan.AgentTarget))
	}
	if !validAgentTarget(plan.AgentTarget) {
		return buildPlan{}, fmt.Errorf("invalid agent target %q", plan.AgentTarget)
	}
	return plan, nil
}

func prepareAgentBuilds(reader *bufio.Reader, out io.Writer, _ io.Reader, primary agentConfig, plan *buildPlan, cfg *wizardConfig) error {
	switch plan.AgentTarget {
	case "none":
		plan.AgentBuilds = nil
	case "linux", "windows":
		plan.AgentBuilds = []agentBuild{{Target: plan.AgentTarget, Agent: primary, Primary: true}}
	case "both":
		fmt.Fprintln(out, "[*] Building Linux and Windows agents requires separate identities. Reusing one ID for both causes one session to alternate between platforms.")
		windowsAgent, envPath, created, err := ensureAdditionalAgent(reader, out, primary, cfg)
		if err != nil {
			return err
		}
		plan.ExtraEnvPath = envPath
		plan.AgentBuilds = []agentBuild{
			{Target: "linux", Agent: primary, Primary: true, EnvPath: "config.env"},
			{Target: "windows", Agent: windowsAgent, EnvPath: envPath},
		}
		if created {
			fmt.Fprintf(out, "[+] Created separate Windows identity: %s\n", filepath.ToSlash(envPath))
		} else {
			fmt.Fprintf(out, "[*] Using separate Windows identity: %s\n", filepath.ToSlash(envPath))
		}
	}
	return nil
}

func ensureAdditionalAgent(reader *bufio.Reader, out io.Writer, primary agentConfig, cfg *wizardConfig) (agentConfig, string, bool, error) {
	label := strings.TrimSpace(cfg.WindowsLabel)
	if label == "" {
		defaultLabel := defaultAdditionalLabel(primary.Label, "windows")
		if cfg.AssumeYes {
			label = defaultLabel
		} else {
			label = prompt(reader, out, "Windows agent label", defaultLabel)
		}
	}
	if err := agentlabel.Validate(label); err != nil {
		return agentConfig{}, "", false, err
	}

	envPath := filepath.Join("agents", label+".env")
	if existing, err := loadAgentConfig(envPath); err == nil {
		return existing, envPath, false, nil
	} else if !os.IsNotExist(err) {
		return agentConfig{}, "", false, fmt.Errorf("read %s: %w", envPath, err)
	}

	secretHex, err := randomHex(32)
	if err != nil {
		return agentConfig{}, "", false, err
	}
	agent := agentConfig{
		ID:           uuid.New().String(),
		SecretHex:    secretHex,
		CertFPHex:    primary.CertFPHex,
		ServerURL:    primary.ServerURL,
		Label:        label,
		Profile:      primary.Profile,
		SleepSeconds: primary.SleepSeconds,
		DNSDomain:    primary.DNSDomain,
	}
	if err := os.MkdirAll(filepath.Dir(envPath), 0700); err != nil {
		return agentConfig{}, "", false, fmt.Errorf("create agents directory: %w", err)
	}
	if err := os.WriteFile(envPath, buildConfigEnv(agent), 0600); err != nil {
		return agentConfig{}, "", false, fmt.Errorf("write %s: %w", envPath, err)
	}
	return agent, envPath, true, nil
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

func runBuildPlan(out io.Writer, runner commandRunner, agent agentConfig, plan buildPlan) error {
	if !plan.Server && plan.AgentTarget == "none" {
		fmt.Fprintln(out, "[*] No build selected.")
		return nil
	}
	if plan.Server {
		output := serverBinary(runtime.GOOS)
		fmt.Fprintf(out, "[*] Building server: %s\n", output)
		if err := runner.Run("go", []string{"build", "-o", output, "./cmd/server"}, nil); err != nil {
			return fmt.Errorf("build server: %w", err)
		}
	}

	builds := plan.AgentBuilds
	if builds == nil {
		if plan.AgentTarget == "both" {
			return fmt.Errorf("internal error: both-agent builds require separate identities")
		}
		builds = defaultAgentBuilds(agent, plan.AgentTarget)
	}
	for _, build := range builds {
		output := agentOutputPath(build.Agent.Label, build.Target)
		fmt.Fprintf(out, "[*] Building %s agent (%s): %s\n", build.Target, build.Agent.Label, filepath.ToSlash(output))
		if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
			return fmt.Errorf("create build directory: %w", err)
		}
		env := []string{"GOOS=" + build.Target, "GOARCH=amd64"}
		args := []string{"build", "-ldflags", agentLDFlags(build.Agent), "-o", output, "./cmd/agent"}
		if err := runner.Run("go", args, env); err != nil {
			return fmt.Errorf("build %s agent: %w", build.Target, err)
		}
	}
	return nil
}

func printNextSteps(out io.Writer, agent agentConfig, plan buildPlan) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Next steps")
	if plan.Server {
		fmt.Fprintf(out, "1. Start the server with %s --password-file ./pw.txt\n", serverBinary(runtime.GOOS))
	} else {
		fmt.Fprintln(out, "1. Rebuild or start the server when you are ready.")
	}
	fmt.Fprintln(out, "2. After the server is running, register the generated agent identities:")
	fmt.Fprintln(out, "   make register PASSWORD=<operator-password>")
	if plan.ExtraEnvPath != "" {
		fmt.Fprintf(out, "   make register PASSWORD=<operator-password> AGENT_ENV=%s\n", filepath.ToSlash(plan.ExtraEnvPath))
	}
	if plan.AgentTarget != "none" {
		fmt.Fprintln(out, "3. Transfer the agent artifact only to systems you own or have written authorization to test:")
		builds := plan.AgentBuilds
		if builds == nil {
			builds = defaultAgentBuilds(agent, plan.AgentTarget)
		}
		for _, build := range builds {
			fmt.Fprintf(out, "   - %s\n", filepath.ToSlash(agentOutputPath(build.Agent.Label, build.Target)))
		}
	} else {
		fmt.Fprintln(out, "3. Build agents later with make build-agent-linux or make build-agent-windows.")
	}
}

func prompt(reader *bufio.Reader, out io.Writer, label, defaultValue string) string {
	if defaultValue != "" {
		fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue
	}
	return value
}

func promptYesNo(reader *bufio.Reader, out io.Writer, label string, defaultValue bool) bool {
	defaultLabel := "y"
	if !defaultValue {
		defaultLabel = "n"
	}
	answer := strings.ToLower(prompt(reader, out, label+" [y/n]", defaultLabel))
	return answer == "y" || answer == "yes"
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

func buildConfigEnv(agent agentConfig) []byte {
	lines := []string{
		"AGENT_ID=" + agent.ID,
		"AGENT_SECRET_HEX=" + agent.SecretHex,
		"CERT_FP_HEX=" + agent.CertFPHex,
		"SERVER_URL=" + agent.ServerURL,
		"AGENT_LABEL=" + agent.Label,
		"AGENT_PROFILE=" + agent.Profile,
		"SLEEP_SECONDS=" + agent.SleepSeconds,
	}
	if strings.TrimSpace(agent.DNSDomain) != "" {
		lines = append(lines, "DNS_DOMAIN="+agent.DNSDomain)
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

func validAgentTarget(value string) bool {
	switch value {
	case "linux", "windows", "both", "none":
		return true
	default:
		return false
	}
}

func defaultAgentBuilds(agent agentConfig, value string) []agentBuild {
	targets := expandAgentTargets(value)
	builds := make([]agentBuild, 0, len(targets))
	for _, target := range targets {
		builds = append(builds, agentBuild{Target: target, Agent: agent, Primary: true, EnvPath: "config.env"})
	}
	return builds
}

func expandAgentTargets(value string) []string {
	switch value {
	case "linux", "windows":
		return []string{value}
	case "both":
		return []string{"linux", "windows"}
	default:
		return nil
	}
}

func serverBinary(goos string) string {
	if goos == "windows" {
		return "sable-server.exe"
	}
	return "sable-server"
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

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aelder202/sable/internal/agentlabel"
	"github.com/google/uuid"
)

const operatorAPIURL = "https://127.0.0.1:8443"

type registerConfig struct {
	AgentID      string
	SecretHex    string
	Password     string
	ServerURL    string
	CertFPHex    string
	SleepSeconds string
	DNSDomain    string
	OutputDir    string
	Label        string
	New          bool
}

type agentSummary struct {
	ID string `json:"id"`
}

func main() {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	client := newLoopbackClient()
	token, err := login(client, cfg.Password)
	if err != nil {
		log.Fatal(err)
	}

	agents, err := listAgents(client, token)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.New {
		if err := createAndRegisterNewAgent(client, token, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	if cfg.AgentID == "" || cfg.SecretHex == "" {
		fmt.Fprintln(os.Stderr, "[!] AGENT_ID and AGENT_SECRET_HEX not set. Run 'make setup' first or use 'make register NEW=1'")
		os.Exit(1)
	}

	if containsAgentID(agents, cfg.AgentID) {
		fmt.Printf("[*] Agent already registered: %s\n", cfg.AgentID)
		fmt.Println("    To create and register another agent, run: make register NEW=1 PASSWORD=<operator-password>")
		return
	}

	if err := registerAgent(client, token, cfg.AgentID, cfg.SecretHex); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("[+] Agent registered: %s\n", cfg.AgentID)
}

func parseConfig(args []string) (*registerConfig, error) {
	fs := flag.NewFlagSet("register-tool", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfg := &registerConfig{}
	fs.BoolVar(&cfg.New, "new", false, "generate and register a fresh agent")
	fs.StringVar(&cfg.Label, "label", "", "human-readable label (1-31 lowercase alphanumeric/-/_); omit for auto-generated prefix")
	fs.StringVar(&cfg.ServerURL, "server-url", "", "agent listener URL for newly generated env files")
	fs.StringVar(&cfg.CertFPHex, "cert-fp", "", "TLS fingerprint for newly generated env files")
	fs.StringVar(&cfg.SleepSeconds, "sleep-seconds", "", "default sleep interval for newly generated env files")
	fs.StringVar(&cfg.DNSDomain, "dns-domain", "", "DNS fallback domain for newly generated env files")
	fs.StringVar(&cfg.OutputDir, "output-dir", "agents", "output directory for newly generated env files")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, usageText())
	}

	rest := fs.Args()
	if len(rest) != 3 {
		return nil, fmt.Errorf("%s", usageText())
	}

	cfg.AgentID = strings.TrimSpace(rest[0])
	cfg.SecretHex = strings.TrimSpace(rest[1])
	cfg.Password = strings.TrimSpace(rest[2])
	if cfg.Password == "" {
		return nil, fmt.Errorf("%s", usageText())
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "agents"
	}
	return cfg, nil
}

func usageText() string {
	return "[!] Usage: make register PASSWORD=yourpassword [NEW=1] [LABEL=<label>]\n" +
		"    (AGENT_ID and AGENT_SECRET_HEX are read from the selected agent env file)\n" +
		"    NEW=1 creates, stores, and registers a fresh agent under agents/<label>.env"
}

func newLoopbackClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback self-signed cert
		},
	}
}

func login(client *http.Client, password string) (string, error) {
	loginBody, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post(operatorAPIURL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		return "", fmt.Errorf("login failed: %v - is the Sable server running?", err)
	}
	defer resp.Body.Close()

	var loginResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil || loginResp.Token == "" {
		return "", fmt.Errorf("login failed - wrong password or Sable server not running")
	}
	return loginResp.Token, nil
}

func listAgents(client *http.Client, token string) ([]agentSummary, error) {
	req, err := http.NewRequest(http.MethodGet, operatorAPIURL+"/api/agents", nil)
	if err != nil {
		return nil, fmt.Errorf("build list-agents request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list agents failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list agents failed (HTTP %d)", resp.StatusCode)
	}

	var agents []agentSummary
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	return agents, nil
}

func registerAgent(client *http.Client, token, agentID, secretHex string) error {
	regBody, _ := json.Marshal(map[string]string{"id": agentID, "secret_hex": secretHex})
	req, _ := http.NewRequest(http.MethodPost, operatorAPIURL+"/api/agents", bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	regResp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("register request failed: %w", err)
	}
	defer regResp.Body.Close()

	if regResp.StatusCode == http.StatusOK || regResp.StatusCode == http.StatusCreated || regResp.StatusCode == http.StatusNoContent {
		return nil
	}

	data, _ := io.ReadAll(io.LimitReader(regResp.Body, 2048))
	message := strings.TrimSpace(string(data))
	if message == "" {
		return fmt.Errorf("registration failed (HTTP %d)", regResp.StatusCode)
	}
	return fmt.Errorf("registration failed (HTTP %d): %s", regResp.StatusCode, message)
}

func createAndRegisterNewAgent(client *http.Client, token string, cfg *registerConfig) error {
	if strings.TrimSpace(cfg.ServerURL) == "" || !looksLikeFingerprint(cfg.CertFPHex) {
		return fmt.Errorf("NEW=1 requires SERVER_URL and CERT_FP_HEX from the current env file")
	}

	agentID, secretHex, label, err := allocateNewAgent(cfg.OutputDir, cfg.Label)
	if err != nil {
		return err
	}

	envPath, err := writeAgentEnv(cfg.OutputDir, label, buildAgentEnv(agentID, secretHex, label, cfg))
	if err != nil {
		return err
	}

	if err := registerAgent(client, token, agentID, secretHex); err != nil {
		_ = os.Remove(envPath)
		return err
	}

	envPathDisplay := filepath.ToSlash(envPath)
	fmt.Printf("[+] Registered new agent: %s (id: %s)\n", label, agentID)
	fmt.Printf("    env file: %s\n", envPathDisplay)
	fmt.Printf("    build linux:   make build-agent-linux   AGENT_ENV=%s\n", envPathDisplay)
	fmt.Printf("    build windows: make build-agent-windows AGENT_ENV=%s\n", envPathDisplay)
	return nil
}

// allocateNewAgent picks a UUID and label that do not collide with existing
// entries. A non-empty requestedLabel is validated and used verbatim; an empty
// requestedLabel triggers auto-generation from the UUID's first 8 hex chars
// with collision retry.
func allocateNewAgent(outputDir, requestedLabel string) (string, string, string, error) {
	if requestedLabel != "" {
		if err := agentlabel.Validate(requestedLabel); err != nil {
			return "", "", "", err
		}
		if exists, _ := labelExists(outputDir, requestedLabel); exists {
			return "", "", "", fmt.Errorf("label %q is already in use", requestedLabel)
		}
		id, secret, err := generateAgentCredentials()
		if err != nil {
			return "", "", "", err
		}
		return id, secret, requestedLabel, nil
	}

	for i := 0; i < 5; i++ {
		id, secret, err := generateAgentCredentials()
		if err != nil {
			return "", "", "", err
		}
		label := agentlabel.FromUUIDPrefix(id)
		if err := agentlabel.Validate(label); err != nil {
			continue
		}
		if exists, _ := labelExists(outputDir, label); !exists {
			return id, secret, label, nil
		}
	}
	return "", "", "", fmt.Errorf("failed to allocate a unique label after retries")
}

// labelExists reports whether an agent with the given label already has an env
// file under outputDir or a build directory under builds/.
func labelExists(outputDir, label string) (bool, error) {
	if _, err := os.Stat(filepath.Join(outputDir, label+".env")); err == nil {
		return true, nil
	}
	if _, err := os.Stat(filepath.Join("builds", label)); err == nil {
		return true, nil
	}
	return false, nil
}

func containsAgentID(agents []agentSummary, agentID string) bool {
	for _, agent := range agents {
		if agent.ID == agentID {
			return true
		}
	}
	return false
}

func generateAgentCredentials() (string, string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("generate secret: %w", err)
	}
	return uuid.New().String(), hex.EncodeToString(secret), nil
}

func buildAgentEnv(agentID, secretHex, label string, cfg *registerConfig) []byte {
	lines := []string{
		"AGENT_ID=" + agentID,
		"AGENT_SECRET_HEX=" + secretHex,
		"CERT_FP_HEX=" + cfg.CertFPHex,
		"SERVER_URL=" + cfg.ServerURL,
		"AGENT_LABEL=" + label,
	}
	if strings.TrimSpace(cfg.SleepSeconds) != "" {
		lines = append(lines, "SLEEP_SECONDS="+cfg.SleepSeconds)
	}
	if strings.TrimSpace(cfg.DNSDomain) != "" {
		lines = append(lines, "DNS_DOMAIN="+cfg.DNSDomain)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func writeAgentEnv(outputDir, label string, contents []byte) (string, error) {
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		return "", fmt.Errorf("create agent env directory: %w", err)
	}

	path := filepath.Join(outputDir, label+".env")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create agent env file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(contents); err != nil {
		return "", fmt.Errorf("write agent env file: %w", err)
	}
	return path, nil
}

func looksLikeFingerprint(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

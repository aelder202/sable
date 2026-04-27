package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aelder202/sable/internal/agentlabel"
	"github.com/aelder202/sable/internal/listener"
	"github.com/google/uuid"
)

func main() {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	var labelFlag string
	var profileFlag string
	var dnsDomainFlag string
	fs.StringVar(&labelFlag, "label", "", "human-readable label for this agent (1-31 lowercase alphanumeric/-/_)")
	fs.StringVar(&profileFlag, "profile", "", "agent build profile: default, fast, quiet, dns")
	fs.StringVar(&dnsDomainFlag, "dns-domain", "", "DNS fallback domain for dns profile")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	serverURL := os.Getenv("SERVER_URL")
	if serverURL == "" && fs.NArg() > 0 {
		serverURL = fs.Arg(0)
	}
	if serverURL == "" {
		fmt.Fprintln(os.Stderr, "usage: make setup SERVER_URL=https://<public-server-ip>:443 [LABEL=<label>]")
		os.Exit(1)
	}

	label, err := resolveLabel(labelFlag, os.Getenv("LABEL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] %v\n", err)
		os.Exit(1)
	}
	profile, err := resolveBuildProfile(profileFlag, os.Getenv("AGENT_PROFILE"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[!] %v\n", err)
		os.Exit(1)
	}
	dnsDomain := strings.TrimSpace(dnsDomainFlag)
	if dnsDomain == "" {
		dnsDomain = strings.TrimSpace(os.Getenv("DNS_DOMAIN"))
	}
	if profile.Name == "dns" && dnsDomain == "" {
		fmt.Fprintln(os.Stderr, "[!] dns profile requires DNS_DOMAIN or --dns-domain")
		os.Exit(1)
	}

	if _, err := os.Stat("config.env"); err == nil {
		fmt.Println("[!] config.env already exists. Delete it and run 'make setup' again to regenerate.")
		os.Exit(0)
	}

	agentID := uuid.New().String()

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		log.Fatalf("generate secret: %v", err)
	}
	secretHex := hex.EncodeToString(secret)

	_, fp, err := listener.LoadOrCreateCert("server.crt", "server.key")
	if err != nil {
		log.Fatalf("generate cert: %v", err)
	}

	env := buildConfigEnv(agentID, secretHex, fp, serverURL, label, profile, dnsDomain)
	if err := os.WriteFile("config.env", env, 0600); err != nil {
		log.Fatalf("write config.env: %v", err)
	}

	fmt.Printf("[+] Setup complete! (label: %s, profile: %s)\n", label, profile.Name)
	fmt.Println("    config.env  - agent ID, secret, cert fingerprint, server URL, label (keep secret)")
	fmt.Println("    server.crt  - TLS certificate (deploy alongside sable-server)")
	fmt.Println("    server.key  - TLS private key  (deploy alongside sable-server)")
	fmt.Println()
	fmt.Println("[*] Next: make build")
}

type buildProfile struct {
	Name         string
	SleepSeconds string
}

func resolveBuildProfile(flagValue, envValue string) (buildProfile, error) {
	name := strings.TrimSpace(flagValue)
	if name == "" {
		name = strings.TrimSpace(envValue)
	}
	if name == "" {
		name = "default"
	}
	switch name {
	case "default":
		return buildProfile{Name: name, SleepSeconds: "30"}, nil
	case "fast":
		return buildProfile{Name: name, SleepSeconds: "5"}, nil
	case "quiet":
		return buildProfile{Name: name, SleepSeconds: "120"}, nil
	case "dns":
		return buildProfile{Name: name, SleepSeconds: "60"}, nil
	default:
		return buildProfile{}, fmt.Errorf("unknown profile %q", name)
	}
}

// resolveLabel picks the first non-empty source (flag, env), defaults to "main",
// then validates via agentlabel.
func resolveLabel(flagValue, envValue string) (string, error) {
	label := strings.TrimSpace(flagValue)
	if label == "" {
		label = strings.TrimSpace(envValue)
	}
	if label == "" {
		label = "main"
	}
	if err := agentlabel.Validate(label); err != nil {
		return "", err
	}
	return label, nil
}

// buildConfigEnv produces the body of config.env for the first agent.
func buildConfigEnv(agentID, secretHex, fp, serverURL, label string, profile buildProfile, dnsDomain string) []byte {
	lines := []string{
		"AGENT_ID=" + agentID,
		"AGENT_SECRET_HEX=" + secretHex,
		"CERT_FP_HEX=" + fp,
		"SERVER_URL=" + serverURL,
		"AGENT_LABEL=" + label,
		"AGENT_PROFILE=" + profile.Name,
		"SLEEP_SECONDS=" + profile.SleepSeconds,
	}
	if dnsDomain != "" {
		lines = append(lines, "DNS_DOMAIN="+dnsDomain)
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

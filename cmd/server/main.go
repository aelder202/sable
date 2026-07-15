package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aelder202/sable/internal/api"
	"github.com/aelder202/sable/internal/cli"
	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/operatorpw"
	"github.com/aelder202/sable/internal/session"
	webui "github.com/aelder202/sable/web"
	mdns "github.com/miekg/dns"
)

func main() {
	cliMode := flag.Bool("cli", false, "start interactive operator CLI instead of server")
	apiURL := flag.String("api", "https://127.0.0.1:8443", "operator API URL (for --cli mode)")
	passwordFile := flag.String("password-file", "", "read operator password from file")
	dnsDomain := flag.String("dns-domain", defaultDNSDomain(), "enable DNS fallback listener for this authoritative domain")
	debugAddr := flag.String("debug-addr", "", "optional loopback debug/pprof address, for example 127.0.0.1:6060")
	stateFile := flag.String("state-file", defaultStateFile(), "operator state JSON file; use 'none' or 'off' to disable persistence")
	stateKeyFile := flag.String("state-key-file", defaultStateKeyFile(), "optional file containing a 32-byte or 64-hex-character state encryption key")
	flag.Parse()

	password, err := loadOperatorPassword(*passwordFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] password error: %v\n", err)
		os.Exit(1)
	}

	if *cliMode {
		token := loginCLI(*apiURL, password)
		c, err := cli.New(*apiURL, token)
		if err != nil {
			log.Fatal(err)
		}
		c.Run()
		return
	}

	// Load or generate the TLS certificate. Persisting it keeps the fingerprint
	// stable across restarts so agents don't need to be rebuilt.
	cert, fingerprint, err := listener.LoadOrCreateCert("server.crt", "server.key")
	if err != nil {
		log.Fatalf("cert error: %v", err)
	}
	fmt.Printf("[*] TLS cert fingerprint (SHA-256): %s\n", fingerprint)
	fmt.Printf("[*] Build agents with: make build-agent-linux CERT_FP_HEX=%s\n", fingerprint)
	if expiry, expiryErr := listener.CertificateExpiry(cert); expiryErr == nil {
		remaining := time.Until(expiry)
		switch {
		case remaining <= 0:
			log.Printf("[!] TLS certificate expired at %s; rotate it and rebuild agents", expiry.Format(time.RFC3339))
		case remaining <= 30*24*time.Hour:
			log.Printf("[!] TLS certificate expires in %s at %s", remaining.Round(time.Hour), expiry.Format(time.RFC3339))
		default:
			log.Printf("[*] TLS certificate expires at %s", expiry.Format(time.RFC3339))
		}
	}

	statePath := normalizeStateFile(*stateFile)
	stateKey, err := loadStateKey(*stateKeyFile)
	if err != nil {
		log.Fatalf("state key error: %v", err)
	}
	store, err := session.NewPersistentStoreWithKey(statePath, stateKey)
	if err != nil {
		log.Fatalf("state error: %v", err)
	}
	if statePath != "" {
		log.Printf("[*] Operator state persistence: %s", statePath)
		if len(stateKey) > 0 {
			log.Printf("[*] Operator state encryption enabled")
		}
	}
	nc := nonce.NewCache(5 * time.Minute)

	agentTLSCfg := listener.NewTLSConfig(cert)
	apiTLSCfg := listener.NewTLSConfig(cert)

	// Agent-facing HTTPS listener on :443
	beaconMux := http.NewServeMux()
	beaconMux.Handle("/cdn/static/update", listener.NewHTTPSHandler(store, nc))
	agentSrv := &http.Server{
		Addr:              ":443",
		Handler:           beaconMux,
		TLSConfig:         agentTLSCfg,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       30 * time.Second,
	}

	// Operator-facing API on 127.0.0.1:8443 over TLS (loopback-only).
	// Binding to loopback prevents off-host exposure; TLS protects the JWT and
	// operator password even on the local machine's network interfaces.
	apiLn, err := tls.Listen("tcp", "127.0.0.1:8443", apiTLSCfg)
	if err != nil {
		log.Fatalf("operator API listen failed: %v", err)
	}
	jwtSecret := generateRandom(32)
	apiCfg := &api.Config{
		OperatorPasswordHash: api.HashPassword(password),
		JWTSecret:            jwtSecret,
	}
	fullMux := http.NewServeMux()
	fullMux.Handle("/api/", api.NewRouter(store, apiCfg))
	fullMux.Handle("/", serveWebUI())
	apiSrv := &http.Server{
		Handler:           api.WithSecurityHeaders(fullMux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	errCh := make(chan error, 3)
	go func() { errCh <- apiSrv.Serve(apiLn) }()
	startDebugServer(*debugAddr)

	var dnsSrv *mdns.Server
	if domain := normalizeDNSDomain(*dnsDomain); domain != "" {
		dnsSrv = &mdns.Server{
			Addr:    ":53",
			Net:     "udp",
			Handler: listener.NewDNSHandler(store, nc, domain),
		}
		go func() { errCh <- dnsSrv.ListenAndServe() }()
		log.Printf("[*] Agent DNS listener on :53 for %s", domain)
	}

	log.Printf("[*] Operator API on https://127.0.0.1:8443 | Agent HTTPS listener on :443")
	go func() { errCh <- agentSrv.ListenAndServeTLS("", "") }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-signals:
		log.Printf("[*] Shutting down after %s", sig)
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("server stopped: %v", serveErr)
		}
	}
	signal.Stop(signals)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = agentSrv.Shutdown(ctx)
	_ = apiSrv.Shutdown(ctx)
	if dnsSrv != nil {
		_ = dnsSrv.Shutdown()
	}
	if err := store.Close(); err != nil {
		log.Printf("state flush failed: %v", err)
	}
}

// loginCLI authenticates to the operator API and returns the JWT.
// InsecureSkipVerify is only allowed for loopback API URLs.
func loginCLI(apiURL, password string) string {
	if err := requireLoopbackAPIURL(apiURL); err != nil {
		log.Fatal(err)
	}
	client := &http.Client{ //nolint:gosec
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := client.Post(apiURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("login failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		log.Fatalf("login rejected: %s", data)
	}
	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	if result["token"] == "" {
		log.Fatal("login failed: no token returned")
	}
	return result["token"]
}

func loadOperatorPassword(passwordFile string) (string, error) {
	if password := strings.TrimSpace(os.Getenv("SABLE_OPERATOR_PASSWORD")); password != "" {
		return password, nil
	}
	if password := strings.TrimSpace(os.Getenv("C2_OPERATOR_PASSWORD")); password != "" {
		return password, nil
	}
	if passwordFile != "" {
		data, err := os.ReadFile(passwordFile)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		password := operatorpw.Normalize(data)
		if password == "" {
			return "", errors.New("password file is empty")
		}
		return password, nil
	}

	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := operatorpw.Normalize(data)
		if password == "" {
			return "", errors.New("stdin password is empty")
		}
		return password, nil
	}

	return "", errors.New("supply the operator password via SABLE_OPERATOR_PASSWORD, --password-file, or stdin")
}

func defaultDNSDomain() string {
	if domain := strings.TrimSpace(os.Getenv("SABLE_DNS_DOMAIN")); domain != "" {
		return domain
	}
	return strings.TrimSpace(os.Getenv("DNS_DOMAIN"))
}

func defaultStateFile() string {
	if path := strings.TrimSpace(os.Getenv("SABLE_STATE_FILE")); path != "" {
		return path
	}
	return "sable-state.json"
}

func defaultStateKeyFile() string {
	return strings.TrimSpace(os.Getenv("SABLE_STATE_KEY_FILE"))
}

func loadStateKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state key file: %w", err)
	}
	if len(data) == 32 {
		return data, nil
	}
	text := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(text)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("state key file must contain exactly 32 raw bytes or 64 hexadecimal characters")
	}
	return decoded, nil
}

func normalizeStateFile(path string) string {
	path = strings.TrimSpace(path)
	switch strings.ToLower(path) {
	case "", "none", "off", "disabled":
		return ""
	default:
		return path
	}
}

func normalizeDNSDomain(domain string) string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" {
		return ""
	}
	return domain + "."
}

func startDebugServer(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		log.Fatalf("invalid debug address %q: %v", addr, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Fatalf("debug address must be loopback-only, got %q", host)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		log.Printf("[*] Debug endpoint on http://%s/debug/pprof/ (loopback only)", addr)
		log.Fatal(http.ListenAndServe(addr, mux))
	}()
}

func requireLoopbackAPIURL(apiURL string) error {
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil {
		return fmt.Errorf("invalid API URL: %w", err)
	}
	host := u.Hostname()
	if u.Scheme != "https" || host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("API URL must be an HTTPS loopback origin without credentials, path, query, or fragment")
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	switch strings.ToLower(host) {
	case "localhost":
		return nil
	default:
		return fmt.Errorf("refusing insecure CLI TLS for non-loopback API host %q", host)
	}
}

func generateRandom(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate random bytes: %v", err)
	}
	return b
}

func serveWebUI() http.Handler {
	sub, err := fs.Sub(webui.FS, ".")
	if err != nil {
		log.Fatalf("web UI embed error: %v", err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	})
}

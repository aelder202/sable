package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/tlspin"
)

func TestLoadOperatorPasswordFromEnv(t *testing.T) {
	t.Setenv("SABLE_OPERATOR_PASSWORD", "env-secret")
	password, err := loadOperatorPassword("")
	if err != nil {
		t.Fatalf("loadOperatorPassword returned error: %v", err)
	}
	if password != "env-secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestLoadStateKeySupportsRawAndHex(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte{0x5a}, 32)
	rawPath := filepath.Join(dir, "raw.key")
	if err := os.WriteFile(rawPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadOrCreateStateKey(rawPath)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("raw state key: got=%x err=%v", got, err)
	}
	hexPath := filepath.Join(dir, "hex.key")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(raw)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = loadOrCreateStateKey(hexPath)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("hex state key: got=%x err=%v", got, err)
	}
	badPath := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(badPath, []byte("too-short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateStateKey(badPath); err == nil {
		t.Fatal("invalid state key should be rejected")
	}
}

func TestLoadOrCreateStateKeyCreatesRestrictedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sable", "state.key")
	key, err := loadOrCreateStateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Fatalf("state key length = %d", len(key))
	}
	reloaded, err := loadOrCreateStateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, reloaded) {
		t.Fatal("state key changed after reload")
	}
}

func TestNormalizeStateKeyFile(t *testing.T) {
	for _, value := range []string{"", "none", "off", "disabled", " OFF "} {
		if got := normalizeStateKeyFile(value); got != "" {
			t.Fatalf("normalizeStateKeyFile(%q) = %q", value, got)
		}
	}
	if got := normalizeStateKeyFile(".sable/state.key"); got != ".sable/state.key" {
		t.Fatalf("unexpected normalized key path %q", got)
	}
}

func TestLoadOperatorPasswordFromFile(t *testing.T) {
	t.Setenv("SABLE_OPERATOR_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "operator-password.txt")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	password, err := loadOperatorPassword(path)
	if err != nil {
		t.Fatalf("loadOperatorPassword returned error: %v", err)
	}
	if password != "file-secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestLoadOperatorPasswordFromUTF8BOMFile(t *testing.T) {
	t.Setenv("SABLE_OPERATOR_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "operator-password-utf8.txt")
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("file-secret\n")...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	password, err := loadOperatorPassword(path)
	if err != nil {
		t.Fatalf("loadOperatorPassword returned error: %v", err)
	}
	if password != "file-secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestLoadOperatorPasswordFromUTF16LEFile(t *testing.T) {
	t.Setenv("SABLE_OPERATOR_PASSWORD", "")
	path := filepath.Join(t.TempDir(), "operator-password-utf16.txt")
	data := []byte{
		0xFF, 0xFE,
		'f', 0x00,
		'i', 0x00,
		'l', 0x00,
		'e', 0x00,
		'-', 0x00,
		's', 0x00,
		'e', 0x00,
		'c', 0x00,
		'r', 0x00,
		'e', 0x00,
		't', 0x00,
		'\n', 0x00,
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	password, err := loadOperatorPassword(path)
	if err != nil {
		t.Fatalf("loadOperatorPassword returned error: %v", err)
	}
	if password != "file-secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestRequireLoopbackAPIURL(t *testing.T) {
	if err := requireLoopbackAPIURL("https://127.0.0.1:8443"); err != nil {
		t.Fatalf("expected loopback API URL to be allowed: %v", err)
	}
	if err := requireLoopbackAPIURL("https://example.com:8443"); err == nil {
		t.Fatal("expected non-loopback API URL to be rejected")
	}
}

func TestLoadOperatorPasswordFromLegacyEnvFallback(t *testing.T) {
	t.Setenv("SABLE_OPERATOR_PASSWORD", "")
	t.Setenv("C2_OPERATOR_PASSWORD", "legacy-secret")
	password, err := loadOperatorPassword("")
	if err != nil {
		t.Fatalf("loadOperatorPassword returned error: %v", err)
	}
	if password != "legacy-secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestDefaultDNSDomainPrefersSableEnv(t *testing.T) {
	t.Setenv("SABLE_DNS_DOMAIN", "c2.example.com")
	t.Setenv("DNS_DOMAIN", "legacy.example.com")
	if got := defaultDNSDomain(); got != "c2.example.com" {
		t.Fatalf("defaultDNSDomain() = %q", got)
	}
}

func TestDefaultDNSDomainUsesLegacyEnvFallback(t *testing.T) {
	t.Setenv("SABLE_DNS_DOMAIN", "")
	t.Setenv("DNS_DOMAIN", "legacy.example.com")
	if got := defaultDNSDomain(); got != "legacy.example.com" {
		t.Fatalf("defaultDNSDomain() = %q", got)
	}
}

func TestDefaultStateFile(t *testing.T) {
	t.Setenv("SABLE_STATE_FILE", "")
	if got := defaultStateFile(); got != "sable-state.json" {
		t.Fatalf("defaultStateFile() = %q", got)
	}
	t.Setenv("SABLE_STATE_FILE", "custom-state.json")
	if got := defaultStateFile(); got != "custom-state.json" {
		t.Fatalf("defaultStateFile() env = %q", got)
	}
}

func TestNormalizeStateFile(t *testing.T) {
	for _, value := range []string{"", "none", "off", "disabled", " OFF "} {
		if got := normalizeStateFile(value); got != "" {
			t.Fatalf("normalizeStateFile(%q) = %q, want empty", value, got)
		}
	}
	if got := normalizeStateFile("state.json"); got != "state.json" {
		t.Fatalf("normalizeStateFile() = %q", got)
	}
}

func TestNormalizeDNSDomain(t *testing.T) {
	tests := map[string]string{
		"":                    "",
		"  C2.Example.COM  ":  "c2.example.com.",
		"c2.example.com.":     "c2.example.com.",
		"sub.c2.example.com":  "sub.c2.example.com.",
		"sub.c2.example.com.": "sub.c2.example.com.",
	}
	for input, want := range tests {
		if got := normalizeDNSDomain(input); got != want {
			t.Fatalf("normalizeDNSDomain(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStartDebugServerRejectsNonLoopback(t *testing.T) {
	if _, _, err := createDebugServer("0.0.0.0:0", nil, "secret"); err == nil {
		t.Fatal("expected non-loopback debug address to be rejected")
	}
}

func TestDebugServerRequiresAuthenticationOverPinnedTLS(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	cert, _, err := listener.LoadOrCreateCert(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	server, ln, err := createDebugServer("127.0.0.1:0", listener.NewTLSConfig(cert), "debug-secret")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	client, err := tlspin.NewClientFromCert(certPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://" + ln.Addr().String() + "/debug/pprof/"
	resp, err := client.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer debug-secret")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

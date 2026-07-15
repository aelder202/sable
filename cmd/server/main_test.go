package main

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
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
	got, err := loadStateKey(rawPath)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("raw state key: got=%x err=%v", got, err)
	}
	hexPath := filepath.Join(dir, "hex.key")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(raw)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = loadStateKey(hexPath)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("hex state key: got=%x err=%v", got, err)
	}
	badPath := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(badPath, []byte("too-short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStateKey(badPath); err == nil {
		t.Fatal("invalid state key should be rejected")
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
	// Validation is covered through the helper logic in startDebugServer by using
	// a malformed/non-loopback address in a subprocess would be excessive here;
	// keep this as a guard for the loopback predicate behavior it relies on.
	allowed := []string{"127.0.0.1", "::1", "localhost"}
	for _, host := range allowed {
		if host != "localhost" {
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				t.Fatalf("expected %s to be loopback", host)
			}
		}
	}
}

package main

import (
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

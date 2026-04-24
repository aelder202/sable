package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewRejectsNonLoopbackAPI(t *testing.T) {
	if _, err := New("https://example.com:8443", "token"); err == nil {
		t.Fatal("expected non-loopback API URL to be rejected")
	}
}

func TestNewAllowsLoopbackAPI(t *testing.T) {
	if _, err := New("https://127.0.0.1:8443", "token"); err != nil {
		t.Fatalf("expected loopback API URL to be allowed: %v", err)
	}
}

func TestBuildUploadPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	payload, err := buildUploadPayload(path, "/tmp/sample.bin")
	if err != nil {
		t.Fatalf("buildUploadPayload returned error: %v", err)
	}

	parts := strings.SplitN(payload, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected payload format %q", payload)
	}
	if parts[0] != "/tmp/sample.bin" {
		t.Fatalf("unexpected remote path %q", parts[0])
	}

	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("unexpected decoded content %q", decoded)
	}
}

func TestBuildUploadPayloadRejectsOversizedPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	data := make([]byte, maxTaskPayloadBytes)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := buildUploadPayload(path, "/tmp/large.bin"); err == nil {
		t.Fatal("expected oversized upload payload to be rejected")
	}
}

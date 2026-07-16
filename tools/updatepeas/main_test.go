package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateAssetVerifiesDigestBeforeWriting(t *testing.T) {
	body := []byte("verified tool body")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(body) //nolint:errcheck
	}))
	defer server.Close()
	digest := sha256.Sum256(body)
	path := filepath.Join(t.TempDir(), "peas", "tool.sh")

	err := updateAsset(server.Client(), peasAsset{
		name: "test", url: server.URL, path: path, sha256: fmt.Sprintf("%x", digest),
	})
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(body) {
		t.Fatalf("written body = %q", written)
	}
}

func TestUpdateAssetRejectsDigestMismatchWithoutWriting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("tampered")) //nolint:errcheck
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "tool.bat")

	err := updateAsset(server.Client(), peasAsset{
		name: "test", url: server.URL, path: path, sha256: fmt.Sprintf("%064x", 1),
	})
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("mismatched asset was written: %v", statErr)
	}
}

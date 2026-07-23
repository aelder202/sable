package tlspin

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pinnedTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestPinnedClientAcceptsExpectedCertificate(t *testing.T) {
	srv := pinnedTestServer(t)
	sum := sha256.Sum256(srv.Certificate().Raw)
	client, err := NewClient(sum[:], 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("pinned request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestPinnedClientRejectsDifferentCertificate(t *testing.T) {
	srv := pinnedTestServer(t)
	wrong := sha256.Sum256([]byte("different certificate"))
	client, err := NewClient(wrong[:], 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(srv.URL); err == nil {
		t.Fatal("expected certificate pin mismatch")
	}
}

func TestLoadFingerprint(t *testing.T) {
	srv := pinnedTestServer(t)
	path := filepath.Join(t.TempDir(), "server.crt")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(srv.Certificate().Raw)
	if string(got) != string(want[:]) {
		t.Fatal("fingerprint mismatch")
	}
}

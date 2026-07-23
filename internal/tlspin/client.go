// Package tlspin provides certificate-pinned HTTPS clients for Sable's local
// control plane. The operator API uses a self-signed certificate, so pinning
// the exact leaf certificate replaces public-CA validation without trusting an
// arbitrary process that happens to bind a loopback port.
package tlspin

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const FingerprintSize = sha256.Size

// LoadFingerprint reads a PEM certificate and returns the SHA-256 fingerprint
// of its first certificate.
func LoadFingerprint(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pinned certificate: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("pinned certificate file does not contain a certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("parse pinned certificate: %w", err)
	}
	sum := sha256.Sum256(block.Bytes)
	return append([]byte(nil), sum[:]...), nil
}

// NewClient returns a TLS 1.3 HTTPS client that accepts only the expected leaf
// certificate fingerprint. Redirects are rejected so credentials and bearer
// tokens cannot be forwarded away from the pinned origin.
func NewClient(expectedFingerprint []byte, timeout time.Duration) (*http.Client, error) {
	if len(expectedFingerprint) != FingerprintSize {
		return nil, fmt.Errorf("certificate fingerprint must be %d bytes", FingerprintSize)
	}
	expected := append([]byte(nil), expectedFingerprint...)
	transport := &http.Transport{
		MaxIdleConns:        2,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true, // verified by the exact leaf pin below
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("server presented no TLS certificate")
				}
				actual := sha256.Sum256(state.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
					return errors.New("server TLS certificate fingerprint mismatch")
				}
				return nil
			},
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// NewClientFromCert loads a certificate pin from certPath and constructs a
// pinned client.
func NewClientFromCert(certPath string, timeout time.Duration) (*http.Client, error) {
	fingerprint, err := LoadFingerprint(certPath)
	if err != nil {
		return nil, err
	}
	return NewClient(fingerprint, timeout)
}

package listener

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"time"
)

// GenerateSelfSignedCert generates an ECDSA P-256 TLS certificate valid for 1 year.
// Returns the tls.Certificate, its SHA-256 fingerprint as a hex string, and any error.
// Use LoadOrCreateCert when persistence across server restarts is needed.
func GenerateSelfSignedCert() (tls.Certificate, string, error) {
	cert, fp, _, _, err := generateCertPEM()
	return cert, fp, err
}

// LoadOrCreateCert loads a persisted TLS certificate from certPath/keyPath, or
// generates a new one and writes it to those paths if they don't exist.
// Persisting the cert keeps the fingerprint stable across server restarts, so
// agents baked with that fingerprint don't need to be rebuilt after every restart.
func LoadOrCreateCert(certPath, keyPath string) (tls.Certificate, string, error) {
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		if len(cert.Certificate) == 0 {
			return tls.Certificate{}, "", errors.New("empty certificate chain")
		}
		fp := sha256.Sum256(cert.Certificate[0])
		return cert, hex.EncodeToString(fp[:]), nil
	}

	// Generate a new cert and persist it.
	cert, fp, certPEM, keyPEM, err := generateCertPEM()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, fp, nil
}

// generateCertPEM creates a fresh ECDSA P-256 cert and returns the tls.Certificate,
// SHA-256 fingerprint hex, and the PEM-encoded bytes for both cert and key.
func generateCertPEM() (tls.Certificate, string, []byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "c2"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", nil, nil, err
	}
	fp := sha256.Sum256(certDER)
	return tlsCert, hex.EncodeToString(fp[:]), certPEM, keyPEM, nil
}

// NewTLSConfig returns a TLS 1.3-minimum config using the provided certificate.
// TLS 1.2 and below are explicitly rejected.
func NewTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}

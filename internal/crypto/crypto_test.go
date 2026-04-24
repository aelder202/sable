package crypto_test

import (
	"bytes"
	"testing"

	"github.com/aelder202/sable/internal/crypto"
)

func TestDeriveKey(t *testing.T) {
	secret := []byte("32-byte-test-secret-padding-here")
	k1, err := crypto.DeriveKey(secret, "beacon")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := crypto.DeriveKey(secret, "beacon")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("DeriveKey not deterministic")
	}
	k3, _ := crypto.DeriveKey(secret, "response")
	if bytes.Equal(k1, k3) {
		t.Fatal("different info must produce different keys")
	}
	if len(k1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(k1))
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key, _ := crypto.DeriveKey([]byte("secret"), "test")
	plaintext := []byte("hello world")

	ct, err := crypto.Encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}

	got, err := crypto.Decrypt(key, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt mismatch: got %q want %q", got, plaintext)
	}
}

func TestEncryptProducesUniqueNonces(t *testing.T) {
	key, _ := crypto.DeriveKey([]byte("secret"), "test")
	ct1, _ := crypto.Encrypt(key, []byte("same plaintext"))
	ct2, _ := crypto.Encrypt(key, []byte("same plaintext"))
	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of same plaintext must differ (unique nonces)")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	key, _ := crypto.DeriveKey([]byte("secret"), "test")
	ct, _ := crypto.Encrypt(key, []byte("data"))
	ct[len(ct)-1] ^= 0xff
	_, err := crypto.Decrypt(key, ct)
	if err == nil {
		t.Fatal("expected error on tampered ciphertext")
	}
}

func TestSignVerify(t *testing.T) {
	secret := []byte("hmac-secret")
	data := []byte("message")
	sig := crypto.Sign(secret, data)
	if !crypto.Verify(secret, data, sig) {
		t.Fatal("Verify failed on valid sig")
	}
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[0] ^= 0xff
	if crypto.Verify(secret, data, tampered) {
		t.Fatal("Verify must fail on tampered sig")
	}
}

func TestRandomBytes(t *testing.T) {
	b1, err := crypto.RandomBytes(16)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := crypto.RandomBytes(16)
	if bytes.Equal(b1, b2) {
		t.Fatal("RandomBytes must be unique")
	}
	if len(b1) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(b1))
	}
}

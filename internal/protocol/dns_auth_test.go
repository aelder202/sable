package protocol_test

import (
	"testing"

	"github.com/aelder202/sable/internal/protocol"
)

func TestDNSChunkAuthTagBindsChunkMetadata(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tag := protocol.DNSChunkAuthTag(secret, "0000000000000000", "agent-1", 1, 3, []byte("payload"))
	if !protocol.VerifyDNSChunkAuthTag(secret, tag, "0000000000000000", "agent-1", 1, 3, []byte("payload")) {
		t.Fatal("valid DNS chunk tag was rejected")
	}
	if protocol.VerifyDNSChunkAuthTag(secret, tag, "0000000000000000", "agent-1", 2, 3, []byte("payload")) {
		t.Fatal("tag did not bind the chunk index")
	}
	if protocol.VerifyDNSChunkAuthTag(secret, tag, "0000000000000000", "agent-1", 1, 3, []byte("tampered")) {
		t.Fatal("tag did not bind the chunk body")
	}
}

func TestDNSResponseAuthTagBindsIndex(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tag := protocol.DNSResponseAuthTag(secret, "0000000000000000", "agent-1", 4)
	if !protocol.VerifyDNSResponseAuthTag(secret, tag, "0000000000000000", "agent-1", 4) {
		t.Fatal("valid DNS response tag was rejected")
	}
	if protocol.VerifyDNSResponseAuthTag(secret, tag, "0000000000000000", "agent-1", 5) {
		t.Fatal("response tag did not bind the index")
	}
}

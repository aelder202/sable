package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
)

const dnsAuthTagBytes = 8

// DNSChunkAuthTag authenticates one DNS beacon chunk before the listener
// allocates any reassembly state for it.
func DNSChunkAuthTag(secret []byte, sessionID, agentID string, index, total int, chunk []byte) string {
	return dnsAuthTag(secret, "chunk", sessionID, agentID, index, total, chunk)
}

// VerifyDNSChunkAuthTag verifies a chunk tag in constant time.
func VerifyDNSChunkAuthTag(secret []byte, tag, sessionID, agentID string, index, total int, chunk []byte) bool {
	expected, err := hex.DecodeString(DNSChunkAuthTag(secret, sessionID, agentID, index, total, chunk))
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(tag)
	return err == nil && len(provided) == dnsAuthTagBytes && hmac.Equal(provided, expected)
}

// DNSResponseAuthTag authenticates a DNS response-chunk retrieval query.
func DNSResponseAuthTag(secret []byte, sessionID, agentID string, index int) string {
	return dnsAuthTag(secret, "response", sessionID, agentID, index, 0, nil)
}

// VerifyDNSResponseAuthTag verifies a response retrieval tag in constant time.
func VerifyDNSResponseAuthTag(secret []byte, tag, sessionID, agentID string, index int) bool {
	expected, err := hex.DecodeString(DNSResponseAuthTag(secret, sessionID, agentID, index))
	if err != nil {
		return false
	}
	provided, err := hex.DecodeString(tag)
	return err == nil && len(provided) == dnsAuthTagBytes && hmac.Equal(provided, expected)
}

func dnsAuthTag(secret []byte, purpose, sessionID, agentID string, index, total int, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("sable-dns-" + purpose + "-v1\x00"))
	writeDNSAuthField(mac, []byte(strings.ToLower(sessionID)))
	writeDNSAuthField(mac, []byte(strings.ToLower(agentID)))
	var number [8]byte
	binary.BigEndian.PutUint32(number[:4], uint32(index))
	binary.BigEndian.PutUint32(number[4:], uint32(total))
	mac.Write(number[:])
	writeDNSAuthField(mac, body)
	return hex.EncodeToString(mac.Sum(nil)[:dnsAuthTagBytes])
}

type dnsAuthWriter interface {
	Write([]byte) (int, error)
}

func writeDNSAuthField(w dnsAuthWriter, value []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	w.Write(size[:]) //nolint:errcheck // hash.Hash writes cannot fail
	w.Write(value)   //nolint:errcheck // hash.Hash writes cannot fail
}

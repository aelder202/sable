package nonce_test

import (
	"testing"
	"time"

	"github.com/aelder202/sable/internal/nonce"
)

func TestSeenReturnsFalseForNew(t *testing.T) {
	c := nonce.NewCache(5 * time.Minute)
	if c.Seen([]byte("brand-new-nonce")) {
		t.Fatal("fresh nonce must not be seen")
	}
}

func TestSeenReturnsTrueAfterAdd(t *testing.T) {
	c := nonce.NewCache(5 * time.Minute)
	n := []byte("replay-nonce-value")
	c.Add(n)
	if !c.Seen(n) {
		t.Fatal("nonce must be seen after Add")
	}
}

func TestReplayRejected(t *testing.T) {
	c := nonce.NewCache(5 * time.Minute)
	n := []byte("nonce-used-once")
	if c.Seen(n) {
		t.Fatal("should not be seen before Add")
	}
	c.Add(n)
	if !c.Seen(n) {
		t.Fatal("should be seen after Add")
	}
}

func TestExpiredNonceAllowedAgain(t *testing.T) {
	c := nonce.NewCache(50 * time.Millisecond)
	n := []byte("short-lived-nonce")
	c.Add(n)
	if !c.Seen(n) {
		t.Fatal("nonce must be seen immediately after Add")
	}
	time.Sleep(100 * time.Millisecond)
	if c.Seen(n) {
		t.Fatal("expired nonce must not be seen")
	}
}

func TestSeenOrAddAtomicRejection(t *testing.T) {
	c := nonce.NewCache(5 * time.Minute)
	n := []byte("atomic-nonce")
	// First call: not seen → records and returns false.
	if c.SeenOrAdd(n) {
		t.Fatal("first SeenOrAdd must return false (new nonce)")
	}
	// Second call: already recorded → returns true.
	if !c.SeenOrAdd(n) {
		t.Fatal("second SeenOrAdd must return true (replay)")
	}
}

func TestDifferentNoncesAreIndependent(t *testing.T) {
	c := nonce.NewCache(5 * time.Minute)
	n1 := []byte("nonce-one")
	n2 := []byte("nonce-two")
	c.Add(n1)
	if c.Seen(n2) {
		t.Fatal("adding n1 must not affect n2")
	}
}

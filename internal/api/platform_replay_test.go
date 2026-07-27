package api

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

func TestPlatformReplayCacheRejectsDuplicateNoncePerPrincipal(t *testing.T) {
	cache := newPlatformReplayCache()
	now := time.Unix(1000, 0)
	expiresAt := now.Add(time.Minute)
	var nonce [16]byte
	nonce[0] = 1

	if got := cache.admit("alice", nonce, expiresAt, now); got != platformReplayAccepted {
		t.Fatalf("first admission = %v, want accepted", got)
	}
	if got := cache.admit("alice", nonce, expiresAt, now); got != platformReplayDuplicate {
		t.Fatalf("duplicate admission = %v, want duplicate", got)
	}
	if got := cache.admit("bob", nonce, expiresAt, now); got != platformReplayAccepted {
		t.Fatalf("other-principal admission = %v, want accepted", got)
	}
}

func TestPlatformReplayCacheReclaimsExpiredEntries(t *testing.T) {
	cache := newPlatformReplayCache()
	now := time.Unix(1000, 0)
	var nonce [16]byte
	nonce[0] = 1

	if got := cache.admit("alice", nonce, now.Add(time.Second), now); got != platformReplayAccepted {
		t.Fatalf("first admission = %v, want accepted", got)
	}
	if got := cache.admit("alice", nonce, now.Add(time.Minute), now.Add(2*time.Second)); got != platformReplayAccepted {
		t.Fatalf("expired nonce admission = %v, want accepted", got)
	}
	if cache.total != 1 {
		t.Fatalf("cache total = %d, want 1 after expiry reclamation", cache.total)
	}
}

func TestPlatformReplayCacheAtomicallyRejectsConcurrentDuplicates(t *testing.T) {
	cache := newPlatformReplayCache()
	now := time.Unix(1000, 0)
	expiresAt := now.Add(time.Minute)
	var nonce [16]byte
	nonce[0] = 1

	const callers = 32
	results := make(chan platformReplayResult, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			ready.Done()
			<-start
			results <- cache.admit("alice", nonce, expiresAt, now)
		}()
	}
	ready.Wait()
	close(start)

	accepted := 0
	duplicates := 0
	for range callers {
		switch <-results {
		case platformReplayAccepted:
			accepted++
		case platformReplayDuplicate:
			duplicates++
		}
	}
	if accepted != 1 || duplicates != callers-1 {
		t.Fatalf("accepted=%d duplicates=%d, want 1 and %d", accepted, duplicates, callers-1)
	}
}

func TestPlatformReplayCacheFailsClosedAtPerPrincipalCapacity(t *testing.T) {
	cache := newPlatformReplayCache()
	now := time.Unix(1000, 0)
	expiresAt := now.Add(time.Minute)
	for i := 0; i < maxPlatformReplayEntriesPrincipal; i++ {
		var nonce [16]byte
		binary.BigEndian.PutUint64(nonce[8:], uint64(i))
		if got := cache.admit("alice", nonce, expiresAt, now); got != platformReplayAccepted {
			t.Fatalf("admission %d = %v, want accepted", i, got)
		}
	}
	var overflow [16]byte
	binary.BigEndian.PutUint64(overflow[8:], uint64(maxPlatformReplayEntriesPrincipal))
	if got := cache.admit("alice", overflow, expiresAt, now); got != platformReplayCapacity {
		t.Fatalf("overflow admission = %v, want capacity", got)
	}
}

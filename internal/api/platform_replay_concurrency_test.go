package api

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPlatformReplayCacheConcurrentAdmitDedups fires many goroutines that all
// present the same principal+nonce. Under contention the cache must admit
// exactly one and reject the rest, so a captured signed request cannot be
// replayed by racing it against its first use. Run under -race.
func TestPlatformReplayCacheConcurrentAdmitDedups(t *testing.T) {
	c := newPlatformReplayCache()
	now := time.Now()
	expires := now.Add(5 * time.Minute)
	var nonce [16]byte
	copy(nonce[:], []byte("fixed-nonce-1234"))

	const goroutines = 200
	var accepted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if c.admit("principal-A", nonce, expires, now) == platformReplayAccepted {
				atomic.AddInt64(&accepted, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&accepted); got != 1 {
		t.Fatalf("same nonce admitted %d times, want exactly 1", got)
	}
	// A subsequent presentation of the same nonce is still a duplicate.
	if r := c.admit("principal-A", nonce, expires, now); r != platformReplayDuplicate {
		t.Fatalf("re-presented nonce result = %v, want duplicate", r)
	}
}

// TestPlatformReplayCacheConcurrentDistinctNoncesAllAdmitted confirms the cache
// does not falsely dedup distinct nonces from concurrent callers.
func TestPlatformReplayCacheConcurrentDistinctNoncesAllAdmitted(t *testing.T) {
	c := newPlatformReplayCache()
	now := time.Now()
	expires := now.Add(5 * time.Minute)

	const goroutines = 200 // < maxPlatformReplayEntriesPrincipal
	var accepted int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var nonce [16]byte
			copy(nonce[:], []byte(fmt.Sprintf("nonce-%010d", i)))
			<-start
			if c.admit("principal-B", nonce, expires, now) == platformReplayAccepted {
				atomic.AddInt64(&accepted, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&accepted); got != goroutines {
		t.Fatalf("distinct nonces admitted %d, want %d", got, goroutines)
	}
}

package api

import (
	"crypto/sha256"
	"sync"
	"time"
)

const (
	maxPlatformReplayPrincipals       = 4096
	maxPlatformReplayEntries          = 65536
	maxPlatformReplayEntriesPrincipal = 2048
)

type platformReplayResult uint8

const (
	platformReplayAccepted platformReplayResult = iota
	platformReplayDuplicate
	platformReplayCapacity
)

type platformReplayCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]map[[16]byte]time.Time
	total   int
}

func newPlatformReplayCache() *platformReplayCache {
	return &platformReplayCache{
		entries: make(map[[sha256.Size]byte]map[[16]byte]time.Time),
	}
}

func (c *platformReplayCache) admit(principal string, nonce [16]byte, expiresAt, now time.Time) platformReplayResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	principalKey := sha256.Sum256([]byte(principal))
	bucket := c.entries[principalKey]
	if bucket != nil {
		for candidate, expiry := range bucket {
			if !expiry.After(now) {
				delete(bucket, candidate)
				c.total--
			}
		}
		if expiry, exists := bucket[nonce]; exists && expiry.After(now) {
			return platformReplayDuplicate
		}
		if len(bucket) == 0 {
			delete(c.entries, principalKey)
			bucket = nil
		}
	}

	if c.total >= maxPlatformReplayEntries || (bucket == nil && len(c.entries) >= maxPlatformReplayPrincipals) {
		for candidatePrincipal, candidateBucket := range c.entries {
			for candidateNonce, expiry := range candidateBucket {
				if !expiry.After(now) {
					delete(candidateBucket, candidateNonce)
					c.total--
				}
			}
			if len(candidateBucket) == 0 {
				delete(c.entries, candidatePrincipal)
			}
		}
		bucket = c.entries[principalKey]
	}
	if c.total >= maxPlatformReplayEntries ||
		(bucket == nil && len(c.entries) >= maxPlatformReplayPrincipals) ||
		len(bucket) >= maxPlatformReplayEntriesPrincipal {
		return platformReplayCapacity
	}
	if bucket == nil {
		bucket = make(map[[16]byte]time.Time)
		c.entries[principalKey] = bucket
	}
	bucket[nonce] = expiresAt
	c.total++
	return platformReplayAccepted
}

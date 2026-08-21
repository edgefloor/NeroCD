package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLoginLimiterKeepsActiveCooldownWhenCapacityIsFlooded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now }, 2, time.Minute, 64)
	limiter.Failed("target@example.invalid", "198.51.100.10")
	limiter.Failed("target@example.invalid", "198.51.100.10")
	if allowed, _ := limiter.Allow("target@example.invalid", "198.51.100.10"); allowed {
		t.Fatal("target was not locked before flood")
	}

	for i := 0; i < 200; i++ {
		limiter.Failed(fmt.Sprintf("flood-%d@example.invalid", i), fmt.Sprintf("203.0.113.%d", i))
	}
	if len(limiter.entries) > limiter.maxKeys {
		t.Fatalf("entries=%d max=%d", len(limiter.entries), limiter.maxKeys)
	}
	if allowed, _ := limiter.Allow("target@example.invalid", "198.51.100.10"); allowed {
		t.Fatal("capacity flood reset the target cooldown")
	}
	if allowed, _ := limiter.Allow("fresh@example.invalid", "203.0.113.250"); allowed {
		t.Fatal("overflow bucket did not fail closed under flood")
	}

	now = now.Add(time.Minute)
	if allowed, _ := limiter.Allow("target@example.invalid", "198.51.100.10"); !allowed {
		t.Fatal("target remained blocked after cooldown")
	}
	if allowed, _ := limiter.Allow("fresh@example.invalid", "203.0.113.250"); !allowed {
		t.Fatal("overflow bucket did not recover after cooldown")
	}
}

func TestLoginLimiterConcurrentFloodRemainsBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now }, 3, time.Minute, 64)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				email := fmt.Sprintf("worker-%d-%d@example.invalid", worker, i)
				source := fmt.Sprintf("198.51.%d.%d", worker, i)
				_, _ = limiter.Allow(email, source)
				limiter.Failed(email, source)
			}
		}(worker)
	}
	wg.Wait()
	if len(limiter.entries) > limiter.maxKeys {
		t.Fatalf("entries=%d max=%d", len(limiter.entries), limiter.maxKeys)
	}
}

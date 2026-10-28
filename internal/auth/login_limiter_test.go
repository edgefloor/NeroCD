package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func failLogin(t *testing.T, l *LoginLimiter, account, source string) {
	t.Helper()
	r, _ := l.Reserve(account, source)
	if r == nil {
		t.Fatal("attempt unexpectedly rejected")
	}
	r.Failed()
}

func TestLoginLimiterKeepsActiveCooldownWhenCapacityIsFlooded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now }, 2, time.Minute, 64)
	failLogin(t, limiter, "target@example.invalid", "198.51.100.10")
	failLogin(t, limiter, "target@example.invalid", "198.51.100.10")
	for i := 0; i < 200; i++ {
		if r, _ := limiter.Reserve(fmt.Sprintf("flood-%d@example.invalid", i), fmt.Sprintf("203.0.113.%d", i)); r != nil {
			r.Failed()
		}
	}
	if len(limiter.entries) > limiter.maxKeys {
		t.Fatalf("entries=%d max=%d", len(limiter.entries), limiter.maxKeys)
	}
	for _, account := range []string{"target@example.invalid", "fresh@example.invalid"} {
		if r, _ := limiter.Reserve(account, "203.0.113.250"); r != nil {
			t.Fatal("flood reset cooldown or bypassed overflow")
		}
	}
	now = now.Add(time.Minute)
	if r, _ := limiter.Reserve("target@example.invalid", "198.51.100.10"); r == nil {
		t.Fatal("target remained blocked after cooldown")
	} else {
		r.Cancel()
	}
	if r, _ := limiter.Reserve("fresh@example.invalid", "203.0.113.250"); r == nil {
		t.Fatal("overflow did not recover")
	} else {
		r.Cancel()
	}
}

func TestLoginLimiterConcurrentFloodRemainsBounded(t *testing.T) {
	limiter := NewLoginLimiter(time.Now, 3, time.Minute, 64)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				if r, _ := limiter.Reserve(fmt.Sprintf("worker-%d-%d@example.invalid", worker, i), fmt.Sprintf("198.51.%d.%d", worker, i)); r != nil {
					r.Failed()
				}
			}
		}(worker)
	}
	wg.Wait()
	if len(limiter.entries) > limiter.maxKeys {
		t.Fatalf("entries=%d max=%d", len(limiter.entries), limiter.maxKeys)
	}
}

func TestLoginReservationSettlementPreservesOtherPendingAttempts(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLoginLimiter(func() time.Time { return now }, 2, time.Minute, 64)
	first, _ := l.Reserve("account", "source1")
	second, _ := l.Reserve("ACCOUNT", "source2")
	if first == nil || second == nil {
		t.Fatal("initial admission")
	}
	if r, _ := l.Reserve("account", "source3"); r != nil {
		t.Fatal("pending account budget bypassed")
	}
	now = now.Add(2 * time.Minute)
	if r, _ := l.Reserve("account", "source3"); r != nil {
		t.Fatal("expiration erased pending attempts")
	}
	first.Succeeded()
	first.Cancel() // Deferred cleanup must not release a second slot.
	third, _ := l.Reserve("account", "source3")
	if third == nil {
		t.Fatal("success did not release its capacity")
	}
	if r, _ := l.Reserve("account", "source4"); r != nil {
		t.Fatal("success erased another pending attempt")
	}
	second.Failed()
	third.Cancel()
	if r, _ := l.Reserve("other", "source2"); r == nil {
		t.Fatal("one source failure exhausted two slots")
	} else {
		r.Failed()
	}
	if r, _ := l.Reserve("another", "source2"); r != nil {
		t.Fatal("source failures were not retained")
	}
}

func TestLoginReservationAtomicallyChargesBothBudgets(t *testing.T) {
	l := NewLoginLimiter(time.Now, 1, time.Minute, 64)
	pending, _ := l.Reserve("first", "source")
	if pending == nil {
		t.Fatal("initial admission")
	}
	if r, _ := l.Reserve("second", "source"); r != nil {
		t.Fatal("pending source budget bypassed")
	}
	// Rejection for the source must not consume capacity for the second account.
	independent, _ := l.Reserve("second", "other-source")
	if independent == nil {
		t.Fatal("partial reservation consumed account capacity")
	}
	independent.Cancel()
	pending.Cancel()
	if r, _ := l.Reserve("first", "source"); r == nil {
		t.Fatal("cancellation leaked capacity")
	} else {
		r.Cancel()
	}
}

func TestLoginReservationOverflowChargesOnceAndPreservesPending(t *testing.T) {
	l := NewLoginLimiter(time.Now, 2, time.Minute, 64)
	for i := 0; i < 32; i++ {
		failLogin(t, l, fmt.Sprint("account", i), fmt.Sprint("source", i))
	}
	first, _ := l.Reserve("overflow1", "overflow-source1")
	second, _ := l.Reserve("overflow2", "overflow-source2")
	if first == nil || second == nil {
		t.Fatal("overflow charged an attempt twice")
	}
	if r, _ := l.Reserve("overflow3", "overflow-source3"); r != nil {
		t.Fatal("overflow pending budget bypassed")
	}
	first.Succeeded()
	first.Cancel()
	third, _ := l.Reserve("overflow3", "overflow-source3")
	if third == nil {
		t.Fatal("overflow success did not release capacity")
	}
	if r, _ := l.Reserve("overflow4", "overflow-source4"); r != nil {
		t.Fatal("overflow success erased other pending capacity")
	}
	second.Failed()
	third.Failed()
	if r, _ := l.Reserve("overflow4", "overflow-source4"); r != nil {
		t.Fatal("overflow failure accounting lost")
	}
}

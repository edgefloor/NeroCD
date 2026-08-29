package auth

import (
	"strings"
	"sync"
	"time"
)

// LoginLimiter is a bounded, in-process failed-login limiter.  It intentionally
// tracks both the canonical account and the trusted client source: an attacker
// cannot evade an account lock by rotating addresses or spray accounts from one
// address. It is an availability control, not distributed state.
type LoginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	limit    int
	maxKeys  int
	entries  map[string]loginLimitEntry
	overflow loginLimitEntry
}

type loginLimitEntry struct {
	started time.Time
	fails   int
}

// NewLoginLimiter constructs an in-memory login-attempt limiter.
func NewLoginLimiter(now func() time.Time, limit int, window time.Duration, maxKeys int) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	if limit < 1 {
		limit = 5
	}
	if window < time.Second {
		window = time.Minute
	}
	if maxKeys < 64 {
		maxKeys = 10_000
	}
	return &LoginLimiter{now: now, limit: limit, window: window, maxKeys: maxKeys, entries: map[string]loginLimitEntry{}}
}

func (l *LoginLimiter) keys(account, source string) []string {
	account = strings.ToLower(strings.TrimSpace(account))
	source = strings.TrimSpace(source)
	if account == "" {
		account = "<empty>"
	}
	if source == "" {
		source = "<unknown>"
	}
	return []string{"account:" + account, "source:" + source}
}

// Allow reports whether a request may proceed and a bounded retry duration.
func (l *LoginLimiter) Allow(account, source string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	retry := time.Duration(0)
	for _, key := range l.keys(account, source) {
		entry, ok := l.entries[key]
		if !ok || !now.Before(entry.started.Add(l.window)) {
			continue
		}
		if entry.fails >= l.limit {
			if left := entry.started.Add(l.window).Sub(now); left > retry {
				retry = left
			}
			if retry <= 0 {
				retry = time.Second
			}
		}
	}
	// Once capacity is reached, unknown account/source pairs are charged to a
	// single bounded overflow bucket. Checking it for every request is
	// deliberately fail-closed: flooding cannot evict an active target's
	// cooldown and turn the limiter into a reset oracle.
	if now.Before(l.overflow.started.Add(l.window)) && l.overflow.fails >= l.limit {
		if left := l.overflow.started.Add(l.window).Sub(now); left > retry {
			retry = left
		}
	}
	return retry == 0, retry
}

// Failed records one completed invalid attempt.  The map is deliberately
// bounded; expiry is preferred and, under a flood, the oldest window is evicted.
func (l *LoginLimiter) Failed(account, source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	for key, entry := range l.entries {
		if !now.Before(entry.started.Add(l.window)) {
			delete(l.entries, key)
		}
	}
	if !l.overflow.started.IsZero() && !now.Before(l.overflow.started.Add(l.window)) {
		l.overflow = loginLimitEntry{}
	}
	overflowed := false
	for _, key := range l.keys(account, source) {
		entry, exists := l.entries[key]
		if !exists {
			if len(l.entries) >= l.maxKeys {
				overflowed = true
				continue
			}
			entry.started = now
		}
		entry.fails++
		l.entries[key] = entry
	}
	if overflowed {
		if l.overflow.started.IsZero() {
			l.overflow.started = now
		}
		l.overflow.fails++
	}
}

// Succeeded clears failed-attempt state for account.
func (l *LoginLimiter) Succeeded(account string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, "account:"+strings.ToLower(strings.TrimSpace(account)))
}

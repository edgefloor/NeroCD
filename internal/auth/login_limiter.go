package auth

import (
	"strings"
	"sync"
	"time"
)

// LoginLimiter bounds completed failures and pending attempts per account and
// trusted source. It is an in-process availability control, not distributed state.
type LoginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	window   time.Duration
	limit    int
	maxKeys  int
	entries  map[string]*loginLimitEntry
	overflow *loginLimitEntry
}

type loginLimitEntry struct {
	started time.Time
	fails   int
	pending int
}

// LoginReservation holds account and source capacity until verification settles.
// Its settlement methods are safe to call concurrently and have effect once.
type LoginReservation struct {
	limiter *LoginLimiter
	entries []*loginLimitEntry
	account *loginLimitEntry
	settled bool
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
	return &LoginLimiter{now: now, limit: limit, window: window, maxKeys: maxKeys, entries: map[string]*loginLimitEntry{}, overflow: &loginLimitEntry{}}
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

func (l *LoginLimiter) expire(entry *loginLimitEntry, now time.Time) {
	if !now.Before(entry.started.Add(l.window)) {
		entry.fails = 0
		entry.started = time.Time{}
	}
}

func (l *LoginLimiter) retry(entry *loginLimitEntry, now time.Time) time.Duration {
	if entry.fails+entry.pending < l.limit {
		return 0
	}
	if left := entry.started.Add(l.window).Sub(now); left > 0 {
		return left
	}
	// Pending work remains charged even when the failure window has expired.
	return l.window
}

// Reserve atomically admits one attempt against both budgets. A nil reservation
// means no work may start. Call Failed, Succeeded, or Cancel on every reservation.
func (l *LoginLimiter) Reserve(account, source string) (*LoginReservation, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	for key, entry := range l.entries {
		l.expire(entry, now)
		if entry.fails == 0 && entry.pending == 0 {
			delete(l.entries, key)
		}
	}
	l.expire(l.overflow, now)
	// Flooding cannot evict an active account's cooldown or pending reservation.
	if retry := l.retry(l.overflow, now); retry > 0 {
		return nil, retry
	}
	keys := l.keys(account, source)
	entries := make([]*loginLimitEntry, len(keys))
	additions := 0
	for i, key := range keys {
		entry := l.entries[key]
		if entry == nil {
			entry = l.overflow
			if len(l.entries)+additions < l.maxKeys {
				entry = &loginLimitEntry{}
				additions++
			}
		}
		if retry := l.retry(entry, now); retry > 0 {
			return nil, retry
		}
		entries[i] = entry
	}
	reservation := &LoginReservation{limiter: l}
	for i, entry := range entries {
		if entry != l.overflow {
			l.entries[keys[i]] = entry
			if i == 0 {
				reservation.account = entry
			}
		}
		// Account and source can share the single bounded overflow bucket.
		if i > 0 && entry == entries[0] {
			continue
		}
		entry.pending++
		reservation.entries = append(reservation.entries, entry)
	}
	return reservation, 0
}

// Failed converts the pending capacity to a completed invalid attempt.
func (r *LoginReservation) Failed() { r.settle(true, false) }

// Succeeded releases capacity and clears account failures. Source failures and
// other in-flight attempts remain charged, including the shared overflow bucket.
func (r *LoginReservation) Succeeded() { r.settle(false, true) }

// Cancel releases capacity after cancellation or an infrastructure error.
func (r *LoginReservation) Cancel() { r.settle(false, false) }

func (r *LoginReservation) settle(failed, succeeded bool) {
	l := r.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.settled {
		return
	}
	r.settled = true
	now := l.now().UTC()
	for _, entry := range r.entries {
		l.expire(entry, now)
		entry.pending--
		if failed {
			if entry.started.IsZero() {
				entry.started = now
			}
			entry.fails++
		}
	}
	if succeeded && r.account != nil {
		r.account.fails = 0
		r.account.started = time.Time{}
	}
}

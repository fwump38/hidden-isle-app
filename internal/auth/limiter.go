package auth

import (
	"sync"
	"time"
)

// Limiter locks a key (user + client IP) after too many failed logins.
type Limiter struct {
	mu       sync.Mutex
	fails    map[string][]time.Time
	max      int
	window   time.Duration
	lockout  time.Duration
	lockedAt map[string]time.Time
	now      func() time.Time
}

func NewLimiter(max int, window, lockout time.Duration) *Limiter {
	return &Limiter{fails: map[string][]time.Time{}, lockedAt: map[string]time.Time{}, max: max,
		window: window, lockout: lockout, now: time.Now}
}

// Locked reports whether key is locked out, and for how long.
func (l *Limiter) Locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.lockedAt[key]
	if !ok {
		return false, 0
	}
	left := l.lockout - l.now().Sub(at)
	if left <= 0 {
		delete(l.lockedAt, key)
		delete(l.fails, key)
		return false, 0
	}
	return true, left
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var recent []time.Time
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	l.fails[key] = recent
	if len(recent) >= l.max {
		l.lockedAt[key] = now
	}
}

// Succeed clears the key's failures.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
	delete(l.lockedAt, key)
}

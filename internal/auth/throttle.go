package auth

import (
	"sync"
	"time"

	"proxy-checker/internal/domain"
)

// Login throttling limits.
const (
	LoginPerPeerPerMinute = 5
	LoginGlobalPerMinute  = 20
	loginWindow           = time.Minute
	maxTrackedPeers       = 4096
)

// LoginLimiter bounds login attempts per network peer and globally using
// sliding windows. State is bounded and expires on its own.
type LoginLimiter struct {
	clock domain.Clock

	mu     sync.Mutex
	peers  map[string][]time.Time
	global []time.Time
}

// NewLoginLimiter returns a limiter using clock.
func NewLoginLimiter(clock domain.Clock) *LoginLimiter {
	return &LoginLimiter{clock: clock, peers: map[string][]time.Time{}}
}

// Allow records an attempt from peer when permitted. When denied it returns
// how long the caller should wait. The peer must come from the TCP connection,
// never from forwarded headers.
func (l *LoginLimiter) Allow(peer string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.global = prune(l.global, now)
	hits, known := l.peers[peer]
	hits = prune(hits, now)

	if len(l.global) >= LoginGlobalPerMinute {
		return false, retryAfter(l.global[0], now)
	}
	if len(hits) >= LoginPerPeerPerMinute {
		l.peers[peer] = hits
		return false, retryAfter(hits[0], now)
	}
	if !known && len(l.peers) >= maxTrackedPeers {
		l.sweep(now)
		if len(l.peers) >= maxTrackedPeers {
			return false, loginWindow
		}
	}
	l.peers[peer] = append(hits, now)
	l.global = append(l.global, now)
	return true, 0
}

func (l *LoginLimiter) sweep(now time.Time) {
	for k, v := range l.peers {
		if v = prune(v, now); len(v) == 0 {
			delete(l.peers, k)
		} else {
			l.peers[k] = v
		}
	}
}

func prune(hits []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(hits) && !hits[i].Add(loginWindow).After(now) {
		i++
	}
	return hits[i:]
}

func retryAfter(oldest, now time.Time) time.Duration {
	d := oldest.Add(loginWindow).Sub(now)
	if d < time.Second {
		d = time.Second
	}
	return d
}

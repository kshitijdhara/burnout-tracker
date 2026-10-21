package ingest

import (
	"sync"
	"time"
)

// tokenBucket is a small, clock-injected token bucket. A misbehaving
// same-user process can't flood the daemon; excess messages are dropped and
// counted rather than queued.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newTokenBucket(rate, burst int, now func() time.Time) *tokenBucket {
	return &tokenBucket{rate: float64(rate), burst: float64(burst), tokens: float64(burst), last: now(), now: now}
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.now()
	if elapsed := t.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(b.burst, b.tokens+elapsed*b.rate)
	}
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

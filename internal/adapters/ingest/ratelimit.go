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

// Per-source limits (privacy & security §8.1): one misbehaving process can't
// use up the global budget or the connection slots of every other hook.
const (
	sourceRate       = 50  // messages per second per source process
	sourceBurst      = 200 // burst per source process
	sourceMaxConns   = 8   // concurrent connections per source process
	sourceMaxTracked = 256 // distinct sources remembered; oldest evicted
)

type admission int

const (
	admitted admission = iota
	sourceRateLimited
	sourceBusy
)

// sourceLimiter keeps a token bucket and an open-connection count per
// source process ID, in a map bounded to sourceMaxTracked entries.
type sourceLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	sources map[int]*source
}

type source struct {
	bucket   *tokenBucket
	open     int
	lastSeen time.Time
}

func newSourceLimiter(now func() time.Time) *sourceLimiter {
	return &sourceLimiter{now: now, sources: make(map[int]*source)}
}

// admit reserves a connection slot for pid if its rate and concurrency
// limits allow. Callers must release an admitted pid.
func (l *sourceLimiter) admit(pid int) admission {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	src, ok := l.sources[pid]
	if !ok {
		l.evictIfFull()
		src = &source{bucket: newTokenBucket(sourceRate, sourceBurst, l.now)}
		l.sources[pid] = src
	}
	src.lastSeen = t
	if src.open >= sourceMaxConns {
		return sourceBusy
	}
	if !src.bucket.allow() {
		return sourceRateLimited
	}
	src.open++
	return admitted
}

func (l *sourceLimiter) release(pid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if src, ok := l.sources[pid]; ok && src.open > 0 {
		src.open--
	}
}

// evictIfFull drops the least recently seen idle source. Sources with open
// connections are kept so their counts stay correct; if every tracked
// source is busy the map may briefly exceed the bound by the number of
// in-flight connections, which maxConcurrent already caps.
func (l *sourceLimiter) evictIfFull() {
	if len(l.sources) < sourceMaxTracked {
		return
	}
	oldest, found := 0, false
	var oldestSeen time.Time
	for pid, src := range l.sources {
		if src.open > 0 {
			continue
		}
		if !found || src.lastSeen.Before(oldestSeen) {
			oldest, oldestSeen, found = pid, src.lastSeen, true
		}
	}
	if found {
		delete(l.sources, oldest)
	}
}

func (l *sourceLimiter) tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sources)
}

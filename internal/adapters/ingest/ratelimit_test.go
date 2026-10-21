package ingest

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	now := time.Unix(1000, 0)
	b := newTokenBucket(10, 5, func() time.Time { return now })

	for i := range 5 {
		if !b.allow() {
			t.Fatalf("burst token %d denied", i)
		}
	}
	if b.allow() {
		t.Fatal("allowed beyond burst")
	}

	now = now.Add(100 * time.Millisecond) // refills one token at 10/s
	if !b.allow() {
		t.Fatal("refilled token denied")
	}
	if b.allow() {
		t.Fatal("allowed more than refilled")
	}

	now = now.Add(time.Hour) // refill is capped at burst
	for i := range 5 {
		if !b.allow() {
			t.Fatalf("token %d denied after long idle", i)
		}
	}
	if b.allow() {
		t.Fatal("refill exceeded burst")
	}

	now = now.Add(-time.Minute) // a clock step backwards never adds tokens
	if b.allow() {
		t.Fatal("clock going backwards added tokens")
	}
}

func TestSourceLimiterRate(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newSourceLimiter(func() time.Time { return now })
	for i := range sourceBurst {
		if l.admit(1) != admitted {
			t.Fatalf("burst %d denied", i)
		}
		l.release(1)
	}
	if got := l.admit(1); got != sourceRateLimited {
		t.Fatalf("source over its burst: got %v", got)
	}
	// Another source is unaffected by the first one's flood.
	if l.admit(2) != admitted {
		t.Fatal("independent source was limited")
	}
	l.release(2)
	now = now.Add(time.Second) // refills sourceRate tokens
	if l.admit(1) != admitted {
		t.Fatal("refilled source denied")
	}
	l.release(1)
}

func TestSourceLimiterConcurrency(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newSourceLimiter(func() time.Time { return now })
	for range sourceMaxConns {
		if l.admit(7) != admitted {
			t.Fatal("slot denied")
		}
	}
	if got := l.admit(7); got != sourceBusy {
		t.Fatalf("over connection cap: got %v", got)
	}
	l.release(7)
	if l.admit(7) != admitted {
		t.Fatal("released slot not reusable")
	}
}

func TestSourceLimiterBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newSourceLimiter(func() time.Time { return now })
	for pid := range 10 * sourceMaxTracked {
		now = now.Add(time.Millisecond)
		if l.admit(pid) != admitted {
			t.Fatalf("pid %d denied", pid)
		}
		l.release(pid)
	}
	if got := l.tracked(); got > sourceMaxTracked {
		t.Fatalf("tracked %d sources, bound is %d", got, sourceMaxTracked)
	}
	// A busy source is never evicted, so its count stays right.
	l2 := newSourceLimiter(func() time.Time { return now })
	if l2.admit(-1) != admitted {
		t.Fatal("busy source denied")
	}
	for pid := range 2 * sourceMaxTracked {
		l2.admit(pid)
		l2.release(pid)
	}
	l2.mu.Lock()
	busy := l2.sources[-1]
	l2.mu.Unlock()
	if busy == nil || busy.open != 1 {
		t.Fatal("busy source was evicted")
	}
}

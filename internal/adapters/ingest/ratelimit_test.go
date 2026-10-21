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

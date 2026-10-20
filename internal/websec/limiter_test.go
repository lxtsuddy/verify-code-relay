package websec

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(1, 5, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("burst %d denied", i)
		}
	}
	if l.Allow() {
		t.Fatal("6th allowed")
	}
	now = now.Add(time.Second)
	if !l.Allow() || l.Allow() {
		t.Fatal("refill should allow exactly one")
	}
	now = now.Add(time.Hour)
	for i := 0; i < 5; i++ {
		l.Allow()
	}
	if l.Allow() {
		t.Fatal("bucket must cap at burst")
	}
}

func TestLimiterClockGoingBackwards(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(1, 1, func() time.Time { return now })
	if !l.Allow() {
		t.Fatal("first denied")
	}
	now = now.Add(-time.Hour)
	if l.Allow() {
		t.Fatal("backwards clock must not refill")
	}
}

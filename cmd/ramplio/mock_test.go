package main

import (
	"testing"
	"time"
)

func TestPickLatency_FixedOverridesBimodal(t *testing.T) {
	p := latencyProfile{Fixed: 25 * time.Millisecond, Fast: 1 * time.Millisecond, Slow: 1 * time.Second, SlowPct: 50}
	for n := int64(1); n <= 200; n++ {
		if got := p.pickLatency(n); got != 25*time.Millisecond {
			t.Fatalf("n=%d: fixed latency must win, got %s", n, got)
		}
	}
}

func TestPickLatency_BimodalDistribution(t *testing.T) {
	const (
		fast = 10 * time.Millisecond
		slow = 200 * time.Millisecond
		pct  = 10
	)
	p := latencyProfile{Fast: fast, Slow: slow, SlowPct: pct}

	slowCount := 0
	const total = 100
	for n := int64(1); n <= total; n++ {
		if p.pickLatency(n) == slow {
			slowCount++
		}
	}
	if slowCount != pct {
		t.Fatalf("expected exactly %d slow requests per 100, got %d", pct, slowCount)
	}
}

func TestPickLatency_NoConfigMeansNoDelay(t *testing.T) {
	p := latencyProfile{}
	if got := p.pickLatency(1); got != 0 {
		t.Fatalf("empty profile must inject no delay, got %s", got)
	}
}

func TestPickLatency_SlowNeedsSlowPct(t *testing.T) {
	// Slow set but SlowPct=0 → never slow, always fast.
	p := latencyProfile{Fast: 5 * time.Millisecond, Slow: 500 * time.Millisecond, SlowPct: 0}
	for n := int64(1); n <= 100; n++ {
		if p.pickLatency(n) != 5*time.Millisecond {
			t.Fatalf("n=%d: with slow-pct=0 every request must be fast", n)
		}
	}
}

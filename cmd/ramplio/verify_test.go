package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/internal/metrics"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func summaryWith(p50, p90, p95, p99 int) metrics.Summary {
	return metrics.Summary{Total: 1000, P50: ms(p50), P90: ms(p90), P95: ms(p95), P99: ms(p99)}
}

func TestEvaluateFixed_Pass(t *testing.T) {
	out := evaluateFixed(ms(50), ms(20), summaryWith(51, 52, 52, 54))
	if !out.Pass {
		t.Fatalf("expected pass, got fail: %+v", out)
	}
	if !strings.Contains(out.Headline, "量測準確") {
		t.Errorf("headline missing 量測準確: %q", out.Headline)
	}
}

func TestEvaluateFixed_Undercut(t *testing.T) {
	// p99 = 40ms < injected 50ms → the most serious failure: under-reporting.
	out := evaluateFixed(ms(50), ms(20), summaryWith(51, 52, 52, 40))
	if out.Pass {
		t.Fatal("expected fail on undercut")
	}
	for _, want := range []string{"低於", "bug"} {
		if !strings.Contains(out.Reason, want) {
			t.Errorf("undercut reason missing %q: %q", want, out.Reason)
		}
	}
}

func TestEvaluateFixed_OverTolerance(t *testing.T) {
	// p99 = 90ms > 50+20, but nothing undercuts → tolerance/load message.
	out := evaluateFixed(ms(50), ms(20), summaryWith(51, 60, 70, 90))
	if out.Pass {
		t.Fatal("expected fail on over-tolerance")
	}
	if strings.Contains(out.Reason, "bug") {
		t.Errorf("over-tolerance should not be flagged as a bug: %q", out.Reason)
	}
	verdict := out.Headline + " " + out.Reason
	for _, want := range []string{"容差", "--vus"} {
		if !strings.Contains(verdict, want) {
			t.Errorf("over-tolerance verdict missing %q: %q", want, verdict)
		}
	}
}

func TestEvaluateBimodal_Pass(t *testing.T) {
	// fast=10ms, slow=200ms, tol=30ms; p50 in fast band, p99 in slow band.
	out := evaluateBimodal(ms(10), ms(200), ms(30), summaryWith(12, 80, 150, 210))
	if !out.Pass {
		t.Fatalf("expected pass, got fail: %+v", out)
	}
	if !strings.Contains(out.Headline, "尾端") {
		t.Errorf("bimodal pass headline missing 尾端: %q", out.Headline)
	}
}

func TestEvaluateBimodal_TailMissed(t *testing.T) {
	// p99 = 50ms never reaches the slow band [200, 230] → fail.
	out := evaluateBimodal(ms(10), ms(200), ms(30), summaryWith(12, 30, 40, 50))
	if out.Pass {
		t.Fatal("expected fail when the slow tail is not separated")
	}
}

func TestWriteVerifyReport(t *testing.T) {
	out := evaluateFixed(ms(50), ms(20), summaryWith(51, 52, 52, 54))
	var buf bytes.Buffer
	writeVerifyReport(&buf, verifyHeader{Distribution: "固定 50ms", Load: "10 VU × 3s", Tolerance: "±20ms"}, out)
	s := buf.String()
	for _, want := range []string{"量測結果", "p50", "p99", "✓", "量測準確"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q\n--- got ---\n%s", want, s)
		}
	}
	for _, bad := range []string{"Capacity", "Measured", "tolerance"} {
		if strings.Contains(s, bad) {
			t.Errorf("report contains English leftover %q", bad)
		}
	}
}

func TestParseVerifyProfile(t *testing.T) {
	// Default → fixed 50ms.
	p, bimodal, err := parseVerifyProfile("", "", "", 10)
	if err != nil || bimodal || p.Fixed != ms(50) {
		t.Errorf("default: got profile=%+v bimodal=%v err=%v", p, bimodal, err)
	}
	// Both fast+slow → bimodal.
	p, bimodal, err = parseVerifyProfile("", "10ms", "200ms", 10)
	if err != nil || !bimodal || p.Fast != ms(10) || p.Slow != ms(200) {
		t.Errorf("bimodal: got profile=%+v bimodal=%v err=%v", p, bimodal, err)
	}
	// Only fast → error (incomplete bimodal).
	if _, _, err = parseVerifyProfile("", "10ms", "", 10); err == nil {
		t.Error("expected error when only --latency-fast is set")
	}
	// Fixed + bimodal → error (mutually exclusive).
	if _, _, err = parseVerifyProfile("50ms", "10ms", "200ms", 10); err == nil {
		t.Error("expected error when --latency mixed with bimodal")
	}
	// Out-of-range slow-pct → error.
	if _, _, err = parseVerifyProfile("", "10ms", "200ms", 150); err == nil {
		t.Error("expected error for slow-pct > 100")
	}
}

// TestVerify_EndToEnd runs the real command against the in-process mock target.
// Timing-dependent, so it is skipped in -short mode (mirrors groundtruth_test).
func TestVerify_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("verify end-to-end timing test skipped in -short mode")
	}
	cmd := newVerifyCmd()
	cmd.SetArgs([]string{"--latency", "40ms", "--duration", "2s", "--tolerance", "30ms"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("verify should pass against a 40ms fixed target: %v", err)
	}
}

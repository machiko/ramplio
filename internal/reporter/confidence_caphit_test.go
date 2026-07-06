package reporter_test

import (
	"strings"
	"testing"

	"github.com/machiko/ramplio/internal/metrics"
	"github.com/machiko/ramplio/internal/reporter"
)

// TestMeasurementConfidence_CapHitDowngradesClean: an otherwise clean run that
// reached the rate-mode worker ceiling drops from high to medium with an
// attribution caveat — the generator itself may be the bottleneck.
func TestMeasurementConfidence_CapHitDowngradesClean(t *testing.T) {
	r := reporter.MeasurementConfidence(metrics.Summary{Total: 10000, GeneratorWorkerCapHit: true})
	if r.Level != "medium" {
		t.Errorf("cap-hit should downgrade a clean run to medium, got %q", r.Level)
	}
	if !strings.Contains(r.Note, "worker 上限") {
		t.Errorf("note should explain the worker ceiling: %q", r.Note)
	}
}

// TestMeasurementConfidence_CapHitKeepsLow: a run already low for dropped samples
// stays low, but the cap caveat is appended rather than dropped.
func TestMeasurementConfidence_CapHitKeepsLow(t *testing.T) {
	r := reporter.MeasurementConfidence(metrics.Summary{Total: 100, DroppedSamples: 50, GeneratorWorkerCapHit: true})
	if r.Level != "low" {
		t.Errorf("a dropped-heavy run must stay low even with cap-hit, got %q", r.Level)
	}
	if !strings.Contains(r.Note, "worker 上限") {
		t.Errorf("note should still mention the worker ceiling: %q", r.Note)
	}
}

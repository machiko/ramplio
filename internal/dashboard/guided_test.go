package dashboard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── TranslateProfile ───────────────────────────────────────────

func TestTranslateProfile_Steady(t *testing.T) {
	p := GuidedProfile{URL: "https://example.com", Method: "GET", ConcurrentUsers: 100, TrafficShape: "steady"}
	plan := TranslateProfile(p)
	require.Len(t, plan.Stages, 3)
	assert.Equal(t, 100, plan.MaxVUs)
	assert.Equal(t, 100, plan.Stages[0].Target)
	assert.Equal(t, 100, plan.Stages[1].Target)
	assert.Equal(t, 0, plan.Stages[2].Target)
	assert.GreaterOrEqual(t, plan.TotalDuration, 30*time.Second)
}

func TestTranslateProfile_Spike(t *testing.T) {
	p := GuidedProfile{URL: "https://example.com", Method: "GET", ConcurrentUsers: 50, TrafficShape: "spike"}
	plan := TranslateProfile(p)
	require.Len(t, plan.Stages, 3)
	assert.Equal(t, 50, plan.MaxVUs)
	assert.Equal(t, 60*time.Second, plan.TotalDuration)
}

func TestTranslateProfile_Soak(t *testing.T) {
	p := GuidedProfile{URL: "https://example.com", Method: "GET", ConcurrentUsers: 20, TrafficShape: "soak"}
	plan := TranslateProfile(p)
	require.Len(t, plan.Stages, 3)
	assert.Equal(t, 20, plan.MaxVUs)
	assert.Equal(t, 5*time.Minute, plan.TotalDuration)
}

func TestTranslateProfile_SteadyFloorDuration(t *testing.T) {
	// 1 user: 1×300ms = 0.3s << 30s → floor kicks in
	plan := TranslateProfile(GuidedProfile{URL: "https://example.com", Method: "GET", ConcurrentUsers: 1, TrafficShape: "steady"})
	assert.GreaterOrEqual(t, plan.TotalDuration, 30*time.Second)
}

func TestTranslateProfile_BoundsClamping(t *testing.T) {
	tests := []struct {
		name    string
		users   int
		wantVUs int
	}{
		{"zero → 1 VU", 0, 1},
		{"negative → 1 VU", -10, 1},
		{"over max → 5000 VU", 99999, 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := TranslateProfile(GuidedProfile{
				URL: "https://example.com", Method: "GET",
				ConcurrentUsers: tt.users, TrafficShape: "steady",
			})
			assert.Equal(t, tt.wantVUs, plan.MaxVUs)
		})
	}
}

func TestTranslateProfile_StepBuiltFromProfile(t *testing.T) {
	// RampPlan no longer includes Steps — callers build engine.RampStep themselves.
	// This test verifies the method field helper round-trips correctly.
	assert.Equal(t, "GET", GuidedMethod("browse"))
	assert.Equal(t, "GET", GuidedMethod("api"))
	assert.Equal(t, "POST", GuidedMethod("checkout-like"))
}

// ── InterpretResult ─────────────────────────────────────────────

func TestInterpretResult_Pass(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 100, TargetLatencyMs: 3000, TrafficShape: "steady"}
	r := RunResult{Total: 1000, Errors: 0, ErrorPct: 0, P95Ms: 250}
	v := InterpretResult(p, r)
	assert.Equal(t, "pass", v.Level)
	assert.NotNil(t, v.SuggestedRetry)
	assert.Greater(t, v.SuggestedRetry.ConcurrentUsers, 100)
}

func TestInterpretResult_Warn(t *testing.T) {
	// p95 > target but ≤ 1.5× target, error < 1%
	p := GuidedProfile{ConcurrentUsers: 100, TargetLatencyMs: 1000, TrafficShape: "steady"}
	r := RunResult{Total: 1000, Errors: 0, ErrorPct: 0, P95Ms: 1400}
	v := InterpretResult(p, r)
	assert.Equal(t, "warn", v.Level)
	require.NotNil(t, v.SuggestedRetry)
	assert.Less(t, v.SuggestedRetry.ConcurrentUsers, 100)
}

func TestInterpretResult_Fail_HighError(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 100, TargetLatencyMs: 3000, TrafficShape: "spike"}
	r := RunResult{Total: 1000, Errors: 50, ErrorPct: 5.0, P95Ms: 800}
	v := InterpretResult(p, r)
	assert.Equal(t, "fail", v.Level)
	require.NotNil(t, v.SuggestedRetry)
	assert.Less(t, v.SuggestedRetry.ConcurrentUsers, 100)
}

func TestInterpretResult_Fail_HighLatency(t *testing.T) {
	// p95 >> 1.5× target
	p := GuidedProfile{ConcurrentUsers: 100, TargetLatencyMs: 1000, TrafficShape: "steady"}
	r := RunResult{Total: 1000, Errors: 0, ErrorPct: 0, P95Ms: 5000}
	v := InterpretResult(p, r)
	assert.Equal(t, "fail", v.Level)
}

func TestInterpretResult_ZeroTraffic(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 100, TargetLatencyMs: 3000}
	r := RunResult{Total: 0}
	v := InterpretResult(p, r)
	assert.Equal(t, "fail", v.Level)
	assert.Nil(t, v.SuggestedRetry)
}

func TestInterpretResult_DefaultTargetLatency(t *testing.T) {
	// TargetLatencyMs == 0 should not panic; defaults to 3000ms
	p := GuidedProfile{ConcurrentUsers: 10, TargetLatencyMs: 0, TrafficShape: "steady"}
	r := RunResult{Total: 100, Errors: 0, ErrorPct: 0, P95Ms: 200}
	v := InterpretResult(p, r)
	assert.Equal(t, "pass", v.Level)
}

func TestInterpretResult_SuggestedRetryCap(t *testing.T) {
	// When users × 1.5 > 5000, retry should be capped
	p := GuidedProfile{ConcurrentUsers: 4000, TargetLatencyMs: 5000, TrafficShape: "steady"}
	r := RunResult{Total: 100, Errors: 0, ErrorPct: 0, P95Ms: 100}
	v := InterpretResult(p, r)
	if v.SuggestedRetry != nil {
		assert.LessOrEqual(t, v.SuggestedRetry.ConcurrentUsers, 5000)
	}
}

// ── GuidedDurationLabel ─────────────────────────────────────────

func TestGuidedDurationLabel_Steady_Short(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 1, TrafficShape: "steady"}
	label := GuidedDurationLabel(p)
	assert.Contains(t, label, "秒")
}

func TestGuidedDurationLabel_Soak(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 10, TrafficShape: "soak"}
	label := GuidedDurationLabel(p)
	assert.Contains(t, label, "分鐘")
}

func TestGuidedDurationLabel_Spike(t *testing.T) {
	p := GuidedProfile{ConcurrentUsers: 100, TrafficShape: "spike"}
	label := GuidedDurationLabel(p)
	assert.Equal(t, "1 分鐘", label)
}

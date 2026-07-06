package dashboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/scenarios"
)

// GuidedProfile holds PM-facing business inputs from the guided wizard.
type GuidedProfile struct {
	URL             string `json:"url"`
	Method          string `json:"method"` // defaults to "GET"
	ConcurrentUsers int    `json:"concurrent_users"`
	TargetLatencyMs int    `json:"target_latency_ms"` // e.g. 1000, 3000, 5000
	TrafficShape    string `json:"traffic_shape"`     // "steady" | "spike" | "soak"
	ScenarioKind    string `json:"scenario_kind"`     // "browse" | "api" | "checkout-like"
}

// RampPlan is the technical test configuration produced by TranslateProfile.
// dashcontrol uses Stages + MaxVUs to build the engine; Steps are built separately
// from the profile URL/Method so the dashboard package need not import engine.
type RampPlan struct {
	Stages        []scenarios.Stage
	MaxVUs        int
	TotalDuration time.Duration
}

// GuidedVerdict is the PM-readable interpretation of a completed test.
type GuidedVerdict struct {
	Level          string         `json:"level"` // "pass" | "warn" | "fail"
	Headline       string         `json:"headline"`
	Detail         string         `json:"detail"`
	NextStep       string         `json:"next_step"`
	SuggestedRetry *GuidedProfile `json:"suggested_retry,omitempty"`
}

// TranslateProfile converts PM business inputs into a technical ramp plan.
// VU count is clamped to [1, 5000]. Duration floors are enforced per shape.
func TranslateProfile(p GuidedProfile) RampPlan {
	vus := p.ConcurrentUsers
	if vus < 1 {
		vus = 1
	}
	if vus > 5000 {
		vus = 5000
	}

	var stages []scenarios.Stage
	var total time.Duration

	switch p.TrafficShape {
	case "spike":
		// 5% ramp-up → 30% hold at peak → 65% cooldown; fixed 60s total.
		ramp := 3 * time.Second
		hold := 18 * time.Second
		cool := 39 * time.Second
		total = ramp + hold + cool
		stages = []scenarios.Stage{
			{Duration: ramp, Target: vus},
			{Duration: hold, Target: vus},
			{Duration: cool, Target: 0},
		}
	case "soak":
		// 10% ramp → 85% sustained hold → 5% ramp-down; fixed 5 min total.
		ramp := 30 * time.Second
		hold := 4*time.Minute + time.Second
		down := 29 * time.Second
		total = ramp + hold + down
		stages = []scenarios.Stage{
			{Duration: ramp, Target: vus},
			{Duration: hold, Target: vus},
			{Duration: down, Target: 0},
		}
	default: // "steady"
		// Base duration grows with user count: max(30s, users × 0.3s).
		base := time.Duration(float64(vus)*300) * time.Millisecond
		if base < 30*time.Second {
			base = 30 * time.Second
		}
		ramp := base / 5
		hold := base * 3 / 5
		down := base / 5
		total = ramp + hold + down
		stages = []scenarios.Stage{
			{Duration: ramp, Target: vus},
			{Duration: hold, Target: vus},
			{Duration: down, Target: 0},
		}
	}

	return RampPlan{
		Stages:        stages,
		MaxVUs:        vus,
		TotalDuration: total,
	}
}

// InterpretResult produces a PM-readable verdict from a completed test and its profile.
// Verdict levels:
//   - pass: error_pct < 1% AND p95 ≤ target latency
//   - warn: error_pct < 1% AND p95 ≤ 1.5× target latency (approaching the limit)
//   - fail: high error rate or latency far exceeds target
func InterpretResult(p GuidedProfile, r RunResult) GuidedVerdict {
	targetMs := p.TargetLatencyMs
	if targetMs <= 0 {
		targetMs = 3000
	}
	users := p.ConcurrentUsers
	if users < 1 {
		users = 1
	}

	errPct := r.ErrorPct
	p95Ms := r.P95Ms
	shapeLabel := shapeDisplayName(p.TrafficShape)
	latLabel := latencyDisplayName(targetMs)

	switch {
	case r.Total == 0:
		return GuidedVerdict{
			Level:    "fail",
			Headline: "測試未能完成",
			Detail:   "沒有收到任何回應，請確認 URL 是否正確、服務是否正常運行。",
			NextStep: "先用瀏覽器開啟該 URL 確認服務正常，再重新測試。",
		}

	case errPct < 1 && p95Ms <= int64(targetMs):
		nextUsers := int(float64(users) * 1.5)
		if nextUsers > 5000 {
			nextUsers = 5000
		}
		return GuidedVerdict{
			Level: "pass",
			Headline: fmt.Sprintf(
				"在「%s」場景下，你的服務可以正常服務 %d 位同時使用的用戶",
				shapeLabel, users,
			),
			Detail: fmt.Sprintf(
				"95%% 的用戶在你的目標 %s 內收到回應（p95 %dms），錯誤率 %.2f%%，服務穩定。",
				latLabel, p95Ms, errPct,
			),
			NextStep: fmt.Sprintf(
				"表現良好！建議挑戰 %d 人的場景，找出系統真正的承載上限。",
				nextUsers,
			),
			SuggestedRetry: &GuidedProfile{
				URL:             p.URL,
				Method:          p.Method,
				ConcurrentUsers: nextUsers,
				TargetLatencyMs: p.TargetLatencyMs,
				TrafficShape:    p.TrafficShape,
				ScenarioKind:    p.ScenarioKind,
			},
		}

	case errPct < 1 && p95Ms <= int64(targetMs*3/2):
		nextUsers := int(float64(users) * 0.7)
		if nextUsers < 1 {
			nextUsers = 1
		}
		return GuidedVerdict{
			Level: "warn",
			Headline: fmt.Sprintf(
				"在「%s」場景下，%d 人同時使用時服務接近極限",
				shapeLabel, users,
			),
			Detail: fmt.Sprintf(
				"p95 延遲達到 %dms，超過你設定的目標（%dms），但錯誤率僅 %.2f%%，服務仍在運作中。",
				p95Ms, targetMs, errPct,
			),
			NextStep: fmt.Sprintf(
				"建議先縮小到 %d 人確認穩定，再考慮優化後端或擴容。",
				nextUsers,
			),
			SuggestedRetry: &GuidedProfile{
				URL:             p.URL,
				Method:          p.Method,
				ConcurrentUsers: nextUsers,
				TargetLatencyMs: p.TargetLatencyMs,
				TrafficShape:    p.TrafficShape,
				ScenarioKind:    p.ScenarioKind,
			},
		}

	default:
		nextUsers := int(float64(users) * 0.5)
		if nextUsers < 1 {
			nextUsers = 1
		}
		return GuidedVerdict{
			Level: "fail",
			Headline: fmt.Sprintf(
				"在「%s」場景下，系統無法承受 %d 位用戶同時操作",
				shapeLabel, users,
			),
			Detail: fmt.Sprintf(
				"錯誤率 %.2f%%，p95 延遲 %dms，已超過可接受範圍（目標 %dms）。",
				errPct, p95Ms, targetMs,
			),
			NextStep: fmt.Sprintf(
				"建議先縮小到 %d 人測試，找出系統可穩定服務的邊界，再決定是否擴容或優化。",
				nextUsers,
			),
			SuggestedRetry: &GuidedProfile{
				URL:             p.URL,
				Method:          p.Method,
				ConcurrentUsers: nextUsers,
				TargetLatencyMs: p.TargetLatencyMs,
				TrafficShape:    p.TrafficShape,
				ScenarioKind:    p.ScenarioKind,
			},
		}
	}
}

func shapeDisplayName(shape string) string {
	switch shape {
	case "spike":
		return "突然湧入"
	case "soak":
		return "長時間壓力"
	default:
		return "平穩日常"
	}
}

func latencyDisplayName(ms int) string {
	switch {
	case ms <= 1000:
		return "1 秒"
	case ms <= 3000:
		return "3 秒"
	default:
		return "5 秒"
	}
}

// GuidedDurationLabel returns a human-readable duration string for the wizard preview.
// Mirrors the logic in TranslateProfile so the UI can show an estimate before running.
func GuidedDurationLabel(p GuidedProfile) string {
	plan := TranslateProfile(p)
	s := int(plan.TotalDuration.Seconds())
	if s < 60 {
		return fmt.Sprintf("%d 秒", s)
	}
	m := s / 60
	r := s % 60
	if r == 0 {
		return fmt.Sprintf("%d 分鐘", m)
	}
	return fmt.Sprintf("%d 分 %d 秒", m, r)
}

// GuidedMethod returns the HTTP method inferred from the scenario kind.
func GuidedMethod(kind string) string {
	if strings.EqualFold(kind, "checkout-like") {
		return "POST"
	}
	return "GET"
}

package reporter

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
)

// Interpretation is the single, plain-language reading of a test result shared
// by every output (terminal, JSON, HTML). It deliberately avoids jargon
// (p50/p99/RPS) and pre-renders human-friendly strings so all surfaces show
// identical wording. The dashboard reuses the same thresholds and vocabulary.
type Interpretation struct {
	Level   string `json:"level"`   // "pass" / "warn" / "fail"
	Icon    string `json:"icon"`    // ✓ / ⚠ / ✗
	Verdict string `json:"verdict"` // 整體結論 one-liner

	Speed     SpeedReading     `json:"speed"`
	Stability StabilityReading `json:"stability"`
	Capacity  CapacityReading  `json:"capacity"`

	Bottleneck string `json:"bottleneck,omitempty"`
	OneLiner   string `json:"one_liner"`

	// Diagnosis is the ranked plain-language root-cause reading (why it's slow/
	// unstable and what to do). Populated by Interpret(sum); the scalar-only
	// ReadingsFor path (dashboard live) leaves it nil.
	Diagnosis []Finding `json:"diagnosis,omitempty"`
}

type SpeedReading struct {
	Icon  string `json:"icon"`
	Label string `json:"label"` // 非常快 / 很快 / 普通 / 偏慢 / 很慢
	Note  string `json:"note"`  // 生活化比喻
	Value string `json:"value"` // humanized p99, e.g. "9 毫秒"
}

type StabilityReading struct {
	Icon  string `json:"icon"`
	Label string `json:"label"` // 完美 / 良好 / 有點不穩 / 不穩定
	Note  string `json:"note"`
}

type CapacityReading struct {
	Value string `json:"value"` // requests/sec with thousands separator
	Note  string `json:"note"`
}

// Verdict thresholds shared across all outputs.
const (
	failErrorRatePct = 5.0
	failP99Ms        = 3000
	warnErrorRatePct = 1.0
	warnP99Ms        = 1000
)

// Interpret turns raw metrics into the shared plain-language interpretation.
func Interpret(sum metrics.Summary) Interpretation {
	bottleneck := ""
	if len(sum.Steps) > 1 {
		var name string
		var slowest time.Duration
		for _, s := range sum.Steps {
			if s.P99 > slowest {
				slowest = s.P99
				name = s.Name
			}
		}
		bottleneck = fmt.Sprintf("最花時間的步驟是「%s」（%s內完成），要加快先從這裡下手。", name, humanizeDuration(slowest))
	}
	in := ReadingsFor(sum.P99, sum.ErrorRate(), sum.RPS(), sum.Total, sum.Errors, bottleneck)
	in.Diagnosis = Diagnose(sum)
	return in
}

// ReadingsFor builds an Interpretation from raw scalar metrics. It is the shared
// core behind Interpret() and is reused by the dashboard (live snapshots and
// completed runs) so every surface — terminal, JSON, HTML and browser — speaks
// the exact same plain language. bottleneck may be "" when not applicable.
func ReadingsFor(p99 time.Duration, errRate, rps float64, total, errors int64, bottleneck string) Interpretation {
	p99ms := p99.Milliseconds()

	in := Interpretation{}

	switch {
	case errRate >= failErrorRatePct || p99ms >= failP99Ms:
		in.Level, in.Icon, in.Verdict = "fail", "✗", "網站在這個壓力下出問題了，建議先別上線"
	case errRate >= warnErrorRatePct || p99ms >= warnP99Ms:
		in.Level, in.Icon, in.Verdict = "warn", "⚠", "網站堪用，但有地方需要注意"
	default:
		in.Level, in.Icon, in.Verdict = "pass", "✓", "網站很健康，可以放心上線"
	}

	si, sl, sn := speedReading(p99ms)
	in.Speed = SpeedReading{Icon: si, Label: sl, Note: sn, Value: humanizeDuration(p99)}

	ti, tl, tn := stabilityReading(errRate, total, errors)
	in.Stability = StabilityReading{Icon: ti, Label: tl, Note: tn}

	in.Capacity = CapacityReading{
		Value: humanizeInt(int64(rps + 0.5)),
		Note:  capacityNote(errRate),
	}

	in.Bottleneck = bottleneck
	in.OneLiner = oneLineSummary(p99ms, errRate)
	return in
}

// speedReading maps tail latency (p99, ms) to a human perception of speed.
func speedReading(p99ms int64) (icon, label, note string) {
	switch {
	case p99ms < 100:
		return "⚡", "非常快（幾乎即時）", "低於 0.1 秒，快到使用者根本感覺不到等待"
	case p99ms < 300:
		return "⚡", "很快（流暢）", "使用者幾乎感覺不到延遲"
	case p99ms < 1000:
		return "✓", "普通", "使用者開始能感覺到一點點等待"
	case p99ms < 3000:
		return "⚠", "偏慢", "使用者會明顯覺得卡頓"
	default:
		return "✗", "很慢", "使用者可能等不及就離開了"
	}
}

// stabilityReading describes the failure rate in plain language.
func stabilityReading(errRate float64, total, errors int64) (icon, label, note string) {
	switch {
	case errRate == 0:
		return "✓", "完美", fmt.Sprintf("這次共試了 %s 次，沒有任何一次失敗。", humanizeInt(total))
	case errRate < 1.0:
		return "✓", "良好", fmt.Sprintf("約每 %s 次才有 1 次失敗（%.2f%%），大致穩定。", humanizeInt(int64(100.0/errRate+0.5)), errRate)
	case errRate < 5.0:
		return "⚠", "有點不穩", fmt.Sprintf("%.1f%% 的請求失敗（共 %s 個），建議查一下原因。", errRate, humanizeInt(errors))
	default:
		return "✗", "不穩定", fmt.Sprintf("%.1f%% 的請求失敗（共 %s 個），服務開始撐不住了。", errRate, humanizeInt(errors))
	}
}

func capacityNote(errRate float64) string {
	switch {
	case errRate == 0:
		return "錯誤率 0，代表這個壓力下軟體還有餘裕。"
	case errRate < 5.0:
		return "已經開始出現少量失敗，可能接近能負荷的上限。"
	default:
		return "大量請求失敗，代表已經超過能負荷的量。"
	}
}

// oneLineSummary combines speed and stability into a single takeaway.
func oneLineSummary(p99ms int64, errRate float64) string {
	fast := p99ms < warnP99Ms
	stable := errRate < warnErrorRatePct
	switch {
	case fast && stable:
		return "整體來說，網站又快又穩，可以放心。"
	case !fast && stable:
		return "網站很穩定，但反應偏慢，使用者體驗會打折扣。"
	case fast && !stable:
		return "網站反應很快，但有請求失敗，建議先解決穩定度問題。"
	default:
		return "網站又慢又不穩，建議先處理問題再上線。"
	}
}

// humanizeDuration renders a duration in 毫秒/秒 for non-technical readers.
func humanizeDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1000 {
		return fmt.Sprintf("%d 毫秒", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

// humanizeInt formats an integer with thousands separators (e.g. 20046 → 20,046).
func humanizeInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

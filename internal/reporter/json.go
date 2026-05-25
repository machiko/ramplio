package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ramplio/ramplio/internal/metrics"
)

// Report is the serializable form of a test run summary.
// All latency values are in milliseconds for readability.
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Total       int64        `json:"total"`
	Errors      int64        `json:"errors"`
	ErrorRate   float64      `json:"error_rate_pct"`
	WallTimeSec float64      `json:"wall_time_s"`
	RPS         float64      `json:"rps"`
	BytesIn     int64        `json:"bytes_in"`
	Latency     LatencyMs    `json:"latency"`
	Steps       []StepReport `json:"steps,omitempty"`
	Verdict     Verdict      `json:"verdict"`
}

// Verdict is a plain-language interpretation of test results for non-technical readers.
type Verdict struct {
	Level           string `json:"level"`                      // "pass", "warn", "fail"
	Headline        string `json:"headline"`
	SpeedLine       string `json:"speed"`
	ReliabilityLine string `json:"reliability"`
	BottleneckLine  string `json:"bottleneck,omitempty"`
}

type LatencyMs struct {
	MinMs  int64 `json:"min_ms"`
	MeanMs int64 `json:"mean_ms"`
	P50Ms  int64 `json:"p50_ms"`
	P90Ms  int64 `json:"p90_ms"`
	P95Ms  int64 `json:"p95_ms"`
	P99Ms  int64 `json:"p99_ms"`
	MaxMs  int64 `json:"max_ms"`
}

// StepReport holds per-step metrics in milliseconds for reporting.
type StepReport struct {
	Name      string  `json:"name"`
	Total     int64   `json:"total"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate_pct"`
	P50Ms     int64   `json:"p50_ms"`
	P90Ms     int64   `json:"p90_ms"`
	P99Ms     int64   `json:"p99_ms"`
}

func computeVerdict(r Report) Verdict {
	level := "pass"
	headline := "Your API handled the load well."
	if r.ErrorRate >= 5.0 || r.Latency.P99Ms >= 1000 {
		level = "fail"
		headline = "Your API struggled under this load."
	} else if r.ErrorRate >= 1.0 || r.Latency.P99Ms >= 500 {
		level = "warn"
		headline = "Your API performed acceptably, with some concerns."
	}

	speedLine := fmt.Sprintf(
		"Half your users got a response in %dms. 99%% received one within %dms.",
		r.Latency.P50Ms, r.Latency.P99Ms,
	)

	var reliabilityLine string
	switch {
	case r.ErrorRate == 0:
		reliabilityLine = "All requests succeeded — no errors."
	case r.ErrorRate < 0.1:
		reliabilityLine = fmt.Sprintf("Roughly 1 in %d requests failed (%.2f%%).", int(100.0/r.ErrorRate), r.ErrorRate)
	default:
		reliabilityLine = fmt.Sprintf("%.1f%% of requests failed (%d errors).", r.ErrorRate, r.Errors)
	}

	var bottleneckLine string
	if len(r.Steps) > 1 {
		var slowest StepReport
		for i, s := range r.Steps {
			if i == 0 || s.P99Ms > slowest.P99Ms {
				slowest = s
			}
		}
		bottleneckLine = fmt.Sprintf(`Slowest step: "%s" (%dms p99).`, slowest.Name, slowest.P99Ms)
	}

	return Verdict{
		Level:           level,
		Headline:        headline,
		SpeedLine:       speedLine,
		ReliabilityLine: reliabilityLine,
		BottleneckLine:  bottleneckLine,
	}
}

// SummaryToReport converts a metrics.Summary to a serializable Report.
func SummaryToReport(sum metrics.Summary) Report {
	r := Report{
		GeneratedAt: time.Now().UTC(),
		Total:       sum.Total,
		Errors:      sum.Errors,
		ErrorRate:   sum.ErrorRate(),
		WallTimeSec: sum.WallTime.Seconds(),
		RPS:         sum.RPS(),
		BytesIn:     sum.BytesIn,
		Latency: LatencyMs{
			MinMs:  sum.MinLatency.Milliseconds(),
			MeanMs: sum.MeanLatency().Milliseconds(),
			P50Ms:  sum.P50.Milliseconds(),
			P90Ms:  sum.P90.Milliseconds(),
			P95Ms:  sum.P95.Milliseconds(),
			P99Ms:  sum.P99.Milliseconds(),
			MaxMs:  sum.MaxLatency.Milliseconds(),
		},
	}
	for _, s := range sum.Steps {
		errRate := float64(0)
		if s.Total > 0 {
			errRate = float64(s.Errors) / float64(s.Total) * 100
		}
		r.Steps = append(r.Steps, StepReport{
			Name:      s.Name,
			Total:     s.Total,
			Errors:    s.Errors,
			ErrorRate: errRate,
			P50Ms:     s.P50.Milliseconds(),
			P90Ms:     s.P90.Milliseconds(),
			P99Ms:     s.P99.Milliseconds(),
		})
	}
	r.Verdict = computeVerdict(r)
	return r
}

// WriteJSON encodes a Summary as JSON into w.
func WriteJSON(w io.Writer, sum metrics.Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(SummaryToReport(sum))
}

// ReadJSON decodes a Report from r.
func ReadJSON(r io.Reader) (Report, error) {
	var rep Report
	err := json.NewDecoder(r).Decode(&rep)
	return rep, err
}

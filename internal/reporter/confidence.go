package reporter

import (
	"fmt"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

// ConfidenceReading is a plain-language judgement of how much to trust the
// numbers, based on the *generator's* own health during the run. A load tester
// that drops samples or stalls for GC is distorting the very thing it measures;
// surfacing that is what separates a credible result from a flattering one.
type ConfidenceReading struct {
	Level string `json:"level"` // "high" / "medium" / "low"
	Icon  string `json:"icon"`  // ✓ / ⚠ / ✗
	Note  string `json:"note"`
}

// Measurement-confidence thresholds.
const (
	droppedConcernRatio = 0.01 // >=1% of samples dropped → don't fully trust the numbers
	gcPauseWarnPct      = 2.0  // generator GC pause >= 2% of wall time → mild concern
	gcPauseFailPct      = 5.0  // >= 5% → results likely inflated by the tool itself
	// generatorShortfallRatio: dispatched / planned below this → the declared
	// rate was not delivered; the report must not read as if it had been.
	generatorShortfallRatio = 0.95
	// generatorShortfallMinGap: a short or low-rate run plans only a handful of
	// requests, and the schedule is a continuous integral while sends are whole
	// — one request short of 18 is already "94%". Flag only a real gap.
	generatorShortfallMinGap = 2
)

// MeasurementConfidence reads the generator self-health signals on the Summary
// (dropped samples, GC pause) and returns how trustworthy the measurement is.
func MeasurementConfidence(sum metrics.Summary) ConfidenceReading {
	dropRatio := 0.0
	if considered := sum.Total + sum.DroppedSamples; considered > 0 {
		dropRatio = float64(sum.DroppedSamples) / float64(considered)
	}
	gcPct := 0.0
	if sum.WallTime > 0 {
		gcPct = float64(sum.GeneratorGCPause) / float64(sum.WallTime) * 100
	}

	var reading ConfidenceReading
	switch {
	case dropRatio >= droppedConcernRatio || gcPct >= gcPauseFailPct:
		reading = ConfidenceReading{
			Level: "low", Icon: "✗",
			Note: "偏低：量測過程中工具本身來不及處理（丟了部分樣本或 GC 暫停偏多），數字可能偏樂觀，建議降載後重測。",
		}
	case sum.DroppedSamples > 0 || gcPct >= gcPauseWarnPct:
		reading = ConfidenceReading{
			Level: "medium", Icon: "⚠",
			Note: "中等：大致可靠，但產生器略有負擔（少量丟樣本或 GC 暫停），最尾端的數字參考即可。",
		}
	default:
		reading = ConfidenceReading{
			Level: "high", Icon: "✓",
			Note: "高：量測過程中產生器沒有丟樣本、GC 干擾也很低，數字可信。",
		}
	}

	// Reaching the rate-mode worker ceiling means the generator itself may be the
	// bottleneck, so any queueing delay can't be pinned solely on the target. This
	// is an attribution caveat, not a defect — downgrade a clean "high" to medium
	// and explain, but never override an already-low reading.
	if sum.GeneratorWorkerCapHit {
		reading = downgradeWithNote(reading, "產生器達到 worker 上限，壓力下延遲可能部分來自產生器自身而非目標；考慮分散到多節點重測以釐清。")
	}

	// A rate-mode run that sent well under its schedule did not apply the load
	// the user asked for; every number on the report describes the smaller load.
	// The comparison uses what workers took up, not the recorded samples: a
	// request still in flight at the end, a skipped `if` step or a `loop`
	// repeat would otherwise read as (or mask) a generator shortfall.
	if planned := sum.GeneratorPlannedRequests; planned > 0 {
		sent := sum.GeneratorDispatchedRequests
		if ratio := float64(sent) / float64(planned); ratio < generatorShortfallRatio && planned-sent > generatorShortfallMinGap {
			reading = downgradeWithNote(reading, fmt.Sprintf(
				"產生器只送出排程的 %.0f%%（排程 %d 筆、實際 %d 筆）：報告反映的是實際送出的負載，不是設定的速率。若同時達到 worker 上限，是目標追不上；否則請降低速率或分散到多節點。",
				ratio*100, planned, sent))
		}
	}
	return reading
}

// downgradeWithNote turns a clean "high" reading into medium with note as the
// reason, or appends note to an already-degraded reading — never overriding a
// low with something milder.
func downgradeWithNote(r ConfidenceReading, note string) ConfidenceReading {
	if r.Level == "high" {
		return ConfidenceReading{Level: "medium", Icon: "⚠", Note: "中等：" + note}
	}
	r.Note = r.Note + " " + note
	return r
}

// generatorGCPausePct returns the generator's GC stop-the-world pause as a percent
// of wall time, used by the diagnosis to flag tool-induced distortion.
func generatorGCPausePct(sum metrics.Summary) float64 {
	if sum.WallTime <= 0 {
		return 0
	}
	return float64(sum.GeneratorGCPause) / float64(sum.WallTime) * 100
}

// gcPauseEvidence renders the GC pause signal for a finding.
func gcPauseEvidence(sum metrics.Summary) string {
	return fmt.Sprintf("產生器 GC 暫停累計 %s，約佔測試時長 %.1f%%。",
		humanizeDuration(sum.GeneratorGCPause), generatorGCPausePct(sum))
}

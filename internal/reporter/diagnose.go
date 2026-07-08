package reporter

import (
	"fmt"
	"sort"

	"github.com/machiko/ramplio/v3/internal/metrics"
)

// Finding is a single plain-language diagnostic insight aimed at non-technical
// readers (PMs / decision makers): what's wrong, why it likely happens, and what
// to do next. Raw numbers stay in Evidence so the headline reads in human terms.
type Finding struct {
	Severity string `json:"severity"` // "critical" / "warn" / "info" / "good"
	Icon     string `json:"icon"`     // ✗ / ⚠ / ℹ / ✓
	Title    string `json:"title"`    // 白話症狀
	Cause    string `json:"cause"`    // 白話可能原因
	Action   string `json:"action"`   // 建議下一步
	Evidence string `json:"evidence"` // 數據佐證
}

// Diagnosis thresholds. Named constants keep the rules tunable and free of magic
// numbers. Verdict thresholds (warn/failP99Ms, warn/failErrorRatePct) are reused
// from interpret.go where they apply.
const (
	tailRatioThreshold  = 3.0 // p99 >= 3× p50 → 尾端延遲
	tailFloorMs         = 500 // 且 p99 >= 500ms 才有意義，避免快測試誤報
	stepErrorMultiplier = 2.0 // 某步驟錯誤率 >= 2× 整體
	dominanceRatio      = 1.5 // 最慢步驟/群組 p99 >= 1.5× 次慢
	coOmissionRatio     = 2.0 // 修正後 p99 >= 2× 服務 p99 → 送達速率超過消化速度
	coOmissionFloorMs   = 200 // 且差距 >= 200ms 才提報，避免噪音
)

// severityRank orders findings for display: most urgent first.
var severityRank = map[string]int{"critical": 0, "warn": 1, "info": 2, "good": 3}

// Diagnose turns a completed run's aggregate metrics into a ranked list of
// plain-language findings. It reads only the Summary (no time-series), so it adds
// no new measurement infrastructure. Returns a single "good" finding when nothing
// notable is detected.
func Diagnose(sum metrics.Summary) []Finding {
	var findings []Finding

	errRate := sum.ErrorRate()
	p50ms := sum.P50.Milliseconds()
	p99ms := sum.P99.Milliseconds()

	// 0. 失敗歸因（為什麼會失敗）— 最高優先，直接點名主因並給下一步。
	if cause, ok := failureCauseFinding(sum); ok {
		findings = append(findings, cause)
	}

	// 0b. 協調遺漏：送達速率超過系統消化速度（rate 模式）。伺服器處理本身不慢，
	//     但請求排隊，使用者實際等待遠高於服務時間——這是 closed-loop 工具會漏掉的。
	if coOmissionSignificant(sum) {
		findings = append(findings, Finding{
			Severity: "critical",
			Icon:     "✗",
			Title:    "請求速率超過系統能消化的速度",
			Cause:    "伺服器每次處理其實不慢，但請求送達的速度比它消化的速度快，多出來的請求只能排隊，使用者實際等待時間因此遠高於伺服器處理時間。",
			Action:   "降低目標 RPS，或擴充後端處理能力；目前這個速率系統已經追不上。",
			Evidence: fmt.Sprintf("伺服器每次處理只要 %s，但壓力下使用者實際要等 %s。", humanizeDuration(sum.P99), humanizeDuration(sum.CorrectedP99)),
		})
	}

	// 1. 整體過載 — 但若失敗其實是「連不上目標」（DNS/連線被拒/憑證），那不是
	//    過載，避免誤導；此時改由上面的失敗歸因說明。
	if (errRate >= failErrorRatePct || p99ms >= failP99Ms) && !reachabilityDominates(sum) {
		findings = append(findings, Finding{
			Severity: "critical",
			Icon:     "✗",
			Title:    "服務在這個壓力下已經超出負荷",
			Cause:    "大量請求變慢或失敗，代表目前的流量已超過服務能穩定處理的量。",
			Action:   "先降低同時使用人數或擴充後端資源，再重新測試找出能穩定服務的上限。",
			Evidence: fmt.Sprintf("錯誤率 %.1f%%，最慢的 1%% 要等 %s。", errRate, humanizeDuration(sum.P99)),
		})
	}

	// 2. 尾端延遲
	if p50ms > 0 && p99ms >= tailFloorMs && float64(p99ms) >= tailRatioThreshold*float64(p50ms) {
		findings = append(findings, Finding{
			Severity: "warn",
			Icon:     "⚠",
			Title:    "最慢的少數人體驗差很多（尾端延遲）",
			Cause:    "多數人很快，但最慢的 1% 慢很多。這通常是偶發的資料庫慢查詢、垃圾回收（GC）或連線排隊造成的。",
			Action:   "請工程師檢查最慢那批請求發生時，系統是不是在跑慢查詢或回收資源。",
			Evidence: fmt.Sprintf("多數人 %s 內完成，但最慢的 1%% 要等到 %s。", humanizeDuration(sum.P50), humanizeDuration(sum.P99)),
		})
	}

	// 3. 錯誤集中於某步驟
	if len(sum.Steps) > 0 && sum.Errors > 0 {
		var worst metrics.StepSummary
		var worstErr float64
		for _, st := range sum.Steps {
			if st.Total == 0 {
				continue
			}
			stepErr := float64(st.Errors) / float64(st.Total) * 100
			if stepErr > worstErr {
				worstErr = stepErr
				worst = st
			}
		}
		if worstErr >= warnErrorRatePct && worstErr >= errRate*stepErrorMultiplier {
			findings = append(findings, Finding{
				Severity: "warn",
				Icon:     "⚠",
				Title:    fmt.Sprintf("問題集中在「%s」這一步", worst.Name),
				Cause:    "這一步的失敗率明顯高於其他步驟，代表瓶頸很可能在這支 API，而不是整個系統。",
				Action:   fmt.Sprintf("先單獨檢查「%s」這支 API 的錯誤原因。", worst.Name),
				Evidence: fmt.Sprintf("這一步錯誤率 %.1f%%，整體只有 %.1f%%。", worstErr, errRate),
			})
		}
	}

	// 4. 瓶頸步驟
	if len(sum.Steps) > 1 {
		slowest, second := twoSlowestSteps(sum.Steps)
		if second.P99 > 0 && float64(slowest.P99) >= dominanceRatio*float64(second.P99) {
			findings = append(findings, Finding{
				Severity: "info",
				Icon:     "ℹ",
				Title:    fmt.Sprintf("整個流程的時間主要卡在「%s」這一步", slowest.Name),
				Cause:    "這一步明顯比其他步驟慢，是拖慢整體最主要的環節。",
				Action:   fmt.Sprintf("想加快整體速度，先從優化「%s」這一步最有效。", slowest.Name),
				Evidence: fmt.Sprintf("這一步最慢的 1%% 要 %s，下一個最慢的步驟只要 %s。", humanizeDuration(slowest.P99), humanizeDuration(second.P99)),
			})
		}
	}

	// 5. 群組分化
	if len(sum.Groups) > 1 {
		slowest, second := twoSlowestGroups(sum.Groups)
		if second.P99 > 0 && float64(slowest.P99) >= dominanceRatio*float64(second.P99) {
			findings = append(findings, Finding{
				Severity: "info",
				Icon:     "ℹ",
				Title:    fmt.Sprintf("「%s」這類功能明顯比其他慢", slowest.Name),
				Cause:    "不同功能的反應速度落差很大，代表慢的那一類有獨立的效能問題。",
				Action:   fmt.Sprintf("把「%s」這類功能單獨拉出來檢查。", slowest.Name),
				Evidence: fmt.Sprintf("「%s」最慢的 1%% 要 %s，其他功能只要 %s。", slowest.Name, humanizeDuration(slowest.P99), humanizeDuration(second.P99)),
			})
		}
	}

	// 6. 量測不完整
	if sum.DroppedSamples > 0 {
		findings = append(findings, Finding{
			Severity: "warn",
			Icon:     "⚠",
			Title:    "有部分數據沒收集到，結論可能偏樂觀",
			Cause:    "負載太高時來不及記錄所有請求，被丟棄的那部分很可能正是表現較差的請求。",
			Action:   "降低同時使用人數後重測一次，數據會更完整可信。",
			Evidence: fmt.Sprintf("這次有 %s 筆數據因負載過高被丟棄。", humanizeInt(sum.DroppedSamples)),
		})
	}

	// 6b. 產生器自身 GC 干擾 — 工具本身停頓會灌水到量測延遲，數字偏樂觀/失真。
	if generatorGCPausePct(sum) >= gcPauseWarnPct {
		findings = append(findings, Finding{
			Severity: "warn",
			Icon:     "⚠",
			Title:    "量測可能被產生器自身的 GC 干擾",
			Cause:    "壓測工具自己在測試期間頻繁回收記憶體（GC）並短暫停頓，這段停頓會被算進量到的延遲，讓數字失真。",
			Action:   "在記憶體更充裕的機器上重測，或分散到多台節點降低單機負擔，數據會更乾淨。",
			Evidence: gcPauseEvidence(sum),
		})
	}

	// 7. 健康（無命中）
	if len(findings) == 0 {
		findings = append(findings, Finding{
			Severity: "good",
			Icon:     "✓",
			Title:    "找不到明顯的弱點，目前表現健康",
			Cause:    "反應速度、穩定度與各步驟表現都在正常範圍。",
			Action:   "可以維持目前設定，或提高負載繼續探索系統的上限。",
		})
	}

	sort.SliceStable(findings, func(i, j int) bool {
		return severityRank[findings[i].Severity] < severityRank[findings[j].Severity]
	})
	return findings
}

// coOmissionSignificant reports whether the corrected (rate-mode) p99 exceeds the
// service p99 by enough to flag that requests are queueing — the generator's
// target rate is outrunning what the system can process.
func coOmissionSignificant(sum metrics.Summary) bool {
	if !sum.HasCorrected {
		return false
	}
	gap := sum.CorrectedP99 - sum.P99
	return gap.Milliseconds() >= coOmissionFloorMs &&
		float64(sum.CorrectedP99) >= coOmissionRatio*float64(sum.P99)
}

// twoSlowestSteps returns the slowest and second-slowest steps by p99.
func twoSlowestSteps(steps []metrics.StepSummary) (slowest, second metrics.StepSummary) {
	for _, s := range steps {
		switch {
		case s.P99 > slowest.P99:
			second = slowest
			slowest = s
		case s.P99 > second.P99:
			second = s
		}
	}
	return
}

// twoSlowestGroups returns the slowest and second-slowest groups by p99.
func twoSlowestGroups(groups []metrics.GroupSummary) (slowest, second metrics.GroupSummary) {
	for _, g := range groups {
		switch {
		case g.P99 > slowest.P99:
			second = slowest
			slowest = g
		case g.P99 > second.P99:
			second = g
		}
	}
	return
}

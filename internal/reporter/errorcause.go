package reporter

import (
	"fmt"

	"github.com/ramplio/ramplio/internal/metrics"
)

// errorCopy holds the plain-language wording for one failure cause. Like the
// rest of interpret.go/diagnose.go, the text targets non-technical readers and
// always pairs a likely cause with a concrete next step. Label is the short tag
// used in the per-cause breakdown table.
type errorCopy struct {
	Label  string
	Title  string
	Cause  string
	Action string
}

// errorCopyByKind is the single source of plain-language wording for every
// failure cause, shared by the terminal, JSON, HTML and dashboard surfaces.
var errorCopyByKind = map[metrics.ErrorKind]errorCopy{
	metrics.ErrKindDNS: {
		Label:  "網域解析失敗",
		Title:  "連不上目標：網域找不到",
		Cause:  "請求在連線階段就失敗，常見原因是網址拼錯、網域不存在，或 DNS 解析不到。",
		Action: "先確認網址拼字正確、網域確實存在（可在瀏覽器開開看），再重新測試。",
	},
	metrics.ErrKindConnRefused: {
		Label:  "連線被拒",
		Title:  "連不上目標：連線被拒絕",
		Cause:  "目標位址有回應卻拒絕連線，通常代表服務沒啟動、埠號（port）打錯，或被防火牆擋住。",
		Action: "確認目標服務正在執行、網址的埠號正確，且沒有防火牆阻擋，再重新測試。",
	},
	metrics.ErrKindConnReset: {
		Label:  "連線中斷",
		Title:  "連線中途被切斷",
		Cause:  "連線已建立卻被中途切斷，常見於伺服器或負載平衡器在高負載下主動關閉連線。",
		Action: "降低同時使用人數再測；若仍發生，檢查伺服器與負載平衡器的連線數上限設定。",
	},
	metrics.ErrKindTimeout: {
		Label:  "逾時",
		Title:  "請求逾時：目標沒有在時限內回應",
		Cause:  "請求送出後等不到回應，可能是目標太慢、網路不通，或服務已被壓垮無法回應。",
		Action: "先用較低人數確認服務本身能正常回應；若低負載正常、加壓才逾時，代表已接近承受上限。",
	},
	metrics.ErrKindTLS: {
		Label:  "TLS/憑證",
		Title:  "連不上目標：HTTPS 憑證有問題",
		Cause:  "連線在 TLS 加密握手階段失敗，常見於憑證過期、自簽憑證，或網域與憑證不符。",
		Action: "若是測試環境的自簽憑證，可加 --tls-skip-verify 暫時略過驗證（勿用於正式環境）；否則請修正伺服器憑證。",
	},
	metrics.ErrKindHTTP4xx: {
		Label:  "HTTP 4xx",
		Title:  "大多數請求被拒絕（4xx）",
		Cause:  "伺服器收到請求卻回了 4xx，常見是需要登入（401）、權限不足（403），或網址路徑錯誤（404）。",
		Action: "檢查認證設定（token/cookie）與請求網址是否正確；這類錯誤通常是設定問題，不是效能問題。",
	},
	metrics.ErrKindHTTP5xx: {
		Label:  "HTTP 5xx",
		Title:  "伺服器端大量出錯（5xx）",
		Cause:  "伺服器收到請求但自己處理失敗。在壓測下，這通常代表後端被壓垮、相依服務或資料庫撐不住。",
		Action: "請工程師查伺服器錯誤日誌找出 5xx 根因，並降低負載找出能穩定服務的上限。",
	},
	metrics.ErrKindAssertion: {
		Label:  "斷言失敗",
		Title:  "請求成功，但回應內容不如預期（斷言失敗）",
		Cause:  "連線與狀態碼正常，但回應內容沒通過你設定的斷言，可能是回應格式變了，或斷言條件太嚴。",
		Action: "比對實際回應與斷言條件，確認是服務回傳變了，還是斷言需要調整。",
	},
	metrics.ErrKindOther: {
		Label:  "其他",
		Title:  "出現無法歸類的連線錯誤",
		Cause:  "請求失敗，但原因無法自動判斷。",
		Action: "用較低人數重測觀察是否重現，並檢查網路與目標服務狀態。",
	},
}

// ExplainErrorKind returns the plain-language title, likely cause and suggested
// action for a failure kind. ok is false for ErrKindNone or unknown kinds. Used
// by the pre-flight check to explain why a target is unreachable before a full
// test wastes the user's time.
func ExplainErrorKind(kind metrics.ErrorKind) (title, cause, action string, ok bool) {
	ec, found := errorCopyByKind[kind]
	if !found {
		return "", "", "", false
	}
	return ec.Title, ec.Cause, ec.Action, true
}

// IsReachabilityFailure reports whether a kind means the target could not be
// reached at all (DNS / connection refused / TLS) — the cases where aborting
// before a full run is the kind thing to do.
func IsReachabilityFailure(kind metrics.ErrorKind) bool {
	return isReachabilityFailure(kind)
}

// reachabilityFailures never indicate overload — they mean the test could not
// reach a healthy service in the first place. When one of these dominates, the
// generic "超出負荷" finding is suppressed in favour of the specific cause.
func isReachabilityFailure(k metrics.ErrorKind) bool {
	switch k {
	case metrics.ErrKindDNS, metrics.ErrKindConnRefused, metrics.ErrKindTLS:
		return true
	}
	return false
}

// failureCauseFinding builds the top-priority plain-language finding that names
// the dominant reason requests failed. Returns ok=false when there are too few
// errors to be worth explaining, so callers can skip it.
func failureCauseFinding(sum metrics.Summary) (Finding, bool) {
	errRate := sum.ErrorRate()
	if sum.Errors == 0 || errRate < warnErrorRatePct {
		return Finding{}, false
	}
	kind, _, share := metrics.DominantErrorKind(sum.ErrorBreakdown, sum.Errors)
	if kind == metrics.ErrKindNone {
		return Finding{}, false
	}
	ec, ok := errorCopyByKind[kind]
	if !ok {
		return Finding{}, false
	}

	severity, icon := "warn", "⚠"
	if errRate >= failErrorRatePct {
		severity, icon = "critical", "✗"
	}

	return Finding{
		Severity: severity,
		Icon:     icon,
		Title:    ec.Title,
		Cause:    ec.Cause,
		Action:   ec.Action,
		Evidence: fmt.Sprintf("整體失敗率 %.1f%%，其中 %.0f%% 屬於「%s」（共 %s 個失敗）。",
			errRate, share*100, ec.Label, humanizeInt(sum.Errors)),
	}, true
}

// reachabilityDominates reports whether a connection/config-level failure makes
// up the majority of errors — used to suppress the misleading overload finding.
func reachabilityDominates(sum metrics.Summary) bool {
	if sum.Errors == 0 {
		return false
	}
	kind, _, share := metrics.DominantErrorKind(sum.ErrorBreakdown, sum.Errors)
	return isReachabilityFailure(kind) && share >= 0.5
}

// ErrorBreakdownRows returns the per-cause failure counts in stable display
// order, each with its short label, for tabular rendering. Empty when no errors.
func ErrorBreakdownRows(sum metrics.Summary) []ErrorBreakdownRow {
	if len(sum.ErrorBreakdown) == 0 {
		return nil
	}
	var rows []ErrorBreakdownRow
	for _, k := range metrics.DisplayOrder {
		c := sum.ErrorBreakdown[k]
		if c == 0 {
			continue
		}
		label := string(k)
		if ec, ok := errorCopyByKind[k]; ok {
			label = ec.Label
		}
		share := 0.0
		if sum.Errors > 0 {
			share = float64(c) / float64(sum.Errors) * 100
		}
		rows = append(rows, ErrorBreakdownRow{Label: label, Count: c, SharePct: share})
	}
	return rows
}

// ErrorBreakdownRow is one cause's tally for tabular display.
type ErrorBreakdownRow struct {
	Label    string  `json:"label"`
	Count    int64   `json:"count"`
	SharePct float64 `json:"share_pct"`
}

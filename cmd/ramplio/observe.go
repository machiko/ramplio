package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/machiko/ramplio/v3/internal/observe"
)

// parseObserveDSN 解析 --observe 的 DSN。
// 支援:jaeger://host:16686?service=<名稱>、tempo://host:3200?service=<名稱>
// (jaegers:// / tempos:// 走 https)。
func parseObserveDSN(dsn string) (observe.TraceSource, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("--observe: 無效的 DSN: %w", err)
	}
	// scheme 先驗:兩個錯誤同時存在時,「不支援的 scheme」才是根本原因,
	// 先報缺 service 會誤導使用者補參數後再撞一次牆。
	scheme := "http"
	var newSource func(baseURL, service string) (observe.TraceSource, error)
	switch u.Scheme {
	case "jaegers":
		scheme = "https"
		fallthrough
	case "jaeger":
		newSource = func(b, s string) (observe.TraceSource, error) { return observe.NewJaegerSource(b, s) }
	case "tempos":
		scheme = "https"
		fallthrough
	case "tempo":
		newSource = func(b, s string) (observe.TraceSource, error) { return observe.NewTempoSource(b, s) }
	default:
		return nil, fmt.Errorf("--observe: 不支援的 scheme %q——目前支援 jaeger:// 與 tempo://(host?service=名稱)", u.Scheme)
	}
	service := u.Query().Get("service")
	if service == "" {
		return nil, fmt.Errorf("--observe: 缺 service 參數(例:jaeger://localhost:16686?service=checkout)")
	}
	return newSource(scheme+"://"+u.Host, service)
}

// observeWindows 由 rate 模式負載輪廓推導比較窗口:
// 基準窗 = 爬升前半(負載 0→50%,最接近「健康狀態」的取樣)
// 臨界窗 = 持平段(全程滿載)
// 這是啟發式:爬升後半已接近目標負載,不當基準。
func observeWindows(start time.Time, rampDur, holdDur time.Duration) (baseStart, baseEnd, stressStart, stressEnd time.Time) {
	baseStart = start
	baseEnd = start.Add(rampDur / 2)
	stressStart = start.Add(rampDur)
	stressEnd = stressStart.Add(holdDur)
	return
}

// maxDisplayedDegradations 是白話段落顯示的退化項上限(Top1 + 兩個其次)。
const maxDisplayedDegradations = 3

// renderObservation 輸出目標系統的白話瓶頸段落。
// truncated 表示任一窗的 trace 取樣達到上限,樣本可能不完整。
func renderObservation(w io.Writer, a observe.Analysis, truncated bool) {
	fmt.Fprintln(w, "\n目標系統觀測(trace 關聯)")
	fmt.Fprintln(w, "──────────────────────────")

	if truncated {
		fmt.Fprintln(w, "  ⚠ trace 取樣達到查詢上限,樣本可能不完整——以下結論僅基於已取得的部分。")
	}

	switch a.Status {
	case observe.StatusInsufficient:
		fmt.Fprintln(w, "  結論:關聯不足——trace 樣本不夠,不猜測瓶頸。")
	case observe.StatusNoCulprit:
		fmt.Fprintln(w, "  結論:未發現單點瓶頸。")
	case observe.StatusOK:
		top := a.Top[0]
		fmt.Fprintf(w, "  結論:瓶頸指向 %s——p95 從 %s 惡化到 %s(%.1f 倍)。\n",
			top.Operation, formatDur(top.BaselineP95), formatDur(top.StressedP95), top.Factor)
		rest := a.Top[1:]
		if len(rest) > maxDisplayedDegradations-1 {
			rest = rest[:maxDisplayedDegradations-1]
		}
		for _, d := range rest {
			fmt.Fprintf(w, "  其次:%s %s → %s(%.1f 倍)\n",
				d.Operation, formatDur(d.BaselineP95), formatDur(d.StressedP95), d.Factor)
		}
	}

	// 多句 Reason 分行呈現(句號斷句),避免長字串黏成一段。
	if a.Reason != "" {
		for _, sentence := range strings.SplitAfter(a.Reason, "。") {
			if strings.TrimSpace(sentence) == "" {
				continue
			}
			fmt.Fprintf(w, "  %s\n", sentence)
		}
	}

	// 排除可見化不可在呈現層失守:名單必須印出,不能只說「見 ExcludedOps」。
	if len(a.ExcludedOps) > 0 {
		fmt.Fprintf(w, "  未納入比較:%s\n", strings.Join(a.ExcludedOps, "、"))
	}

	// 窗口啟發式的已知偏誤,誠實揭露:基準窗取自爬升前段,
	// 目標系統的冷啟動(連線建立/JIT/快取預熱)會墊高基準,倍率可能被低估。
	fmt.Fprintln(w, "  註:基準窗取自爬升前段,若目標系統有明顯冷啟動效應,退化倍率可能被低估。")
}

func formatDur(d time.Duration) string {
	return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
}

// runObservation 執行完整觀測流程。任何失敗只警告不中斷:
// 觀測是壓測結果的補充,不可污染主流程 exit code(比照 sink 慣例)。
func runObservation(outW, errW io.Writer, src observe.TraceSource, start time.Time, rampDur, holdDur time.Duration) {
	if holdDur <= 0 {
		fmt.Fprintln(errW, "warning: --observe 需要有持平段(duration 過短),略過觀測")
		return
	}
	baseStart, baseEnd, stressStart, stressEnd := observeWindows(start, rampDur, holdDur)

	// 兩窗各自獨立逾時:基準窗查詢耗時不可壓縮臨界窗的時間預算。
	fetch := func(s, e time.Time) (observe.FetchResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return src.FetchSpans(ctx, s, e)
	}
	baseRes, err := fetch(baseStart, baseEnd)
	if err != nil {
		fmt.Fprintf(errW, "warning: 拉取基準窗 trace 失敗,略過觀測: %v\n", err)
		return
	}
	stressRes, err := fetch(stressStart, stressEnd)
	if err != nil {
		fmt.Fprintf(errW, "warning: 拉取臨界窗 trace 失敗,略過觀測: %v\n", err)
		return
	}

	analysis := observe.AnalyzeWindows(baseRes.Spans, stressRes.Spans, observe.DefaultAnalyzeConfig())
	renderObservation(outW, analysis, baseRes.Truncated || stressRes.Truncated)
}

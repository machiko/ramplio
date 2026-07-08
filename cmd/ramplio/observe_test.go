package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/observe"
)

func TestParseObserveDSN(t *testing.T) {
	src, err := parseObserveDSN("jaeger://localhost:16686?service=checkout")
	if err != nil {
		t.Fatalf("合法 DSN 應成功: %v", err)
	}
	if src == nil {
		t.Fatal("應回傳 TraceSource")
	}

	if _, err := parseObserveDSN("jaeger://localhost:16686"); err == nil {
		t.Fatal("缺 service 參數應報錯(Jaeger 查詢必要)")
	}
	if _, err := parseObserveDSN("tempo://x?service=y"); err == nil {
		t.Fatal("不支援的 scheme 應報錯並列出支援清單")
	}
}

// 觀測窗口出自 rate 模式負載輪廓:爬升前半(低負載)當基準、持平段當臨界。
func TestObserveWindowsFromProfile(t *testing.T) {
	start := time.UnixMicro(1700000000000000)
	rampDur, holdDur := 2*time.Second, 4*time.Second

	baseStart, baseEnd, stressStart, stressEnd := observeWindows(start, rampDur, holdDur)

	if !baseStart.Equal(start) || !baseEnd.Equal(start.Add(time.Second)) {
		t.Fatalf("基準窗應為爬升前半 [start, start+1s],得到 [%v, %v]", baseStart, baseEnd)
	}
	if !stressStart.Equal(start.Add(2*time.Second)) || !stressEnd.Equal(start.Add(6*time.Second)) {
		t.Fatalf("臨界窗應為持平段 [start+2s, start+6s],得到 [%v, %v]", stressStart, stressEnd)
	}
}

func TestRenderObservationCulprit(t *testing.T) {
	var sb strings.Builder
	renderObservation(&sb, observe.Analysis{
		Status: observe.StatusOK,
		Top: []observe.Degradation{{
			Operation:   "SELECT orders",
			BaselineP95: 10 * time.Millisecond,
			StressedP95: 80 * time.Millisecond,
			Factor:      8.0,
		}},
	}, false)
	out := sb.String()

	for _, want := range []string{"SELECT orders", "10ms", "80ms", "8.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("輸出應含 %q,實際:\n%s", want, out)
		}
	}
}

// 誠實原則的呈現層:關聯不足與樣本截斷都必須清楚可見。
func TestRenderObservationInsufficientAndTruncated(t *testing.T) {
	var sb strings.Builder
	renderObservation(&sb, observe.Analysis{
		Status: observe.StatusInsufficient,
		Reason: "沒有任何 operation 在兩個時間窗都達到 20 筆樣本——無法做有代表性的比較。可嘗試:提高 APM 取樣率。",
	}, true)
	out := sb.String()

	if !strings.Contains(out, "關聯不足") {
		t.Fatalf("insufficient 應顯示關聯不足,實際:\n%s", out)
	}
	if !strings.Contains(out, "可能不完整") {
		t.Fatalf("截斷時應警示樣本可能不完整,實際:\n%s", out)
	}
	// p3-2 遺留 LOW:多句 Reason 需分行呈現,不可黏成一大段
	if !strings.Contains(out, "\n") || strings.Count(out, "。\n") < 1 {
		t.Fatalf("多句 Reason 應分行,實際:\n%s", out)
	}
}

// 排除可見化不可在呈現層失守:被排除的 op 名單必須印出,
// 否則「見 ExcludedOps」指向一個使用者根本看不到的欄位。
func TestRenderObservationShowsExcludedOps(t *testing.T) {
	var sb strings.Builder
	renderObservation(&sb, observe.Analysis{
		Status:      observe.StatusNoCulprit,
		Reason:      "在已納入比較的 operation 中沒有明顯的單點瓶頸。另有 2 個 operation 因樣本不足或僅出現於單一時間窗而未納入比較,可能遺漏真實瓶頸(見 ExcludedOps)。",
		ExcludedOps: []string{"low-traffic-op", "rare-op"},
	}, false)
	out := sb.String()

	for _, want := range []string{"low-traffic-op", "rare-op", "未納入比較"} {
		if !strings.Contains(out, want) {
			t.Fatalf("被排除的 op 名單必須可見,缺 %q:\n%s", want, out)
		}
	}
}

// 窗口啟發式的已知偏誤必須揭露:基準窗取自爬升前段,冷啟動會讓倍率被低估。
func TestRenderObservationDisclosesColdStartBias(t *testing.T) {
	var sb strings.Builder
	renderObservation(&sb, observe.Analysis{
		Status: observe.StatusOK,
		Top:    []observe.Degradation{{Operation: "A", Factor: 3.0, BaselineP95: 10 * time.Millisecond, StressedP95: 30 * time.Millisecond}},
	}, false)
	if !strings.Contains(sb.String(), "冷啟動") {
		t.Fatalf("應揭露基準窗冷啟動偏誤(倍率可能被低估),實際:\n%s", sb.String())
	}
}

// stubSource 是可程控的 TraceSource,供 runObservation 分支測試。
type stubSource struct {
	results []observe.FetchResult
	errs    []error
	calls   int
}

func (s *stubSource) FetchSpans(_ context.Context, _, _ time.Time) (observe.FetchResult, error) {
	i := s.calls
	s.calls++
	var res observe.FetchResult
	var err error
	if i < len(s.results) {
		res = s.results[i]
	}
	if i < len(s.errs) {
		err = s.errs[i]
	}
	return res, err
}

func TestRunObservationBranches(t *testing.T) {
	okSpans := func(op string, n int, d time.Duration) []observe.Span {
		out := make([]observe.Span, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, observe.Span{Operation: op, Duration: d})
		}
		return out
	}

	t.Run("holdDur 為零時警告並略過", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{}
		runObservation(&out, &errW, src, time.Now(), time.Second, 0)
		if src.calls != 0 {
			t.Fatal("無持平段不應發出任何查詢")
		}
		if !strings.Contains(errW.String(), "warning") {
			t.Fatalf("應警告略過,實際: %q", errW.String())
		}
	})

	t.Run("基準窗失敗警告並略過", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{errs: []error{fmt.Errorf("boom")}}
		runObservation(&out, &errW, src, time.Now(), time.Second, time.Second)
		if !strings.Contains(errW.String(), "基準窗") {
			t.Fatalf("錯誤訊息應指明基準窗,實際: %q", errW.String())
		}
	})

	t.Run("臨界窗失敗警告並略過", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{errs: []error{nil, fmt.Errorf("boom")}}
		runObservation(&out, &errW, src, time.Now(), time.Second, time.Second)
		if !strings.Contains(errW.String(), "臨界窗") {
			t.Fatalf("錯誤訊息應指明臨界窗,實際: %q", errW.String())
		}
	})

	t.Run("成功路徑輸出觀測段落且截斷傳遞", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{results: []observe.FetchResult{
			{Spans: okSpans("op", 25, 10*time.Millisecond)},
			{Spans: okSpans("op", 25, 80*time.Millisecond), Truncated: true},
		}}
		runObservation(&out, &errW, src, time.Now(), time.Second, time.Second)
		if !strings.Contains(out.String(), "目標系統觀測") {
			t.Fatalf("應輸出觀測段落,實際: %q", out.String())
		}
		if !strings.Contains(out.String(), "可能不完整") {
			t.Fatalf("任一窗截斷應警示,實際: %q", out.String())
		}
	})
}

func TestRenderObservationNoCulprit(t *testing.T) {
	var sb strings.Builder
	renderObservation(&sb, observe.Analysis{
		Status: observe.StatusNoCulprit,
		Reason: "所有 operation 大致等幅變慢——疑似整體資源飽和。",
		Top:    []observe.Degradation{{Operation: "A", Factor: 3.0, BaselineP95: 10 * time.Millisecond, StressedP95: 30 * time.Millisecond}},
	}, false)
	out := sb.String()

	if !strings.Contains(out, "資源飽和") {
		t.Fatalf("no_culprit 的 Reason 應完整呈現,實際:\n%s", out)
	}
}

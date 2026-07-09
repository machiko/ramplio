package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/observe"
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

	// v3.1 起支援 Tempo backend
	tempoSrc, tempoErr := parseObserveDSN("tempo://localhost:3200?service=checkout")
	if tempoErr != nil {
		t.Fatalf("tempo:// 應為合法 DSN: %v", tempoErr)
	}
	if tempoSrc == nil {
		t.Fatal("應回傳 TraceSource")
	}
	if _, err := parseObserveDSN("tempo://localhost:3200"); err == nil {
		t.Fatal("tempo 缺 service 應報錯")
	}
	if _, err := parseObserveDSN("zipkin://x?service=y"); err == nil {
		t.Fatal("不支援的 scheme 應報錯並列出支援清單")
	}
	// scheme 錯 + service 也缺:應報 scheme(根本原因),不誤導補參數
	if err := func() error { _, e := parseObserveDSN("zipkin://x"); return e }(); err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("雙重錯誤時應優先報不支援的 scheme,得到: %v", err)
	}
}

// 設定錯誤要在開跑前失敗(fail fast),不浪費一輪壓測;
// 「不適用」(VU 模式配 --observe)與「不可信」(觀測品質)是不同語意。
func TestValidateObserveConfig(t *testing.T) {
	if src, err := validateObserveConfig("", 0); src != nil || err != nil {
		t.Fatalf("未啟用觀測應回 nil, nil: %v %v", src, err)
	}
	if _, err := validateObserveConfig("jaeger://h:1?service=x", 0); err == nil || !strings.Contains(err.Error(), "--rps") {
		t.Fatalf("VU 模式配 --observe 應報「需搭配 --rps」的設定錯誤: %v", err)
	}
	if _, err := validateObserveConfig("zipkin://h?service=x", 100); err == nil {
		t.Fatal("壞 DSN 應在開跑前報錯")
	}
	if src, err := validateObserveConfig("jaeger://h:1?service=x", 100); src == nil || err != nil {
		t.Fatalf("合法設定應回 TraceSource: %v %v", src, err)
	}
}

// strict-trust 守門:僅在「有啟用觀測且不可信」時失敗;
// 未啟用觀測時不適用(不可把 no-op 當失敗)。
func TestStrictTrustGateErr(t *testing.T) {
	tests := []struct {
		strict, hasObserve, trusted bool
		wantErr                     bool
	}{
		{true, true, false, true},   // strict + 觀測不可信 → 失敗
		{true, true, true, false},   // strict + 可信 → 過
		{true, false, false, false}, // strict 但未啟用觀測 → 不適用
		{false, true, false, false}, // 非 strict → 只警告不擋
	}
	for _, tt := range tests {
		err := strictTrustGateErr(tt.strict, tt.hasObserve, tt.trusted)
		if (err != nil) != tt.wantErr {
			t.Fatalf("strict=%v hasObserve=%v trusted=%v: err=%v, wantErr=%v",
				tt.strict, tt.hasObserve, tt.trusted, err, tt.wantErr)
		}
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

	t.Run("holdDur 為零時警告並略過(不可信)", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{}
		trusted := runObservation(&out, &errW, src, time.Now(), time.Second, 0)
		if src.calls != 0 {
			t.Fatal("無持平段不應發出任何查詢")
		}
		if !strings.Contains(errW.String(), "warning") {
			t.Fatalf("應警告略過,實際: %q", errW.String())
		}
		if trusted {
			t.Fatal("略過的觀測不可回報為可信")
		}
	})

	t.Run("基準窗失敗警告並略過(不可信)", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{errs: []error{fmt.Errorf("boom")}}
		if trusted := runObservation(&out, &errW, src, time.Now(), time.Second, time.Second); trusted {
			t.Fatal("拉取失敗不可回報為可信")
		}
		if !strings.Contains(errW.String(), "基準窗") {
			t.Fatalf("錯誤訊息應指明基準窗,實際: %q", errW.String())
		}
	})

	t.Run("臨界窗失敗警告並略過(不可信)", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{errs: []error{nil, fmt.Errorf("boom")}}
		if trusted := runObservation(&out, &errW, src, time.Now(), time.Second, time.Second); trusted {
			t.Fatal("拉取失敗不可回報為可信")
		}
		if !strings.Contains(errW.String(), "臨界窗") {
			t.Fatalf("錯誤訊息應指明臨界窗,實際: %q", errW.String())
		}
	})

	t.Run("成功但截斷:輸出完整、回報不可信", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{results: []observe.FetchResult{
			{Spans: okSpans("op", 25, 10*time.Millisecond)},
			{Spans: okSpans("op", 25, 80*time.Millisecond), Truncated: true},
		}}
		trusted := runObservation(&out, &errW, src, time.Now(), time.Second, time.Second)
		if !strings.Contains(out.String(), "目標系統觀測") {
			t.Fatalf("應輸出觀測段落,實際: %q", out.String())
		}
		if !strings.Contains(out.String(), "可能不完整") {
			t.Fatalf("任一窗截斷應警示,實際: %q", out.String())
		}
		if trusted {
			t.Fatal("樣本截斷的觀測不可回報為可信(strict-trust 據此判定)")
		}
	})

	t.Run("成功且樣本充分:可信", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{results: []observe.FetchResult{
			{Spans: okSpans("op", 25, 10*time.Millisecond)},
			{Spans: okSpans("op", 25, 80*time.Millisecond)},
		}}
		if trusted := runObservation(&out, &errW, src, time.Now(), time.Second, time.Second); !trusted {
			t.Fatal("成功且無截斷、非 insufficient 的觀測應為可信")
		}
	})

	t.Run("關聯不足:輸出誠實、回報不可信", func(t *testing.T) {
		var out, errW strings.Builder
		src := &stubSource{results: []observe.FetchResult{
			{Spans: okSpans("op", 3, 10*time.Millisecond)}, // 遠低於樣本門檻
			{Spans: okSpans("op", 3, 80*time.Millisecond)},
		}}
		if trusted := runObservation(&out, &errW, src, time.Now(), time.Second, time.Second); trusted {
			t.Fatal("關聯不足(insufficient)不可回報為可信")
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

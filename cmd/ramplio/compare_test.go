package main

import (
	"strings"
	"testing"

	"github.com/machiko/ramplio/v2/internal/baseline"
)

func comparisonFixture() baseline.Comparison {
	return baseline.Comparison{
		Deltas: []baseline.MetricDelta{
			{Name: "p99_ms", Before: 100, After: 150, DeltaPct: 50, Verdict: baseline.VerdictRegressed},
			{Name: "error_rate_pct", Before: 0.1, After: 0.1, DeltaPct: 0, Verdict: baseline.VerdictStable},
			{Name: "throughput_rps", Before: 200, After: 240, DeltaPct: 20, Verdict: baseline.VerdictImproved},
		},
		Regressed: true,
		Warnings:  []string{"本次(after)的量測可信度存疑:收集器丟棄了 500 筆樣本"},
	}
}

// compare 是守門命令:輸出必須白話、警告必須強制顯示(審查關要求,
// 不可信的比較結果默默通過是危險的假陽性)。
func TestRenderComparison(t *testing.T) {
	var sb strings.Builder
	renderComparison(&sb, comparisonFixture())
	out := sb.String()

	for _, want := range []string{
		"退步",        // regressed 的白話
		"p99",       // 指標可辨識
		"100ms",     // before 值
		"150ms",     // after 值
		"+50",       // 變化幅度
		"改善",        // improved 的白話
		"注意",        // warnings 區塊標題
		"丟棄了 500 筆", // 警告內容必須完整印出
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("輸出應含 %q,實際:\n%s", want, out)
		}
	}
}

func TestRenderComparisonAllStable(t *testing.T) {
	var sb strings.Builder
	renderComparison(&sb, baseline.Comparison{
		Deltas: []baseline.MetricDelta{
			{Name: "p99_ms", Before: 100, After: 102, DeltaPct: 2, Verdict: baseline.VerdictStable},
		},
	})
	out := sb.String()
	if !strings.Contains(out, "持平") {
		t.Fatalf("全持平時應有持平結論,實際:\n%s", out)
	}
	if strings.Contains(out, "注意") {
		t.Fatalf("無警告時不應出現注意區塊,實際:\n%s", out)
	}
}

// 白話單一來源:同一概念必須與 terminal/interpret 同詞——
// 服務端延遲=「伺服器處理」、CO 修正延遲=「使用者實感」,不可自創同義詞。
func TestRenderComparisonVocabularyMatchesInterpret(t *testing.T) {
	var sb strings.Builder
	renderComparison(&sb, baseline.Comparison{
		Deltas: []baseline.MetricDelta{
			{Name: "p99_ms", Before: 100, After: 100, Verdict: baseline.VerdictStable},
			{Name: "corrected_p99_ms", Before: 120, After: 120, Verdict: baseline.VerdictStable},
		},
	})
	out := sb.String()

	// 連全形括號一起釘死,與 terminal.go 逐字一致——半形/全形漂移也算回歸
	for _, want := range []string{"p99（伺服器處理）", "p99（使用者實感）"} {
		if !strings.Contains(out, want) {
			t.Fatalf("compare 用語應與 terminal.go 逐字一致,缺 %q:\n%s", want, out)
		}
	}
	for _, ban := range []string{"一般回應時間", "最慢回應時間", "壓力下實感"} {
		if strings.Contains(out, ban) {
			t.Fatalf("不可自創同義詞 %q(單一來源原則):\n%s", ban, out)
		}
	}

	// 容量詞彙與 discover 對齊:SafeLimit=「安全上限」,不可自創「安全容量上限」
	var sb2 strings.Builder
	renderComparison(&sb2, baseline.Comparison{
		Deltas: []baseline.MetricDelta{
			{Name: "safe_limit_rps", Before: 900, After: 900, Verdict: baseline.VerdictStable},
		},
	})
	if !strings.Contains(sb2.String(), "安全上限") || strings.Contains(sb2.String(), "安全容量上限") {
		t.Fatalf("safe_limit 用語應與 discover 的「安全上限」一致:\n%s", sb2.String())
	}
}

// 守門契約:退步時 compare 必須以非零 exit code 結束(CI 靠這個擋合併)。
func TestCompareVerdictError(t *testing.T) {
	if err := compareVerdictErr(baseline.Comparison{Regressed: true}); err == nil {
		t.Fatal("Regressed=true 必須回傳錯誤(exit 1)")
	}
	if err := compareVerdictErr(baseline.Comparison{Regressed: false}); err != nil {
		t.Fatalf("Regressed=false 不應回傳錯誤,得到: %v", err)
	}
}

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	if err := compareVerdictErr(baseline.Comparison{Regressed: true}, false); err == nil {
		t.Fatal("Regressed=true 必須回傳錯誤(exit 1)")
	}
	if err := compareVerdictErr(baseline.Comparison{Regressed: false}, false); err != nil {
		t.Fatalf("Regressed=false 不應回傳錯誤,得到: %v", err)
	}
}

// --strict-trust:CI 場景「量測不可信視同失敗」——Warnings 非空即非零 exit,
// 即使指標全持平;預設(非 strict)維持只警告不擋的既有行為。
func TestCompareStrictTrust(t *testing.T) {
	suspect := baseline.Comparison{
		Regressed: false,
		Warnings:  []string{"本次(after)的量測可信度存疑:收集器丟棄了 500 筆樣本"},
	}
	if err := compareVerdictErr(suspect, false); err != nil {
		t.Fatalf("非 strict 模式下可信度存疑只警告不擋,得到: %v", err)
	}
	err := compareVerdictErr(suspect, true)
	if err == nil {
		t.Fatal("strict 模式下 Warnings 非空必須回傳錯誤")
	}
	if !strings.Contains(err.Error(), "可信") {
		t.Fatalf("錯誤訊息應說明是可信度問題而非回歸: %v", err)
	}

	clean := baseline.Comparison{Regressed: false}
	if err := compareVerdictErr(clean, true); err != nil {
		t.Fatalf("strict 模式下無警告無退步不應報錯: %v", err)
	}
}

func execCompare(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCompareCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func saveRunRecord(t *testing.T, dir, key string, p99Ms float64) string {
	t.Helper()
	rec := history.Record{
		Baseline: baseline.Baseline{
			SchemaVersion: baseline.SchemaVersion,
			CreatedAt:     time.Now().UTC(),
			Scenario:      key,
			Metrics:       &baseline.MetricsSnapshot{Total: 100, P50Ms: p99Ms / 2, P99Ms: p99Ms, ThroughputRPS: 100},
		},
		Kind:      history.KindRun,
		TargetKey: key,
	}
	res, err := history.Save(dir, rec, history.DefaultKeep)
	require.NoError(t, err)
	return res.Path
}

// 情境 13：零參數比「最近一筆」與「同目標的前一筆」，沿用退步 → 錯誤的契約。
func TestCompareZeroArgUsesHistory(t *testing.T) {
	t.Run("退步回錯誤", func(t *testing.T) {
		dir := useHistoryDir(t)
		saveRunRecord(t, dir, "A", 100)
		saveRunRecord(t, dir, "B", 999) // 夾在中間的其他目標不能被拿來比
		saveRunRecord(t, dir, "A", 300)

		out, err := execCompare(t)

		require.Error(t, err, "退步時要 exit ≠ 0")
		assert.Contains(t, out, "100ms")
		assert.Contains(t, out, "300ms")
		assert.NotContains(t, out, "999ms", "不可拿別的目標來比")
		assert.Contains(t, out, "退步")
	})
	t.Run("持平回 nil", func(t *testing.T) {
		dir := useHistoryDir(t)
		saveRunRecord(t, dir, "A", 100)
		saveRunRecord(t, dir, "A", 101)

		out, err := execCompare(t)

		require.NoError(t, err)
		assert.Contains(t, out, "持平")
	})
}

// 情境 14：紀錄不夠時回錯誤並給出怎麼產生紀錄的範例。
func TestCompareZeroArgNotEnoughHistory(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"沒有任何紀錄":       func(*testing.T, string) {},
		"只有一筆":         func(t *testing.T, dir string) { saveRunRecord(t, dir, "A", 100) },
		"最近一筆沒有同目標前一筆": func(t *testing.T, dir string) { saveRunRecord(t, dir, "A", 100); saveRunRecord(t, dir, "B", 100) },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			dir := useHistoryDir(t)
			arrange(t, dir)

			_, err := execCompare(t)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "ramplio run")
		})
	}
}

// 情境 16：只給 1 個參數是用法錯誤，不能默默當成零參數。
func TestCompareOneArgRejected(t *testing.T) {
	useHistoryDir(t)

	_, err := execCompare(t, "only.json")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "1")
}

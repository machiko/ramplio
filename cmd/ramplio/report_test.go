package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func execReport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newReportCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func saveReportRecord(t *testing.T, dir, scenario string, total int64) {
	t.Helper()
	sum := metrics.Summary{Total: total}
	rep := reporter.SummaryToReport(sum)
	_, err := history.Save(dir, history.Record{
		Baseline:  baseline.FromSummary(sum, scenario),
		Kind:      history.KindRun,
		TargetKey: scenario,
		Report:    &rep,
	}, history.DefaultKeep)
	require.NoError(t, err)
}

func saveDiscoverRecord(t *testing.T, dir string) {
	t.Helper()
	_, err := history.Save(dir, history.Record{
		Baseline:  baseline.FromDiscover(discover.DiscoverResult{SafeLimit: 10, BreakingPoint: 20}, "http://d.test/"),
		Kind:      history.KindDiscover,
		TargetKey: "d",
	}, history.DefaultKeep)
	require.NoError(t, err)
}

// 情境 15：零參數用最近一筆有報告的 run 紀錄產生 HTML，並說明用的是哪一筆。
func TestReportZeroArgUsesLatestRun(t *testing.T) {
	dir := useHistoryDir(t)
	saveReportRecord(t, dir, "http://old.test/", 111)
	saveReportRecord(t, dir, "http://new.test/", 222)
	saveDiscoverRecord(t, dir) // 最新的是 discover，要跳過它
	outPath := filepath.Join(t.TempDir(), "r.html")

	out, err := execReport(t, "--output", outPath)

	require.NoError(t, err)
	assert.Contains(t, out, "http://new.test/", "要說明用的是哪一筆紀錄")
	html, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(html), "222", "要用最近一筆 run 紀錄的數字")
	assert.NotContains(t, string(html), "111")
}

// 情境 15（負向）：只有 discover 紀錄時回錯誤並說明，不產生空報告。
func TestReportZeroArgOnlyDiscover(t *testing.T) {
	dir := useHistoryDir(t)
	saveDiscoverRecord(t, dir)
	outPath := filepath.Join(t.TempDir(), "r.html")

	_, err := execReport(t, "--output", outPath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "discover")
	assert.Contains(t, err.Error(), "ramplio run")
	assert.NoFileExists(t, outPath)
}

// 沒有任何紀錄也要給出下一步。
func TestReportZeroArgNoHistory(t *testing.T) {
	useHistoryDir(t)
	outPath := filepath.Join(t.TempDir(), "r.html")

	_, err := execReport(t, "--output", outPath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ramplio run")
	assert.NoFileExists(t, outPath)
}

// 相容：--input 照舊讀 --output 存的 JSON 報告。
func TestReportExplicitInputStillWorks(t *testing.T) {
	in := filepath.Join(t.TempDir(), "in.json")
	f, err := os.Create(in)
	require.NoError(t, err)
	require.NoError(t, reporter.WriteJSON(f, metrics.Summary{Total: 333}))
	require.NoError(t, f.Close())
	outPath := filepath.Join(t.TempDir(), "r.html")

	_, err = execReport(t, "--input", in, "--output", outPath)

	require.NoError(t, err)
	html, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(html), "333")
}

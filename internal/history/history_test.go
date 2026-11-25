package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/metrics"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runRecord(key string) Record {
	sum := metrics.Summary{Total: 10}
	rep := reporter.SummaryToReport(sum)
	return Record{
		Baseline:  baseline.FromSummary(sum, "http://example.test/"),
		Kind:      KindRun,
		TargetKey: key,
		Report:    &rep,
	}
}

func discoverRecord(key string) Record {
	return Record{
		Baseline:  baseline.FromDiscover(discover.DiscoverResult{SafeLimit: 10, BreakingPoint: 20}, "http://example.test/"),
		Kind:      KindDiscover,
		TargetKey: key,
	}
}

func listRecordFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, RecordsSubdir, "*.json"))
	require.NoError(t, err)
	return matches
}

// 情境 11（資料層）：第一次建立目錄時要寫 .gitignore（內容 *）並回報 Created。
func TestHistorySaveFirstCreateWritesGitignore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ramplio")

	first, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	assert.True(t, first.Created, "第一次存檔應回報建立了目錄")

	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(ignore))

	second, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	assert.False(t, second.Created, "目錄已存在時不應再回報建立")
	assert.NotEqual(t, first.Path, second.Path, "兩次存檔不可共用同一個檔名")
}

// 紀錄檔是 baseline 的超集：既有的 baseline.Load（compare 用）要能直接讀。
func TestHistorySavedRecordLoadsAsBaseline(t *testing.T) {
	dir := t.TempDir()

	res, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)

	b, err := baseline.Load(res.Path)
	require.NoError(t, err)
	require.NotNil(t, b.Metrics)
	assert.Equal(t, int64(10), b.Metrics.Total)

	raw, err := os.ReadFile(res.Path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"target_key": "k"`)
	assert.Contains(t, string(raw), `"report": {`)
}

// 情境 12：超過保留上限時刪最舊的，總數維持上限。
func TestHistorySavePrunesOldest(t *testing.T) {
	dir := t.TempDir()
	const keep = 3

	var paths []string
	for i := 0; i < keep+2; i++ {
		res, err := Save(dir, runRecord("k"), keep)
		require.NoError(t, err)
		require.NoError(t, res.PruneErr)
		paths = append(paths, res.Path)
	}

	files := listRecordFiles(t, dir)
	assert.Len(t, files, keep)
	for _, old := range paths[:2] {
		assert.NoFileExists(t, old, "最舊的紀錄應被刪除")
	}
	for _, kept := range paths[2:] {
		assert.FileExists(t, kept)
	}
}

// List 由新到舊，壞檔放進錯誤清單、不中斷。
func TestHistoryListNewestFirstSkipsCorrupt(t *testing.T) {
	dir := t.TempDir()
	a, err := Save(dir, runRecord("a"), DefaultKeep)
	require.NoError(t, err)
	b, err := Save(dir, discoverRecord("b"), DefaultKeep)
	require.NoError(t, err)
	bad := filepath.Join(dir, RecordsSubdir, "00000000T000000.000Z-00-run.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o644))

	entries, errs := List(dir)

	require.Len(t, entries, 2)
	assert.Equal(t, b.Path, entries[0].Path, "最新的應排第一")
	assert.Equal(t, a.Path, entries[1].Path)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), filepath.Base(bad))
	assert.Nil(t, entries[0].Record.Report, "discover 紀錄沒有 report")
	assert.NotNil(t, entries[1].Record.Report)
}

func TestHistoryListMissingDirIsEmpty(t *testing.T) {
	entries, errs := List(filepath.Join(t.TempDir(), "nope"))
	assert.Empty(t, entries)
	assert.Empty(t, errs)
}

func TestHistoryPreviousSameTarget(t *testing.T) {
	entries := []Entry{
		{Path: "4", Record: runRecord("x")},
		{Path: "3", Record: runRecord("y")},
		{Path: "2", Record: runRecord("x")},
		{Path: "1", Record: runRecord("x")},
	}

	prev, ok := PreviousSameTarget(entries, "x", "4")
	require.True(t, ok)
	assert.Equal(t, "2", prev.Path, "應跳過自己與其他目標，取最近的同目標紀錄")

	_, ok = PreviousSameTarget(entries, "y", "3")
	assert.False(t, ok, "沒有其他同目標紀錄時回 false")

	_, ok = PreviousSameTarget(entries, "", "")
	assert.False(t, ok, "空的 key 不可匹配")
}

func TestHistoryLatestWithReport(t *testing.T) {
	entries := []Entry{
		{Path: "3", Record: discoverRecord("d")},
		{Path: "2", Record: runRecord("r")},
		{Path: "1", Record: runRecord("r")},
	}
	got, ok := LatestWithReport(entries)
	require.True(t, ok)
	assert.Equal(t, "2", got.Path)

	_, ok = LatestWithReport(entries[:1])
	assert.False(t, ok)
}

// Save 拒絕無效紀錄（與 baseline.Save 同一套不變量）。
func TestHistorySaveRejectsInvalidRecord(t *testing.T) {
	dir := t.TempDir()
	_, err := Save(dir, Record{Kind: KindRun, TargetKey: "k"}, DefaultKeep)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "Metrics") || strings.Contains(err.Error(), "無效"))
	assert.Empty(t, listRecordFiles(t, dir))
}

// M1：header＋body 的雜湊。順序與名稱大小寫不影響；值或 body 不同就不同；不含明文。
func TestHistoryRequestFingerprint(t *testing.T) {
	base := RequestFingerprint(map[string]string{"X-A": "1", "X-B": "2"}, []byte("body"))

	assert.Regexp(t, "^[0-9a-f]{8}$", base, "只取 sha256 前 8 碼")
	assert.Equal(t, base, RequestFingerprint(map[string]string{"x-b": "2", "x-a": "1"}, []byte("body")),
		"名稱大小寫與順序不影響")
	assert.NotEqual(t, base, RequestFingerprint(map[string]string{"X-A": "9", "X-B": "2"}, []byte("body")),
		"header 值不同要不同")
	assert.NotEqual(t, base, RequestFingerprint(map[string]string{"X-A": "1", "X-B": "2"}, []byte("other")),
		"body 不同要不同")
	assert.NotEqual(t, RequestFingerprint(map[string]string{"X-A": "1b"}, nil),
		RequestFingerprint(map[string]string{"X-A": "1"}, []byte("b")),
		"header 與 body 的邊界不可混淆")
	assert.Equal(t, RequestFingerprint(nil, nil), RequestFingerprint(map[string]string{}, []byte{}),
		"沒有 header 與 body 時結果穩定")
}

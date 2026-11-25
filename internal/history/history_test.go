package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// 情境 11（資料層）：第一次建立目錄時要寫 .gitignore（內容 *）並回報 WroteGitignore。
func TestHistorySaveFirstCreateWritesGitignore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ramplio")

	first, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	assert.True(t, first.WroteGitignore, "第一次存檔應回報寫了 .gitignore")

	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(ignore))

	second, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	assert.False(t, second.WroteGitignore, ".gitignore 已存在時不應再回報寫入")
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

	entries, errs, err := List(dir)
	require.NoError(t, err)

	require.Len(t, entries, 2)
	assert.Equal(t, b.Path, entries[0].Path, "最新的應排第一")
	assert.Equal(t, a.Path, entries[1].Path)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), filepath.Base(bad))
	assert.Nil(t, entries[0].Record.Report, "discover 紀錄沒有 report")
	assert.NotNil(t, entries[1].Record.Report)
}

func TestHistoryListMissingDirIsEmpty(t *testing.T) {
	entries, errs, err := List(filepath.Join(t.TempDir(), "nope"))
	require.NoError(t, err)
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

// L1：0 byte 的檔是另一個行程正在寫的佔位檔，靜默略過、不算壞檔。
func TestHistoryListSkipsEmptyPlaceholderSilently(t *testing.T) {
	dir := t.TempDir()
	res, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	placeholder := filepath.Join(dir, RecordsSubdir, "29991231T235959.999Z-00.json")
	require.NoError(t, os.WriteFile(placeholder, nil, 0o644))

	entries, skipped, err := List(dir)

	require.NoError(t, err)
	assert.Empty(t, skipped, "佔位檔不可產生警告")
	require.Len(t, entries, 1)
	assert.Equal(t, res.Path, entries[0].Path)
}

// L2：要刪的舊檔已經被別人刪掉（並行清理），不算失敗。
func TestHistoryRemoveOldestIgnoresMissing(t *testing.T) {
	runsDir := t.TempDir()
	keepMe := filepath.Join(runsDir, "b.json")
	require.NoError(t, os.WriteFile(keepMe, []byte("{}"), 0o644))

	err := removeOldest(runsDir, []string{"gone.json", "b.json"}, 1)

	require.NoError(t, err)
	assert.FileExists(t, keepMe)
}

// L3：時鐘倒退時，新紀錄的檔名仍要排在既有紀錄之後。
func TestHistoryClockGoesBackwardKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	setClock(t, t0)
	first, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)

	setClock(t, t0.Add(-time.Hour))
	second, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)

	assert.Greater(t, filepath.Base(second.Path), filepath.Base(first.Path))
	entries, _, err := List(dir)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, second.Path, entries[0].Path, "最新存的要排第一")
}

func setClock(t *testing.T, at time.Time) {
	t.Helper()
	orig := nowFunc
	nowFunc = func() time.Time { return at }
	t.Cleanup(func() { nowFunc = orig })
}

// L5：整個紀錄目錄讀不了（是檔案、沒有權限）時回傳目錄層級的錯誤，跟個別壞檔分開。
func TestHistoryListDirErrorIsSeparate(t *testing.T) {
	t.Run("紀錄目錄是檔案", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(dir, []byte("x"), 0o644))

		entries, skipped, err := List(dir)

		require.Error(t, err)
		assert.Empty(t, entries)
		assert.Empty(t, skipped)
	})
	t.Run("runs 不可讀", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 不受目錄權限限制")
		}
		dir := t.TempDir()
		runsDir := filepath.Join(dir, RecordsSubdir)
		require.NoError(t, os.Mkdir(runsDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(runsDir, 0o700) })

		_, _, err := List(dir)

		require.Error(t, err)
	})
}

// L6：使用者自己先建了目錄，也要補寫 .gitignore，而且只回報實際寫入的那次。
func TestHistoryGitignoreWrittenWhenDirPreexists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ramplio")
	require.NoError(t, os.Mkdir(dir, 0o755))

	first, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)
	second, err := Save(dir, runRecord("k"), DefaultKeep)
	require.NoError(t, err)

	assert.True(t, first.WroteGitignore)
	assert.False(t, second.WroteGitignore)
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(ignore))
}

// L6（負向）：使用者自己的 .gitignore 不可被覆寫。
func TestHistoryGitignoreKeepsUserFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ramplio")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("custom\n"), 0o644))

	res, err := Save(dir, runRecord("k"), DefaultKeep)

	require.NoError(t, err)
	assert.False(t, res.WroteGitignore)
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "custom\n", string(ignore))
}

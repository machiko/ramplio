package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/baseline"
	"github.com/machiko/ramplio/v3/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain points the automatic history at a throwaway directory so no test
// in this package leaves a .ramplio/ behind in the source tree. Tests that
// inspect history set their own directory with t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ramplio-history-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "建立測試用紀錄目錄失敗:", err)
		os.Exit(1)
	}
	if err := os.Setenv(historyDirEnv, dir); err != nil {
		fmt.Fprintln(os.Stderr, "設定測試用紀錄目錄失敗:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// execRun drives the real `run` command. Only output written through cobra's
// writers is captured; the long-standing summary still goes to os.Stdout.
func execRun(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRunCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// useHistoryDir gives one test its own history directory (not yet created).
func useHistoryDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".ramplio")
	t.Setenv(historyDirEnv, dir)
	return dir
}

func historyFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, history.RecordsSubdir, "*.json"))
	require.NoError(t, err)
	return files
}

// delayServer answers 200 after a delay that the test can change between runs.
type delayServer struct {
	*httptest.Server
	delay atomic.Int64 // nanoseconds
}

func newDelayServer(t *testing.T) *delayServer {
	t.Helper()
	ds := &delayServer{}
	ds.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Duration(ds.delay.Load()))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ds.Close)
	return ds
}

const quickRun = "300ms"

// 情境 1：run --url 跑完自動存一筆，baseline.Load 讀得到，帶 report。
func TestHistoryRunAutoSaves(t *testing.T) {
	dir := useHistoryDir(t)
	srv := newDelayServer(t)

	stdout, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)

	files := historyFiles(t, dir)
	require.Len(t, files, 1)
	b, err := baseline.Load(files[0])
	require.NoError(t, err)
	require.NotNil(t, b.Metrics)
	assert.Positive(t, b.Metrics.Total)

	entries, errs, err := history.List(dir)
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Len(t, entries, 1)
	assert.NotNil(t, entries[0].Record.Report, "run 紀錄要帶完整報告")
	assert.Equal(t, history.KindRun, entries[0].Record.Kind)
	assert.Contains(t, stdout, files[0], "要告訴使用者存在哪裡")
}

// 情境 2：discover 與單一入口跑完都自動存，沒有 report。
func TestHistoryDiscoverAutoSaves(t *testing.T) {
	t.Run("discover 指令", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newRecordingServer(t, "", "")

		_, err := execDiscover(t, "", "--url", srv.URL, "--probe-duration", "1500ms", "--max-rps", "5")
		require.NoError(t, err)

		assertOneDiscoverRecord(t, dir)
	})
	t.Run("單一入口", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newCapturingServer(t)

		_, err := runEntry(t, "", srv.URL+"/health")
		require.NoError(t, err)

		assertOneDiscoverRecord(t, dir)
	})
}

func assertOneDiscoverRecord(t *testing.T, dir string) {
	t.Helper()
	entries, errs, err := history.List(dir)
	require.NoError(t, err)
	require.Empty(t, errs)
	require.Len(t, entries, 1)
	rec := entries[0].Record
	assert.Equal(t, history.KindDiscover, rec.Kind)
	assert.NotNil(t, rec.Discover)
	assert.Nil(t, rec.Report, "discover 沒有 metrics.Summary，不該有 report")
}

// 情境 3（負向）：存檔內容不能出現網址裡的 token 與 fragment。
func TestHistoryMasksURL(t *testing.T) {
	const secret, frag = "SECRET123", "fragXYZ"

	t.Run("run", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newDelayServer(t)
		target := srv.URL + "/?token=" + secret + "#" + frag

		_, _, err := execRun(t, "--url", target, "-d", quickRun, "--no-tui")
		require.NoError(t, err)

		assertRecordsHide(t, dir, secret, frag)
	})
	t.Run("discover", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newRecordingServer(t, "", "")
		target := srv.URL + "/?token=" + secret + "#" + frag

		_, err := execDiscover(t, "", "--url", target, "--probe-duration", "1500ms", "--max-rps", "5")
		require.NoError(t, err)

		assertRecordsHide(t, dir, secret, frag)
	})
}

func assertRecordsHide(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	files := historyFiles(t, dir)
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	for _, s := range secrets {
		assert.NotContains(t, string(raw), s)
	}
	assert.Contains(t, string(raw), "token=***", "遮罩後的網址仍要能辨識目標")
}

// 情境 4（負向）：被擋下的寫入型 discover 不存紀錄。
func TestHistoryBlockedWriteNotSaved(t *testing.T) {
	t.Run("discover --method POST", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newRecordingServer(t, "", "")

		_, err := execDiscover(t, "", "--url", srv.URL, "--method", "POST", "--probe-duration", "1s", "--max-rps", "5")
		require.Error(t, err)

		assert.NoDirExists(t, dir)
	})
	t.Run("單一入口 curl POST", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newCapturingServer(t)

		_, err := runEntry(t, "", "curl -X POST '"+srv.URL+"'")
		require.NoError(t, err)

		assert.NoDirExists(t, dir)
	})
}

// 情境 5（負向）：--no-history 不建立目錄、不寫檔。
func TestHistoryNoHistoryFlag(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newDelayServer(t)

		stdout, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui", "--no-history")
		require.NoError(t, err)

		assert.NoDirExists(t, dir)
		assert.NotContains(t, stdout, "紀錄")
	})
	t.Run("discover", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newRecordingServer(t, "", "")

		_, err := execDiscover(t, "", "--url", srv.URL, "--probe-duration", "1500ms", "--max-rps", "5", "--no-history")
		require.NoError(t, err)

		assert.NoDirExists(t, dir)
	})
}

// readOnlyHistoryDir returns a history path whose parent is not writable.
func readOnlyHistoryDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 不受目錄權限限制")
	}
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	dir := filepath.Join(parent, ".ramplio")
	t.Setenv(historyDirEnv, dir)
	return dir
}

// 情境 6（負向）：唯讀目錄只警告一行，結果照印、不回錯誤。
func TestHistoryReadOnlyDirKeepsResult(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		dir := readOnlyHistoryDir(t)
		srv := newDelayServer(t)

		_, stderr, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")

		require.NoError(t, err, "存檔失敗不可影響 exit code")
		assert.Equal(t, 1, strings.Count(stderr, "無法存執行紀錄"), "只警告一行，實際：%q", stderr)
		assert.NoDirExists(t, dir)
	})
	t.Run("discover", func(t *testing.T) {
		readOnlyHistoryDir(t)
		srv := newRecordingServer(t, "", "")

		out, err := execDiscover(t, "", "--url", srv.URL, "--probe-duration", "1500ms", "--max-rps", "5")

		require.NoError(t, err)
		assert.Contains(t, out, "容量報告", "報告照印")
		assert.Equal(t, 1, strings.Count(out, "無法存執行紀錄"))
	})
}

// 情境 7：有壞掉的紀錄檔時略過並警告，本次照存、照比。
func TestHistorySkipsCorruptRecord(t *testing.T) {
	dir := useHistoryDir(t)
	srv := newDelayServer(t)
	_, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	bad := filepath.Join(dir, history.RecordsSubdir, "99999999T999999.999Z-00-run.json")
	require.NoError(t, os.WriteFile(bad, []byte("{broken"), 0o644))

	stdout, stderr, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")

	require.NoError(t, err)
	assert.Contains(t, stderr, filepath.Base(bad), "要指出是哪個檔壞了")
	assert.Len(t, historyFiles(t, dir), 3, "壞檔不刪，本次照存")
	assert.Contains(t, stdout, "跟上一次", "略過壞檔後仍要找到上一筆好的紀錄來比")
}

// 情境 8：同目標同參數的第二次，結尾印比較段落。
func TestHistoryAutoDiffSameTarget(t *testing.T) {
	useHistoryDir(t)
	srv := newDelayServer(t)

	first, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	assert.Contains(t, first, "第一筆紀錄")
	assert.NotContains(t, first, "跟上一次")

	second, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	for _, want := range []string{"跟上一次", "p99", "錯誤率", "每秒請求", "→"} {
		assert.Contains(t, second, want)
	}
	assert.Regexp(t, "持平|改善|退步", second)
}

// 情境 9：換網址或換負載參數就不比較。
func TestHistoryAutoDiffSkipsDifferentTarget(t *testing.T) {
	useHistoryDir(t)
	srv := newDelayServer(t)
	_, _, err := execRun(t, "--url", srv.URL+"/a", "-d", quickRun, "--no-tui")
	require.NoError(t, err)

	for name, args := range map[string][]string{
		"不同網址":      {"--url", srv.URL + "/b", "-d", quickRun, "--no-tui"},
		"不同 VU 數":   {"--url", srv.URL + "/a", "-d", quickRun, "--no-tui", "--vus", "2"},
		"不同時長":      {"--url", srv.URL + "/a", "-d", "400ms", "--no-tui"},
		"多了 header": {"--url", srv.URL + "/a", "-d", quickRun, "--no-tui", "-H", "X-A: 1"},
		"多了 body":   {"--url", srv.URL + "/a", "-d", quickRun, "--no-tui", "--body", "a"},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, err := execRun(t, args...)
			require.NoError(t, err)
			assert.Contains(t, out, "第一筆紀錄")
			assert.NotContains(t, out, "跟上一次")
		})
	}
}

// 情境 10（負向）：比出退步也不改 exit code。
func TestHistoryAutoDiffRegressionKeepsExitCode(t *testing.T) {
	useHistoryDir(t)
	srv := newDelayServer(t)
	_, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)

	srv.delay.Store(int64(30 * time.Millisecond))
	out, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")

	require.NoError(t, err, "自動比對只是資訊，不可回錯誤")
	assert.Contains(t, out, "退步")
}

// 情境 11：第一次建立 .ramplio/ 自動寫 .gitignore 並說明一次。
func TestHistoryFirstCreateWritesGitignore(t *testing.T) {
	dir := useHistoryDir(t)
	srv := newDelayServer(t)

	first, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	ignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "*\n", string(ignore))
	assert.Contains(t, first, ".gitignore")

	second, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	assert.NotContains(t, second, ".gitignore", "只在第一次建立時說明")
}

// 情境 9（M1）：header 值不同、body 不同都不比較。
func TestHistoryAutoDiffSkipsDifferentRequest(t *testing.T) {
	cases := map[string][2][]string{
		"header 值不同": {{"-H", "X-A: 1"}, {"-H", "X-A: 2"}},
		"body 不同":    {{"--body", "a"}, {"--body", "b"}},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			useHistoryDir(t)
			srv := newDelayServer(t)
			base := []string{"--url", srv.URL, "-d", quickRun, "--no-tui"}

			_, _, err := execRun(t, append(base, pair[0]...)...)
			require.NoError(t, err)
			out, _, err := execRun(t, append(base, pair[1]...)...)
			require.NoError(t, err)

			assert.Contains(t, out, "第一筆紀錄")
			assert.NotContains(t, out, "跟上一次")
		})
	}
}

// 情境 9（M1）：header 只差在順序或名稱大小寫，仍算同一目標。
func TestHistoryAutoDiffHeaderOrderAndCaseSameTarget(t *testing.T) {
	useHistoryDir(t)
	srv := newDelayServer(t)
	base := []string{"--url", srv.URL, "-d", quickRun, "--no-tui", "--body", "same"}

	_, _, err := execRun(t, append(base, "-H", "X-A: 1", "-H", "X-B: 2")...)
	require.NoError(t, err)
	out, _, err := execRun(t, append(base, "-H", "x-b: 2", "-H", "x-a: 1")...)
	require.NoError(t, err)

	assert.Contains(t, out, "跟上一次")
}

// 情境 9（M1）：discover（與單一入口共用的 runDiscover）也納入 header。
func TestHistoryDiscoverDifferentHeaderNotCompared(t *testing.T) {
	useHistoryDir(t)
	srv := newRecordingServer(t, "", "")
	args := []string{"--url", srv.URL, "--probe-duration", "1500ms", "--max-rps", "5"}

	_, err := execDiscover(t, "", append(args, "-H", "X-A: 1")...)
	require.NoError(t, err)
	same, err := execDiscover(t, "", append(args, "-H", "x-a: 1")...)
	require.NoError(t, err)
	diff, err := execDiscover(t, "", append(args, "-H", "X-A: 2")...)
	require.NoError(t, err)

	assert.Contains(t, same, "跟上一次", "只差名稱大小寫要算同一目標")
	assert.Contains(t, diff, "第一筆紀錄")
	assert.NotContains(t, diff, "跟上一次")
}

// 情境 9（M1，負向）：紀錄檔只存雜湊，找不到 header 值與 body 原文。
func TestHistoryRecordOmitsHeaderAndBody(t *testing.T) {
	const headerSecret, bodySecret = "HDRSECRET77", "BODYSECRET88"

	t.Run("run", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newDelayServer(t)

		_, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui",
			"-H", "Authorization: Bearer "+headerSecret, "--body", bodySecret)
		require.NoError(t, err)

		assertFilesHide(t, dir, headerSecret, bodySecret)
	})
	t.Run("discover", func(t *testing.T) {
		dir := useHistoryDir(t)
		srv := newRecordingServer(t, "Bearer "+headerSecret, "")

		_, err := execDiscover(t, "", "--url", srv.URL, "--probe-duration", "1500ms", "--max-rps", "5",
			"-H", "Authorization: Bearer "+headerSecret)
		require.NoError(t, err)

		assertFilesHide(t, dir, headerSecret)
	})
}

func assertFilesHide(t *testing.T, dir string, secrets ...string) {
	t.Helper()
	files := historyFiles(t, dir)
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	for _, s := range secrets {
		assert.NotContains(t, string(raw), s)
	}
}

// L5（負向）：紀錄目錄本身不能用（是檔案、runs 不可讀）時只警告一行，不回錯誤。
func TestHistoryUnusableDirWarnsOnce(t *testing.T) {
	t.Run("紀錄目錄是檔案", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(dir, []byte("x"), 0o644))
		t.Setenv(historyDirEnv, dir)
		srv := newDelayServer(t)

		_, stderr, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")

		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(stderr, "warning:"), "只警告一行，實際：%q", stderr)
	})
	t.Run("runs 不可讀", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root 不受目錄權限限制")
		}
		dir := useHistoryDir(t)
		runsDir := filepath.Join(dir, history.RecordsSubdir)
		require.NoError(t, os.MkdirAll(runsDir, 0o755))
		require.NoError(t, os.Chmod(runsDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(runsDir, 0o700) })
		srv := newDelayServer(t)

		_, stderr, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")

		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(stderr, "warning:"), "只警告一行，實際：%q", stderr)
	})
}

// L6：使用者自己先建好 .ramplio/ 時也補寫 .gitignore，說明只在實際寫入時印一次。
func TestHistoryPreexistingDirGetsGitignoreNotice(t *testing.T) {
	dir := useHistoryDir(t)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	srv := newDelayServer(t)

	first, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	second, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(dir, ".gitignore"))
	assert.Contains(t, first, ".gitignore")
	assert.NotContains(t, first, "已建立", "目錄是使用者建的，不能說是我們建立的")
	assert.NotContains(t, second, ".gitignore")
}

// M-2（負向）：RAMPLIO_HISTORY_DIR 指到有其他檔案的目錄時不寫 .gitignore，只提示一次。
func TestHistoryNonEmptyDirPrintsIgnoreHint(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	t.Setenv(historyDirEnv, dir)
	srv := newDelayServer(t)

	first, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)
	second, _, err := execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui")
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(dir, ".gitignore"))
	assert.Contains(t, first, filepath.Join(dir, history.RecordsSubdir), "要指出該忽略的路徑")
	assert.Contains(t, first, ".gitignore")
	assert.NotContains(t, second, ".gitignore", "只在第一次提示")
}

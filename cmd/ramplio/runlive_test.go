package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/distributed"
	"github.com/machiko/ramplio/v3/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tuiFunc has the shape of reporter.RunTUI.
type tuiFunc func(reporter.LiveProvider, context.CancelFunc, <-chan struct{}) error

// fakeLive replaces the terminal check, the TUI and the signal source, so the
// tests neither need a real terminal nor send real signals to the test process.
type fakeLive struct {
	isTTY bool
	sigs  chan os.Signal
	tui   tuiFunc

	mu       sync.Mutex
	tuiCalls int
	maxTotal int64
	stageTot int
}

func useFakeLive(t *testing.T, isTTY bool, tui tuiFunc) *fakeLive {
	t.Helper()
	f := &fakeLive{isTTY: isTTY, sigs: make(chan os.Signal, 2), tui: tui}
	if f.tui == nil {
		f.tui = f.watchUntilDone
	}
	old := newLiveEnv
	newLiveEnv = func() liveEnv {
		return liveEnv{
			isTTY: func() bool { return f.isTTY },
			runTUI: func(p reporter.LiveProvider, cancel context.CancelFunc, done <-chan struct{}) error {
				f.mu.Lock()
				f.tuiCalls++
				f.mu.Unlock()
				return f.tui(p, cancel, done)
			},
			signals: func() (<-chan os.Signal, func()) { return f.sigs, func() {} },
		}
	}
	t.Cleanup(func() { newLiveEnv = old })
	return f
}

// watchUntilDone behaves like the real TUI: it polls snapshots until the run ends.
func (f *fakeLive) watchUntilDone(p reporter.LiveProvider, _ context.CancelFunc, done <-chan struct{}) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			f.record(p.Snapshot())
			return nil
		case <-tick.C:
			f.record(p.Snapshot())
		}
	}
}

func (f *fakeLive) record(s reporter.LiveSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Total > f.maxTotal {
		f.maxTotal = s.Total
	}
	if s.StageTotal > f.stageTot {
		f.stageTot = s.StageTotal
	}
}

func (f *fakeLive) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tuiCalls
}

// sendAfter delivers sig on the injected signal source after d.
func (f *fakeLive) sendAfter(d time.Duration, sig os.Signal) {
	go func() {
		time.Sleep(d)
		f.sigs <- sig
	}()
}

// captureStdout collects everything written to os.Stdout while fn runs; the
// run summary and the "Running …" line are still printed there directly.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(copied)
	}()
	defer func() {
		os.Stdout = old
		_ = w.Close()
		<-copied
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	<-copied
	os.Stdout = old
	return buf.String()
}

const (
	summaryMark    = "\n測試結果\n" // 「測試結果說明」也含這四個字，要帶換行才唯一
	longRun        = "30s"
	interruptAfter = 500 * time.Millisecond
	// interruptBound is how long an interrupted 30s run may take at most.
	interruptBound = 5 * time.Second
	// progressProbe outlasts runHeadlessProgress's 2s tick, so a progress
	// line would show up if the non-TTY path printed one.
	progressProbe = "2500ms"
)

// 情境 5：只有「沒加 --no-tui 且是終端」才開畫面。
func TestShouldUseTUI(t *testing.T) {
	cases := []struct {
		noTUI, isTTY, want bool
	}{
		{false, true, true},
		{false, false, false},
		{true, true, false},
		{true, false, false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, shouldUseTUI(c.noTUI, c.isTTY), "noTUI=%v isTTY=%v", c.noTUI, c.isTTY)
	}
}

// 情境 1：TTY 下 run --url 開即時畫面，畫面拿得到持續增加的數字。
func TestRunLiveURLStartsTUIOnTTY(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, true, nil)
	srv := newDelayServer(t)

	var err error
	out := captureStdout(t, func() { _, _, err = execRun(t, "--url", srv.URL, "-d", "1s") })
	require.NoError(t, err)

	assert.Equal(t, 1, fake.calls(), "TTY 下要開 1 次即時畫面")
	assert.Positive(t, fake.maxTotal, "畫面要看得到請求數增加")
	assert.Contains(t, out, summaryMark, "畫面結束後要印摘要")
	assert.Len(t, historyFiles(t, dir), 1, "正常跑完照樣存紀錄")
}

// 情境 2：TTY 下 run --rps 開即時畫面，看得到三個階段。
func TestRunLiveRPSStartsTUIOnTTY(t *testing.T) {
	useHistoryDir(t)
	fake := useFakeLive(t, true, nil)
	srv := newDelayServer(t)

	var err error
	out := captureStdout(t, func() { _, _, err = execRun(t, "--url", srv.URL, "--rps", "5", "-d", "1s") })
	require.NoError(t, err)

	assert.Equal(t, 1, fake.calls())
	assert.Equal(t, 3, fake.stageTot, "rate 模式是 ramp／hold／ramp-down 三階段")
	assert.Positive(t, fake.maxTotal)
	assert.Contains(t, out, summaryMark)
}

// 情境 3（負向）：非 TTY 不開畫面，也不多印進度行（D1）。
func TestRunLiveNonTTYSkipsTUI(t *testing.T) {
	cases := []struct {
		name    string
		mode    []string
		running string // 改動前就有的開頭行
	}{
		{"vus", nil, "Running 1 VUs for " + progressProbe + " → GET "},
		{"rps", []string{"--rps", "5"}, "Running rate mode: 5 req/s for " + progressProbe + " → GET "},
	}
	for _, c := range cases {
		mode := c.mode
		t.Run(c.name, func(t *testing.T) {
			useHistoryDir(t)
			fake := useFakeLive(t, false, nil)
			srv := newDelayServer(t)

			args := append([]string{"--url", srv.URL, "-d", progressProbe}, mode...)
			var err error
			out := captureStdout(t, func() { _, _, err = execRun(t, args...) })
			require.NoError(t, err)

			assert.Zero(t, fake.calls(), "非 TTY 不可開畫面")
			assert.NotContains(t, out, "VUs=", "非 TTY 的 stdout 要跟改動前一樣，不印進度行")
			assert.Contains(t, out, c.running, "開頭行要跟改動前一樣")
			assert.Contains(t, out, summaryMark)
			for _, frame := range []string{"╭", "╰", "█", "░"} {
				assert.NotContains(t, out, frame, "非 TTY 不可以出現 TUI 的框線或進度條")
			}
		})
	}
}

// 情境 4（負向）：TTY 但加 --no-tui，不開畫面。
func TestRunLiveNoTUIFlag(t *testing.T) {
	useHistoryDir(t)
	fake := useFakeLive(t, true, nil)
	srv := newDelayServer(t)

	var err error
	captureStdout(t, func() { _, _, err = execRun(t, "--url", srv.URL, "-d", quickRun, "--no-tui") })
	require.NoError(t, err)
	assert.Zero(t, fake.calls())
}

// assertInterrupted checks the shared contract of an interrupted run: it
// stopped early, still printed the summary, saved no history and failed.
func assertInterrupted(t *testing.T, dir string, elapsed time.Duration, out, stderr string, err error) {
	t.Helper()
	assert.Less(t, elapsed, interruptBound, "中斷後要提早結束，不是跑滿 30 秒")
	assert.Contains(t, out, summaryMark, "中斷也要印摘要")
	assert.Contains(t, stderr, "已中斷")
	assert.NotContains(t, stderr, "Usage:", "中斷不是用法錯誤")
	assert.Empty(t, historyFiles(t, dir), "被中斷的 run 不存自動紀錄")
	require.Error(t, err, "被中斷的 run 要 exit 1（D2）")
	assert.Contains(t, err.Error(), "中斷")
}

func runInterrupted(t *testing.T, fake *fakeLive, sig os.Signal, args ...string) (out, stderr string, elapsed time.Duration, err error) {
	t.Helper()
	fake.sendAfter(interruptAfter, sig)
	start := time.Now()
	out = captureStdout(t, func() { _, stderr, err = execRun(t, args...) })
	return out, stderr, time.Since(start), err
}

// 情境 6：非 TTY 下 Ctrl-C。
func TestRunLiveInterruptURL(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	srv := newDelayServer(t)

	out, stderr, elapsed, err := runInterrupted(t, fake, os.Interrupt, "--url", srv.URL, "-d", longRun)
	assertInterrupted(t, dir, elapsed, out, stderr, err)
}

// 情境 7：非 TTY 下 SIGTERM。
func TestRunLiveSigtermURL(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	srv := newDelayServer(t)

	out, stderr, elapsed, err := runInterrupted(t, fake, syscall.SIGTERM, "--url", srv.URL, "-d", longRun)
	assertInterrupted(t, dir, elapsed, out, stderr, err)
}

// 情境 8：rate 模式中斷。
func TestRunLiveInterruptRPS(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	srv := newDelayServer(t)

	out, stderr, elapsed, err := runInterrupted(t, fake, os.Interrupt, "--url", srv.URL, "--rps", "5", "-d", longRun)
	assertInterrupted(t, dir, elapsed, out, stderr, err)
}

// 情境 9：畫面裡按 Ctrl-C（TUI 呼叫它拿到的 cancel 後結束）。
func TestRunLiveTUICtrlC(t *testing.T) {
	dir := useHistoryDir(t)
	useFakeLive(t, true, func(_ reporter.LiveProvider, cancel context.CancelFunc, _ <-chan struct{}) error {
		time.Sleep(interruptAfter)
		cancel()
		return nil
	})
	srv := newDelayServer(t)

	start := time.Now()
	var stderr string
	var err error
	out := captureStdout(t, func() { _, stderr, err = execRun(t, "--url", srv.URL, "-d", longRun) })
	assertInterrupted(t, dir, time.Since(start), out, stderr, err)
}

// 情境 10：畫面開著時收到 SIGTERM，引擎要停、畫面收到 done 結束。
func TestRunLiveTUISigterm(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, true, nil)
	srv := newDelayServer(t)

	out, stderr, elapsed, err := runInterrupted(t, fake, syscall.SIGTERM, "--url", srv.URL, "-d", longRun)
	assertInterrupted(t, dir, elapsed, out, stderr, err)
	assert.Equal(t, 1, fake.calls())
}

// 情境 11：中斷時使用者明確要的輸出照樣產生。
func TestRunLiveInterruptKeepsOutputs(t *testing.T) {
	useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	srv := newDelayServer(t)
	tmp := t.TempDir()
	jsonOut := filepath.Join(tmp, "x.json")
	htmlOut := filepath.Join(tmp, "x.html")

	_, _, _, err := runInterrupted(t, fake, os.Interrupt,
		"--url", srv.URL, "-d", longRun, "--output", jsonOut, "--report", htmlOut)
	require.Error(t, err)
	assert.FileExists(t, jsonOut)
	assert.FileExists(t, htmlOut)
}

// 情境 12（D3）：scenario 被中斷也不存紀錄、exit 1，不看 thresholds。
func TestRunLiveInterruptScenario(t *testing.T) {
	dir := useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	srv := newDelayServer(t)

	yaml := fmt.Sprintf(`name: interrupt me
stages:
  - duration: 30s
    target: 2
steps:
  - name: GET ok
    method: GET
    url: %s/
thresholds:
  error_rate_pct: 100
`, srv.URL)
	path := filepath.Join(t.TempDir(), "s.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	out, stderr, elapsed, err := runInterrupted(t, fake, os.Interrupt, "--scenario", path)
	assertInterrupted(t, dir, elapsed, out, stderr, err)
}

// 情境 13：畫面結束後，摘要與自動比較各一次、順序固定。
func TestRunLiveOutputOrder(t *testing.T) {
	useHistoryDir(t)
	const tuiMark = "<<TUI-FRAME>>"
	srv := newDelayServer(t)
	args := []string{"--url", srv.URL, "-d", quickRun}

	useFakeLive(t, false, nil)
	_, _, err := execRun(t, args...) // 先存一筆，第二次才有「跟上一次比」
	require.NoError(t, err)

	fake := useFakeLive(t, true, nil)
	inner := fake.tui
	fake.tui = func(p reporter.LiveProvider, cancel context.CancelFunc, done <-chan struct{}) error {
		fmt.Fprintln(os.Stdout, tuiMark)
		return inner(p, cancel, done)
	}

	out := captureStdout(t, func() {
		cmd := newRunCmd() // 不設 SetOut：紀錄訊息也走 os.Stdout，才能比順序
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		err = cmd.Execute()
	})
	require.NoError(t, err)

	order := []string{"Running", tuiMark, summaryMark, "跟上一次"}
	last := -1
	for _, mark := range order {
		assert.Equal(t, 1, strings.Count(out, mark), "%q 要剛好出現 1 次", mark)
		idx := strings.Index(out, mark)
		assert.Greater(t, idx, last, "%q 的位置不對：\n%s", mark, out)
		last = idx
	}
}

// 情境 16（回歸）：分散式換成共用的 watchRun 後，非 TTY 照樣跑完、不開畫面、exit 0。
func TestRunDistributedUsesWatchRun(t *testing.T) {
	useHistoryDir(t)
	fake := useFakeLive(t, false, nil)
	target := newDelayServer(t)
	worker := httptest.NewServer(distributed.NewWorker("test-worker").Handler())
	t.Cleanup(worker.Close)

	yaml := fmt.Sprintf(`name: dist
stages:
  - duration: 1s
    target: 1
steps:
  - name: GET ok
    method: GET
    url: %s/
`, target.URL)
	path := filepath.Join(t.TempDir(), "d.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	var err error
	out := captureStdout(t, func() {
		_, _, err = execRun(t, "--scenario", path, "--worker", worker.URL, "--poll-interval", "200ms")
	})
	require.NoError(t, err)
	assert.Zero(t, fake.calls())
	assert.Contains(t, out, summaryMark)
}

type zeroProvider struct{}

func (zeroProvider) Snapshot() reporter.LiveSnapshot { return reporter.LiveSnapshot{} }

// R2：TUI 無法初始化（例如沒有 /dev/tty，回錯誤）時，run 照樣跑完、不算中斷（跟改動前一致）。
func TestWatchRunTUIErrorKeepsRunning(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	env := liveEnv{
		isTTY: func() bool { return true },
		runTUI: func(reporter.LiveProvider, context.CancelFunc, <-chan struct{}) error {
			return fmt.Errorf("no tty input")
		},
		signals: func() (<-chan os.Signal, func()) { return sigs, func() {} },
	}
	done := make(chan struct{})
	cancelled := make(chan struct{})
	go func() { time.Sleep(200 * time.Millisecond); close(done) }()

	interrupted := watchRun(env, watchOpts{}, zeroProvider{}, func() { close(cancelled) }, done)

	assert.False(t, interrupted)
	select {
	case <-cancelled:
		t.Fatal("TUI 失敗不可以取消 run")
	default:
	}
}

// R2：TUI 無法初始化（例如沒有 /dev/tty）時，訊號仍能停止壓測。
// M-1：收到第一個訊號後，要先釋放訊號（第二次 Ctrl-C 才會恢復預設、強制結束）再取消 run。
func TestWatchRunSignalReleasedBeforeStop(t *testing.T) {
	cases := []struct {
		name  string
		isTTY bool
	}{
		{"TUI 初始化失敗", true},
		{"非 TTY", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sigs := make(chan os.Signal, 1)
			var mu sync.Mutex
			released := 0
			env := liveEnv{
				isTTY: func() bool { return c.isTTY },
				runTUI: func(reporter.LiveProvider, context.CancelFunc, <-chan struct{}) error {
					return fmt.Errorf("could not open a new TTY")
				},
				signals: func() (<-chan os.Signal, func()) {
					return sigs, func() { mu.Lock(); released++; mu.Unlock() }
				},
			}
			done := make(chan struct{})
			var once sync.Once
			releasedAtCancel := -1
			cancel := func() {
				once.Do(func() {
					mu.Lock()
					releasedAtCancel = released
					mu.Unlock()
					close(done)
				})
			}
			sigs <- os.Interrupt

			start := time.Now()
			interrupted := watchRun(env, watchOpts{}, zeroProvider{}, cancel, done)

			assert.True(t, interrupted)
			assert.Less(t, time.Since(start), interruptBound)
			assert.Positive(t, releasedAtCancel, "取消 run 之前就要先釋放訊號，第二次 Ctrl-C 才能強制結束")
		})
	}
}

// M-3：被中斷時不做門檻判定，--output 不可以寫進用部分結果算出的門檻失敗。
func TestRunLiveInterruptOutputHasNoThresholdVerdict(t *testing.T) {
	for _, ext := range []string{".json", ".xml"} {
		t.Run(ext, func(t *testing.T) {
			useHistoryDir(t)
			fake := useFakeLive(t, false, nil)
			srv := newDelayServer(t)
			srv.delay.Store(int64(5 * time.Millisecond))

			// p50_ms: 1 在 5ms 的目標上一定會被觸發。先 100ms 爬到 2 VU：
			// 單一 stage 會從 0 VU 線性爬升，中斷前可能一個請求都沒有。
			yaml := fmt.Sprintf(`name: interrupt threshold
stages:
  - duration: 100ms
    target: 2
  - duration: 30s
    target: 2
steps:
  - name: GET slow
    method: GET
    url: %s/
thresholds:
  p50_ms: 1
`, srv.URL)
			tmp := t.TempDir()
			path := filepath.Join(tmp, "s.yaml")
			require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
			out := filepath.Join(tmp, "result"+ext)

			_, _, _, err := runInterrupted(t, fake, os.Interrupt, "--scenario", path, "--output", out)
			require.ErrorIs(t, err, errRunInterrupted)

			raw, readErr := os.ReadFile(out)
			require.NoError(t, readErr)
			assert.NotContains(t, string(raw), "p50 ", "中斷的結果不做門檻判定")
			assert.NotContains(t, string(raw), "<failure", "JUnit 不可以記成門檻失敗")
		})
	}
}

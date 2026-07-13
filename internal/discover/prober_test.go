package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeSequence(t *testing.T) {
	tests := []struct {
		name   string
		maxRPS int
		want   []int
	}{
		{
			name:   "maxRPS 恰好是最後一個 base 值不重複附加",
			maxRPS: 2000,
			want:   []int{5, 10, 20, 40, 75, 125, 200, 300, 500, 750, 1000, 1500, 2000},
		},
		{
			name:   "maxRPS 恰好是中段 base 值不重複附加",
			maxRPS: 500,
			want:   []int{5, 10, 20, 40, 75, 125, 200, 300, 500},
		},
		{
			name:   "maxRPS 落在兩 base 值之間附加於尾",
			maxRPS: 100,
			want:   []int{5, 10, 20, 40, 75, 100},
		},
		{
			name:   "maxRPS 小於第一個 base 值只回自己",
			maxRPS: 3,
			want:   []int{3},
		},
		{
			name:   "maxRPS 大於所有 base 值附加於尾",
			maxRPS: 5000,
			want:   []int{5, 10, 20, 40, 75, 125, 200, 300, 500, 750, 1000, 1500, 2000, 5000},
		},
		{
			name:   "maxRPS 為 1 只回自己",
			maxRPS: 1,
			want:   []int{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ProbeSequence(tt.maxRPS))
		})
	}
}

func TestClassify(t *testing.T) {
	const tol = 100 * time.Millisecond
	tests := []struct {
		name      string
		p99       time.Duration
		errorRate float64
		want      ProbeStatus
	}{
		{"延遲與錯誤率皆在容差內判 PASS", 50 * time.Millisecond, 0, ProbePass},
		{"p99 恰等於容差不算超過判 PASS", tol, 0, ProbePass},
		{"錯誤率略低於 1% 判 PASS", 50 * time.Millisecond, 0.99, ProbePass},
		{"p99 略超過容差判 WARN", tol + time.Millisecond, 0, ProbeWarn},
		{"p99 恰等於 1.5 倍容差不算超過判 WARN", tol * 3 / 2, 0, ProbeWarn},
		{"錯誤率達 1% 判 WARN", 50 * time.Millisecond, 1.0, ProbeWarn},
		{"錯誤率略低於 3% 判 WARN", 50 * time.Millisecond, 2.99, ProbeWarn},
		{"p99 超過 1.5 倍容差判 FAIL", tol*3/2 + time.Millisecond, 0, ProbeFail},
		{"錯誤率達 3% 判 FAIL", 50 * time.Millisecond, 3.0, ProbeFail},
		{"延遲與錯誤率同時超標判 FAIL", tol * 2, 5.0, ProbeFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classify(tt.p99, tt.errorRate, tol))
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	p := New(Config{URL: "http://example.com"})
	assert.Equal(t, "GET", p.cfg.Method)
	assert.Equal(t, 2*time.Second, p.cfg.Tolerance)
	assert.Equal(t, 500, p.cfg.MaxRPS)
	assert.Equal(t, 15*time.Second, p.cfg.ProbeDuration)
	assert.NotNil(t, p.executor, "executor 應被建立")
}

func TestNewKeepsExplicitConfig(t *testing.T) {
	p := New(Config{
		URL:           "http://example.com",
		Method:        "POST",
		Tolerance:     time.Second,
		MaxRPS:        100,
		ProbeDuration: 5 * time.Second,
	})
	assert.Equal(t, "POST", p.cfg.Method)
	assert.Equal(t, time.Second, p.cfg.Tolerance)
	assert.Equal(t, 100, p.cfg.MaxRPS)
	assert.Equal(t, 5*time.Second, p.cfg.ProbeDuration)
}

// probeCollector 執行緒安全地收集 Run 的回調。
type probeCollector struct {
	mu      sync.Mutex
	starts  []int
	results []ProbeResult
}

func (c *probeCollector) onStart(rps int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, rps)
}

func (c *probeCollector) onProbe(pr ProbeResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results = append(c.results, pr)
}

func TestRunAllPassExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// ProbeDuration 刻意留較長時窗:最低 rps=5(限流間隔 200ms)需足夠時間累積
	// 樣本,否則慢 CI 上 sum.Total 可能為 0 而落入 ProbeFail 防禦分支導致偽失敗。
	p := New(Config{
		URL:           srv.URL,
		MaxRPS:        10, // seq = [5, 10]
		ProbeDuration: 1500 * time.Millisecond,
		Tolerance:     2 * time.Second,
	})

	c := &probeCollector{}
	res := p.Run(context.Background(), c.onStart, c.onProbe)

	require.Len(t, res.Probes, 2, "seq=[5,10] 應跑滿兩個 probe")
	assert.Equal(t, []int{5, 10}, c.starts, "onProbeStart 應按升序呼叫")
	assert.Len(t, c.results, 2, "onProbe 每個 probe 各呼叫一次")

	assert.Equal(t, 10, res.SafeLimit, "全 PASS 時安全上限為最高 rps")
	assert.Equal(t, 0, res.BreakingPoint, "未失敗時臨界點為 0")
	assert.True(t, res.Exhausted, "跑完全部 probe 未失敗應標記 Exhausted")

	for _, pr := range res.Probes {
		assert.Equal(t, ProbePass, pr.Status)
		assert.Greater(t, pr.Total, int64(0), "PASS 的 probe 必須有實際請求樣本")
	}
}

func TestRunFailEarlyStopsAndReportsBreakingPoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New(Config{
		URL:           srv.URL,
		MaxRPS:        20, // seq = [5, 10, 20];第一個就該失敗
		ProbeDuration: 1500 * time.Millisecond,
		Tolerance:     2 * time.Second,
	})

	c := &probeCollector{}
	res := p.Run(context.Background(), c.onStart, c.onProbe)

	require.Len(t, res.Probes, 1, "第一個 probe 全 500 即失敗,應提早停止")
	assert.Equal(t, []int{5}, c.starts)
	assert.Len(t, c.results, 1)

	assert.Equal(t, 5, res.BreakingPoint, "臨界點為第一個失敗的 rps")
	assert.Equal(t, 0, res.SafeLimit, "首個 probe 即失敗時無安全上限")
	assert.False(t, res.Exhausted, "提早失敗不算 Exhausted")
	assert.Equal(t, ProbeFail, res.Probes[0].Status)
}

func TestRunContextCancelledBeforeStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := New(Config{
		URL:           srv.URL,
		MaxRPS:        10,
		ProbeDuration: 600 * time.Millisecond,
		Tolerance:     2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	var starts int32
	res := p.Run(ctx, func(int) { atomic.AddInt32(&starts, 1) }, nil)

	assert.Empty(t, res.Probes, "ctx 已取消不應執行任何 probe")
	assert.Equal(t, int32(0), atomic.LoadInt32(&starts), "取消時 onProbeStart 不應被呼叫")
	assert.Equal(t, 0, res.SafeLimit)
	assert.Equal(t, 0, res.BreakingPoint)
	assert.False(t, res.Exhausted, "沒跑任何 probe 不算 Exhausted")
}

func TestRunNilCallbacksAndLowRPSWorkerFloor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// MaxRPS=1 → seq=[1] → targetRPS=1 → workerCount=5,觸發 <10 的下限鉗制。
	// 同時傳 nil 回調,驗證 Run 不因 nil 而 panic。
	// 1 rps 限流間隔達 1s,時窗需拉長以確保至少送出一個請求(避免 Total==0 偽失敗)。
	p := New(Config{
		URL:           srv.URL,
		MaxRPS:        1,
		ProbeDuration: 2500 * time.Millisecond,
		Tolerance:     2 * time.Second,
	})

	res := p.Run(context.Background(), nil, nil)

	require.Len(t, res.Probes, 1)
	assert.Equal(t, 1, res.SafeLimit)
	assert.True(t, res.Exhausted)
	assert.Equal(t, ProbePass, res.Probes[0].Status)
}

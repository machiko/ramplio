package metrics

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCollector_AccumulatesSamples(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 20 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 30 * time.Millisecond, Error: errors.New("timeout")})

	sum := c.Stop()

	assert.Equal(t, int64(3), sum.Total)
	assert.Equal(t, int64(1), sum.Errors)
	assert.Equal(t, 10*time.Millisecond, sum.MinLatency)
	assert.Equal(t, 30*time.Millisecond, sum.MaxLatency)
	assert.InDelta(t, 20.0, sum.MeanLatency().Milliseconds(), 1)
	assert.InDelta(t, 33.3, sum.ErrorRate(), 0.2)
}

func TestCollector_ConcurrentAdd(t *testing.T) {
	const n = 500
	c := NewCollector(n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Add(Sample{Latency: time.Millisecond, StatusCode: 200})
		}()
	}
	wg.Wait()

	sum := c.Stop()
	assert.Equal(t, int64(n), sum.Total)
}

func TestCollector_StopIsIdempotent(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 5 * time.Millisecond, StatusCode: 200})

	sum1 := c.Stop()
	sum2 := c.Stop() // 呼叫兩次不應 panic

	assert.Equal(t, sum1.Total, sum2.Total)
}

func TestCollector_AddAfterStop(t *testing.T) {
	c := NewCollector(10)
	_ = c.Stop()
	// 不應 panic
	c.Add(Sample{Latency: time.Millisecond, StatusCode: 200})
}

func TestCollector_LiveSummary(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 20 * time.Millisecond, StatusCode: 200})

	// Give the aggregate goroutine time to process.
	time.Sleep(20 * time.Millisecond)

	snap := c.LiveSummary()
	assert.Equal(t, int64(2), snap.Total)
	assert.Greater(t, snap.WallTime, time.Duration(0))
	assert.Greater(t, snap.RPS(), 0.0)

	_ = c.Stop()
}

// BenchmarkCollector_WriteWithLiveReads validates the DoD for M5: WebSocket reads
// (LiveSummary + LivePercentiles) must not significantly impede the write path.
// Run with: go test -bench=. -benchtime=3s ./internal/metrics/
func BenchmarkCollector_WriteWithLiveReads(b *testing.B) {
	c := NewCollector(1000)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ { // simulate 10 concurrent dashboard clients
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = c.LiveSummary()
					_, _, _, _ = c.LivePercentiles()
				}
			}
		}()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Add(Sample{Latency: time.Millisecond, StatusCode: 200})
	}
	b.StopTimer()

	close(stop)
	wg.Wait()
	_ = c.Stop()
}

func TestCollector_LivePercentiles_ConcurrentSafety(t *testing.T) {
	const n = 200
	c := NewCollector(n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Add(Sample{Latency: 5 * time.Millisecond, StatusCode: 200})
		}()
	}

	// Concurrently read live percentiles while writes are in-flight.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			p50, _, _, _ := c.LivePercentiles()
			_ = p50
		}
	}()

	wg.Wait()
	<-done

	sum := c.Stop()
	assert.Equal(t, int64(n), sum.Total)
}

// TTFT(串流首 chunk 到達)獨立 histogram:有 TTFT 樣本才出現,
// 非串流測試 HasTTFT 恆 false(不適用時缺席,比照 CO 修正慣例)。
func TestCollector_TTFTPercentiles(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 200 * time.Millisecond, TTFT: 50 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 300 * time.Millisecond, TTFT: 60 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 400 * time.Millisecond, TTFT: 70 * time.Millisecond, StatusCode: 200})

	sum := c.Stop()

	assert.True(t, sum.HasTTFT)
	assert.InDelta(t, 60, sum.TTFTP50.Milliseconds(), 5)
	assert.InDelta(t, 70, sum.TTFTP99.Milliseconds(), 5)
	// TTFT 必然 ≤ 總延遲的對應百分位
	assert.Less(t, sum.TTFTP99, sum.P99)
}

func TestCollector_NoTTFTSamplesNoTTFT(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200})

	sum := c.Stop()

	assert.False(t, sum.HasTTFT, "無 TTFT 樣本時 HasTTFT 應為 false")
}

// 混合場景(部分步驟串流):TTFT histogram 只收串流樣本。
func TestCollector_MixedStreamAndPlain(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 100 * time.Millisecond, TTFT: 40 * time.Millisecond, StatusCode: 200})
	c.Add(Sample{Latency: 10 * time.Millisecond, StatusCode: 200}) // 非串流

	sum := c.Stop()

	assert.True(t, sum.HasTTFT)
	assert.InDelta(t, 40, sum.TTFTP50.Milliseconds(), 5)
}

// 審查關發現(HIGH):rate 模式下 generator 追不上排程時,TTFT 若從實際
// 發送起算會系統性低報——corrected_latency 飆高而 ttft 看似健康,兩數字
// 互相矛盾。修正:rate 模式 TTFT 從排定時刻起算(與 CO 修正同一模型)。
func TestCollector_TTFTCoordinatedOmissionCorrection(t *testing.T) {
	c := NewCollector(10)
	now := time.Now()
	// 排定 200ms 前就該送出:排隊 200ms 後才實際發送,
	// 發送後 50ms 首 chunk、總耗時 100ms(At = 完成時刻)。
	c.Add(Sample{
		Latency:     100 * time.Millisecond,
		TTFT:        50 * time.Millisecond,
		StatusCode:  200,
		At:          now,
		ScheduledAt: now.Add(-300 * time.Millisecond), // 排隊 200ms + 服務 100ms
	})

	sum := c.Stop()

	if !sum.HasTTFT {
		t.Fatal("HasTTFT 應為 true")
	}
	// 使用者實感的「開始回應」= 排隊 200ms + 首 chunk 50ms = 250ms
	assert.InDelta(t, 250, sum.TTFTP50.Milliseconds(), 10,
		"rate 模式 TTFT 應含排隊等待(從排定時刻起算)")
}

// VU 模式(無 ScheduledAt):TTFT 維持原始值,不受影響。
func TestCollector_TTFTNoScheduleNoCorrection(t *testing.T) {
	c := NewCollector(10)
	c.Add(Sample{Latency: 100 * time.Millisecond, TTFT: 50 * time.Millisecond, StatusCode: 200, At: time.Now()})

	sum := c.Stop()

	if !sum.HasTTFT {
		t.Fatal("HasTTFT 應為 true")
	}
	assert.InDelta(t, 50, sum.TTFTP50.Milliseconds(), 5)
}

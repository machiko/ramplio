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

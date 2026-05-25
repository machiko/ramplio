package metrics

import "time"

type Summary struct {
	Total      int64         `json:"total"`
	Errors     int64         `json:"errors"`
	MinLatency time.Duration `json:"-"`
	MaxLatency time.Duration `json:"-"`
	BytesIn    int64         `json:"bytes_in"`
	WallTime   time.Duration `json:"-"`

	// HDR-computed percentiles, populated by Collector.Stop()
	P50 time.Duration `json:"-"`
	P90 time.Duration `json:"-"`
	P95 time.Duration `json:"-"`
	P99 time.Duration `json:"-"`

	// Per-step breakdown; nil when no step names were recorded (single URL mode).
	Steps []StepSummary `json:"-"`
	// Per-group breakdown; nil when no group names were recorded.
	Groups []GroupSummary `json:"-"`

	sumLatency time.Duration
}

func (s *Summary) record(sample Sample) {
	s.Total++
	s.BytesIn += sample.BytesRead
	s.sumLatency += sample.Latency

	if sample.isError() {
		s.Errors++
	}
	if s.Total == 1 || sample.Latency < s.MinLatency {
		s.MinLatency = sample.Latency
	}
	if sample.Latency > s.MaxLatency {
		s.MaxLatency = sample.Latency
	}
}

func (s Summary) ErrorRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Errors) / float64(s.Total) * 100
}

func (s Summary) MeanLatency() time.Duration {
	if s.Total == 0 {
		return 0
	}
	return time.Duration(int64(s.sumLatency) / s.Total)
}

func (s Summary) RPS() float64 {
	if s.WallTime == 0 {
		return 0
	}
	return float64(s.Total) / s.WallTime.Seconds()
}

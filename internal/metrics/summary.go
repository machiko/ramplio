package metrics

import "time"

type Summary struct {
	Total      int64         `json:"total"`
	Errors     int64         `json:"errors"`
	MinLatency time.Duration `json:"-"`
	MaxLatency time.Duration `json:"-"`
	BytesIn    int64         `json:"bytes_in"`
	WallTime   time.Duration `json:"-"`

	// HDR-computed percentiles, populated by Collector.Stop(). These are the raw
	// service-time percentiles (time on the wire), identical in both VU and rate mode.
	P50 time.Duration `json:"-"`
	P90 time.Duration `json:"-"`
	P95 time.Duration `json:"-"`
	P99 time.Duration `json:"-"`

	// Coordinated-omission-corrected percentiles, measured from each request's
	// scheduled dispatch time rather than its actual send time. Populated only in
	// rate (open) mode; HasCorrected is false in VU mode. Under overload these
	// exceed the raw percentiles by the queueing delay the user really waits —
	// the honest latency that closed-loop generators systematically under-report.
	CorrectedP50 time.Duration `json:"-"`
	CorrectedP90 time.Duration `json:"-"`
	CorrectedP95 time.Duration `json:"-"`
	CorrectedP99 time.Duration `json:"-"`
	HasCorrected bool          `json:"-"`

	// Per-step breakdown; nil when no step names were recorded (single URL mode).
	Steps []StepSummary `json:"-"`
	// Per-group breakdown; nil when no group names were recorded.
	Groups []GroupSummary `json:"-"`
	// DroppedSamples is the number of samples discarded because the collector channel was full.
	DroppedSamples int64 `json:"-"`

	// ErrorBreakdown counts failed requests by cause (DNS, connection refused,
	// timeout, TLS, HTTP 4xx/5xx, assertion, …). Populated lazily on the first
	// failure; nil when there were no errors. Successes are never recorded here.
	ErrorBreakdown map[ErrorKind]int64 `json:"-"`

	sumLatency time.Duration
}

func (s *Summary) record(sample Sample) {
	s.Total++
	s.BytesIn += sample.BytesRead
	s.sumLatency += sample.Latency

	if sample.isError() {
		s.Errors++
		if s.ErrorBreakdown == nil {
			s.ErrorBreakdown = make(map[ErrorKind]int64)
		}
		s.ErrorBreakdown[ClassifyError(sample.Error, sample.StatusCode)]++
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

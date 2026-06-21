package metrics

import "time"

type Sample struct {
	Latency    time.Duration
	StatusCode int
	BytesRead  int64
	Error      error
	At         time.Time
	StepName   string // "" = URL mode; non-empty enables per-step bucketing
	Group      string // optional group name for aggregate group reporting
	// ScheduledAt is the time this request was *due* to be dispatched per the
	// target rate. Set only in rate (open) mode. The collector uses At-ScheduledAt
	// as the coordinated-omission-corrected latency: when the generator can't keep
	// up, a request waits in queue past its scheduled time, and that wait must be
	// counted as latency the user actually experiences. Zero in VU (closed-loop)
	// mode, where there is no schedule and thus no omission.
	ScheduledAt time.Time
}

func (s Sample) isError() bool {
	return s.Error != nil || s.StatusCode < 200 || s.StatusCode >= 300
}

package metrics

import "time"

type Sample struct {
	Latency    time.Duration
	StatusCode int
	BytesRead  int64
	Error      error
	At         time.Time
}

func (s Sample) isError() bool {
	return s.Error != nil || s.StatusCode < 200 || s.StatusCode >= 300
}

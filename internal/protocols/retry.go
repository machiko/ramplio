package protocols

import (
	"context"
	"time"
)

// RetryingExecutor wraps an inner Executor and retries failed requests
// according to the provided configuration.
type RetryingExecutor struct {
	inner     Executor
	maxCount  int
	onCodes   map[int]bool // empty = retry on any error
	backoff   time.Duration
}

// NewRetryingExecutor creates a RetryingExecutor around inner.
// count is the max number of retry attempts (not counting the first try).
// onCodes, when non-empty, restricts retries to the listed HTTP status codes.
// backoffMs is the fixed wait between attempts (0 = no wait).
func NewRetryingExecutor(inner Executor, count int, onCodes []int, backoffMs int) *RetryingExecutor {
	codes := make(map[int]bool, len(onCodes))
	for _, c := range onCodes {
		codes[c] = true
	}
	return &RetryingExecutor{
		inner:    inner,
		maxCount: count,
		onCodes:  codes,
		backoff:  time.Duration(backoffMs) * time.Millisecond,
	}
}

func (r *RetryingExecutor) Execute(ctx context.Context, req Request) Result {
	result := r.inner.Execute(ctx, req)
	for attempt := 0; attempt < r.maxCount; attempt++ {
		if !r.shouldRetry(result) {
			break
		}
		if r.backoff > 0 {
			select {
			case <-time.After(r.backoff):
			case <-ctx.Done():
				return result
			}
		}
		result = r.inner.Execute(ctx, req)
	}
	return result
}

func (r *RetryingExecutor) shouldRetry(res Result) bool {
	if res.Error != nil {
		return true
	}
	if len(r.onCodes) == 0 {
		return res.StatusCode < 200 || res.StatusCode >= 300
	}
	return r.onCodes[res.StatusCode]
}

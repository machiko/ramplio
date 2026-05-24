package protocols

import (
	"context"
	"time"
)

type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

type Result struct {
	StatusCode int
	Latency    time.Duration
	BytesRead  int64
	Error      error
}

type Executor interface {
	Execute(ctx context.Context, req Request) Result
}

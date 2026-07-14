package protocols

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// scriptedExecutor 依序回放預先寫好的結果,並記錄被呼叫次數。
type scriptedExecutor struct {
	results []Result
	calls   int
}

func (s *scriptedExecutor) Execute(_ context.Context, _ Request) Result {
	idx := s.calls
	s.calls++
	if idx >= len(s.results) {
		return s.results[len(s.results)-1] // 超出腳本重複最後一筆
	}
	return s.results[idx]
}

func TestRetryingExecutor(t *testing.T) {
	ok := Result{StatusCode: 200}
	serverErr := Result{StatusCode: 500}
	teapot := Result{StatusCode: 418}
	netErr := Result{Error: errors.New("connection refused")}

	tests := []struct {
		name      string
		results   []Result
		count     int
		onCodes   []int
		wantCalls int
		wantLast  Result
	}{
		{
			name:      "首次成功不重試",
			results:   []Result{ok},
			count:     3,
			wantCalls: 1,
			wantLast:  ok,
		},
		{
			name:      "傳輸錯誤重試到成功即停",
			results:   []Result{netErr, netErr, ok},
			count:     5,
			wantCalls: 3,
			wantLast:  ok,
		},
		{
			name:      "onCodes 為空時非 2xx 觸發重試",
			results:   []Result{serverErr, ok},
			count:     3,
			wantCalls: 2,
			wantLast:  ok,
		},
		{
			name:      "重試次數用盡回傳最後結果",
			results:   []Result{serverErr, serverErr, serverErr},
			count:     2,
			wantCalls: 3, // 首次 + 2 次重試
			wantLast:  serverErr,
		},
		{
			name:      "onCodes 指定時僅列出的狀態碼重試",
			results:   []Result{serverErr, ok},
			count:     3,
			onCodes:   []int{500},
			wantCalls: 2,
			wantLast:  ok,
		},
		{
			name:      "onCodes 指定時未列出的失敗碼不重試",
			results:   []Result{teapot},
			count:     3,
			onCodes:   []int{500, 502},
			wantCalls: 1,
			wantLast:  teapot,
		},
		{
			name:      "onCodes 指定時傳輸錯誤仍重試",
			results:   []Result{netErr, ok},
			count:     3,
			onCodes:   []int{500},
			wantCalls: 2,
			wantLast:  ok,
		},
		{
			name:      "count 為 0 等同不重試",
			results:   []Result{serverErr},
			count:     0,
			wantCalls: 1,
			wantLast:  serverErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &scriptedExecutor{results: tt.results}
			r := NewRetryingExecutor(stub, tt.count, tt.onCodes, 0)

			got := r.Execute(context.Background(), Request{})

			assert.Equal(t, tt.wantCalls, stub.calls, "inner Execute 呼叫次數")
			assert.Equal(t, tt.wantLast.StatusCode, got.StatusCode)
			assert.Equal(t, tt.wantLast.Error, got.Error)
		})
	}
}

func TestRetryingExecutorBackoffWaits(t *testing.T) {
	stub := &scriptedExecutor{results: []Result{{StatusCode: 500}, {StatusCode: 200}}}
	r := NewRetryingExecutor(stub, 3, nil, 30) // 30ms backoff

	start := time.Now()
	got := r.Execute(context.Background(), Request{})
	elapsed := time.Since(start)

	assert.Equal(t, 200, got.StatusCode)
	assert.Equal(t, 2, stub.calls)
	assert.GreaterOrEqual(t, elapsed, 30*time.Millisecond, "重試前應等滿 backoff")
}

func TestRetryingExecutorContextCancelDuringBackoff(t *testing.T) {
	failed := Result{StatusCode: 503}
	stub := &scriptedExecutor{results: []Result{failed}}
	// backoff 長於 ctx 期限:取消應中止等待並回傳「最後一次」結果,不再重試。
	r := NewRetryingExecutor(stub, 5, nil, 5000)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	got := r.Execute(ctx, Request{})
	elapsed := time.Since(start)

	assert.Equal(t, 1, stub.calls, "取消後不應再打 inner")
	assert.Equal(t, 503, got.StatusCode, "應回傳取消前的最後結果")
	assert.Less(t, elapsed, time.Second, "不應等滿 5s backoff")
}

// 審查關發現(HIGH):101 是 WS 握手成功,retry 的「非 2xx 需重試」規則
// 是 isError()/ClassifyError 之外的第三個複製點,漏了豁免會讓每次
// 成功的 WS exchange 都被多打 N 次假重試。
func TestRetryingExecutorDoesNotRetryWS101(t *testing.T) {
	stub := &scriptedExecutor{results: []Result{{StatusCode: 101}}}
	rex := NewRetryingExecutor(stub, 3, nil, 0)

	res := rex.Execute(context.Background(), Request{})

	assert.NoError(t, res.Error)
	assert.Equal(t, 101, res.StatusCode)
	assert.Equal(t, 1, stub.calls, "成功的 WS exchange 不應觸發任何重試")
}

// Package observe 把壓測時間窗與被測系統的 trace 關聯,
// 回答「容量臨界點時,目標系統是哪裡先垮」。
// 不可妥協原則:取樣/資料不足時誠實回報「關聯不足」,
// 絕不硬給答案——錯誤歸因比不歸因更傷公信力。
package observe

import (
	"context"
	"time"
)

// Span 是跨後端正規化後的最小 trace 單位。
// 取捨(刻意):攤平所有 span、不保留 trace 分組與父子關係——
// 下游分析(p3-2)只需要 per-operation 的延遲分佈比較;
// 若未來要重建呼叫鏈,需擴充此結構,屆時是破壞性變更。
type Span struct {
	Operation string
	StartTime time.Time
	Duration  time.Duration
}

// FetchResult 除了 spans 也攜帶樣本品質中繼資料:
// 截斷與樣本量必須對下游可見,否則分析會建立在欠採樣的假象上。
type FetchResult struct {
	Spans      []Span
	TraceCount int
	// Truncated 為 true 表示後端回傳的 trace 數達到查詢上限,
	// 時間窗內可能還有更多資料——下游必須把這視為「樣本可能不完整」。
	Truncated bool
}

// TraceSource 拉取指定時間窗內的 spans。
// 介面刻意最小化:Jaeger 首發,Tempo 等後端之後以相同介面加入。
type TraceSource interface {
	FetchSpans(ctx context.Context, start, end time.Time) (FetchResult, error)
}

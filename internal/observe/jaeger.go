package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultTraceLimit 顯式覆蓋 Jaeger 的預設 limit=20——
// 20 條 trace 對壓測時間窗的統計分析嚴重欠採樣。
// 1000 低於常見後端上限(1500),再高就該縮小時間窗而非加大 limit。
const defaultTraceLimit = 1000

// defaultMaxResponseBytes 限制單次回應的讀取量:
// 壓測工具自身不可因吞下巨型回應而成為記憶體瓶頸(干擾同進程的量測)。
const defaultMaxResponseBytes = 32 << 20 // 32 MiB

// JaegerSource 透過 Jaeger query API(/api/traces)拉取 spans。
// 時間參數以微秒 epoch 表示(Jaeger API 慣例)。
type JaegerSource struct {
	baseURL  string
	service  string
	limit    int
	maxBytes int64
	client   *http.Client
}

// JaegerOption 供測試與進階場景覆寫預設值。
type JaegerOption func(*JaegerSource)

func WithTraceLimit(n int) JaegerOption {
	return func(j *JaegerSource) { j.limit = n }
}

func WithMaxResponseBytes(n int64) JaegerOption {
	return func(j *JaegerSource) { j.maxBytes = n }
}

func NewJaegerSource(baseURL, service string, opts ...JaegerOption) (*JaegerSource, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("jaeger source: base URL 不可為空")
	}
	if service == "" {
		return nil, fmt.Errorf("jaeger source: service 名稱不可為空(Jaeger 查詢必要參數)")
	}
	j := &JaegerSource{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		service:  service,
		limit:    defaultTraceLimit,
		maxBytes: defaultMaxResponseBytes,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
	for _, opt := range opts {
		opt(j)
	}
	return j, nil
}

// jaegerResponse 只解我們需要的欄位;Jaeger 的 startTime/duration 單位為微秒。
type jaegerResponse struct {
	Data []struct {
		Spans []struct {
			OperationName string `json:"operationName"`
			StartTime     int64  `json:"startTime"`
			Duration      int64  `json:"duration"`
		} `json:"spans"`
	} `json:"data"`
}

func (j *JaegerSource) FetchSpans(ctx context.Context, start, end time.Time) (FetchResult, error) {
	q := url.Values{}
	q.Set("service", j.service)
	q.Set("start", strconv.FormatInt(start.UnixMicro(), 10))
	q.Set("end", strconv.FormatInt(end.UnixMicro(), 10))
	q.Set("limit", strconv.Itoa(j.limit))
	endpoint := j.baseURL + "/api/traces?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("jaeger source: 建立請求失敗: %w", err)
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return FetchResult{}, fmt.Errorf("jaeger source: 查詢 %s 失敗: %w", j.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return FetchResult{}, fmt.Errorf("jaeger source: 回應 %d: %s", resp.StatusCode, snippet)
	}

	// LimitReader 多讀 1 byte 以區分「剛好等於上限」與「超過上限」。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, j.maxBytes+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("jaeger source: 讀取回應失敗: %w", err)
	}
	if int64(len(raw)) > j.maxBytes {
		return FetchResult{}, fmt.Errorf(
			"jaeger source: 回應超過 %d bytes 上限——請縮小時間窗或降低 trace limit", j.maxBytes)
	}

	var parsed jaegerResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return FetchResult{}, fmt.Errorf("jaeger source: 解析回應失敗(非有效 JSON): %w", err)
	}

	res := FetchResult{TraceCount: len(parsed.Data)}
	for _, trace := range parsed.Data {
		for _, s := range trace.Spans {
			res.Spans = append(res.Spans, Span{
				Operation: s.OperationName,
				StartTime: time.UnixMicro(s.StartTime),
				Duration:  time.Duration(s.Duration) * time.Microsecond,
			})
		}
	}
	// trace 數達到查詢上限 → 時間窗內可能還有更多資料,樣本或不完整。
	res.Truncated = res.TraceCount >= j.limit
	return res, nil
}

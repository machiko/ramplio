package observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// defaultSpansPerSet 顯式覆蓋 Tempo 的 spss(每 span-set 回傳 span 數)——
// 後端預設僅 3,壓測場景同一 trace 內重複 operation 會被系統性欠採樣。
const defaultSpansPerSet = 100

// TempoSource 透過 Grafana Tempo 的 search API(/api/search,TraceQL)拉取 spans。
// 與 Jaeger 的契約差異:時間參數用 unix 秒、以 TraceQL 過濾 service、
// uint64(奈秒時間戳/時長)依 protobuf-JSON 慣例以字串編碼。
type TempoSource struct {
	baseURL  string
	service  string
	limit    int
	maxBytes int64
	client   *http.Client
}

// TempoOption 供測試與進階場景覆寫預設值。
type TempoOption func(*TempoSource)

func WithTempoTraceLimit(n int) TempoOption {
	return func(s *TempoSource) { s.limit = n }
}

func WithTempoMaxResponseBytes(n int64) TempoOption {
	return func(s *TempoSource) { s.maxBytes = n }
}

func NewTempoSource(baseURL, service string, opts ...TempoOption) (*TempoSource, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("tempo source: base URL 不可為空")
	}
	if service == "" {
		return nil, fmt.Errorf("tempo source: service 名稱不可為空(TraceQL 過濾必要)")
	}
	// TraceQL 查詢是手刻字串:service 含特殊字元會破壞查詢語法
	//(%q 是 Go 逸出,不是 TraceQL 逸出,不可依賴),直接拒絕。
	if strings.ContainsAny(service, `"{}\`) {
		return nil, fmt.Errorf(`tempo source: service 名稱 %q 含 TraceQL 特殊字元(" { } \),不支援`, service)
	}
	s := &TempoSource{
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		service:  service,
		limit:    defaultTraceLimit,
		maxBytes: defaultMaxResponseBytes,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// tempoSpanSet 對應 Tempo 的 SpanSet;matched 是實際命中數,
// 大於回傳 span 數即代表被 spss 截斷。
type tempoSpanSet struct {
	Matched int `json:"matched"`
	Spans   []struct {
		Name              string `json:"name"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
		DurationNanos     string `json:"durationNanos"`
	} `json:"spans"`
}

// tempoResponse:官方現行欄位是 spanSets(複數);spanSet(單數)已棄用,
// 保留為舊版相容 fallback——只解單數欄位會在新版 Tempo 靜默遺失全部 spans。
type tempoResponse struct {
	Traces []struct {
		SpanSets []tempoSpanSet `json:"spanSets"`
		SpanSet  tempoSpanSet   `json:"spanSet"`
	} `json:"traces"`
}

func (t *TempoSource) FetchSpans(ctx context.Context, start, end time.Time) (FetchResult, error) {
	q := url.Values{}
	q.Set("q", fmt.Sprintf(`{resource.service.name=%q}`, t.service))
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("limit", strconv.Itoa(t.limit))
	q.Set("spss", strconv.Itoa(defaultSpansPerSet))
	endpoint := t.baseURL + "/api/search?" + q.Encode()

	raw, err := fetchLimited(ctx, t.client, endpoint, "tempo source", t.maxBytes)
	if err != nil {
		return FetchResult{}, err
	}

	var parsed tempoResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return FetchResult{}, fmt.Errorf("tempo source: 解析回應失敗(非有效 JSON): %w", err)
	}

	res := FetchResult{TraceCount: len(parsed.Traces)}
	spssTruncated := false
	for _, trace := range parsed.Traces {
		sets := trace.SpanSets
		if len(sets) == 0 && len(trace.SpanSet.Spans) > 0 {
			sets = []tempoSpanSet{trace.SpanSet} // 舊版單數欄位 fallback
		}
		for _, set := range sets {
			if set.Matched > len(set.Spans) {
				spssTruncated = true // spss 截斷:欠採樣不可隱形
			}
			for _, s := range set.Spans {
				startNano, perr := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
				if perr != nil {
					return FetchResult{}, fmt.Errorf("tempo source: startTimeUnixNano %q 非合法奈秒字串: %w", s.StartTimeUnixNano, perr)
				}
				durNano, perr := strconv.ParseInt(s.DurationNanos, 10, 64)
				if perr != nil {
					return FetchResult{}, fmt.Errorf("tempo source: durationNanos %q 非合法奈秒字串: %w", s.DurationNanos, perr)
				}
				res.Spans = append(res.Spans, Span{
					Operation: s.Name,
					StartTime: time.Unix(0, startNano),
					Duration:  time.Duration(durNano),
				})
			}
		}
	}
	res.Truncated = res.TraceCount >= t.limit || spssTruncated
	return res, nil
}

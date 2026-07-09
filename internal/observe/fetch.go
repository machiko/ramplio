package observe

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// defaultTraceLimit 顯式覆蓋後端的 trace 數預設上限(Jaeger 預設 20、
// Tempo 也有低預設)——對壓測時間窗的統計分析嚴重欠採樣。
// 1000 低於常見後端上限,再高就該縮小時間窗而非加大 limit。
const defaultTraceLimit = 1000

// defaultMaxResponseBytes 限制單次回應的讀取量:
// 壓測工具自身不可因吞下巨型回應而成為記憶體瓶頸(干擾同進程的量測)。
const defaultMaxResponseBytes = 32 << 20 // 32 MiB

// fetchLimited 是 TraceSource 實作共用的傳輸層:GET、狀態碼檢查、
// 大小上限讀取(LimitReader 多讀 1 byte 區分「恰好等於」與「超過」)。
// 查詢參數組裝與回應解析屬各後端契約,由呼叫端自理。
func fetchLimited(ctx context.Context, client *http.Client, endpoint, label string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: 建立請求失敗: %w", label, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: 查詢失敗: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s: 回應 %d: %s", label, resp.StatusCode, snippet)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: 讀取回應失敗: %w", label, err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%s: 回應超過 %d bytes 上限——請縮小時間窗或降低 trace limit", label, maxBytes)
	}
	return raw, nil
}

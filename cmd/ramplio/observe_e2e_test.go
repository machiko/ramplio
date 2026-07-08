package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/observe"
)

// Ground-truth 自證(整合版):假 Jaeger 注入已知瓶頸,
// 走「真 JaegerSource → 分析 → 白話輸出」全管線,結論必須指向注入的瓶頸。
// 歸因準不準是可驗證的數學問題——與 ramplio verify 同一哲學。
func TestObserveGroundTruthE2E(t *testing.T) {
	type jSpan struct {
		OperationName string `json:"operationName"`
		StartTime     int64  `json:"startTime"`
		Duration      int64  `json:"duration"`
	}
	spans := func(op string, n int, us int64) []jSpan {
		out := make([]jSpan, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, jSpan{OperationName: op, StartTime: 1700000000000000 + int64(i), Duration: us})
		}
		return out
	}

	var call atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start") == "" || r.URL.Query().Get("end") == "" {
			t.Error("每次查詢都必須帶時間窗參數")
		}
		var payload []jSpan
		if call.Add(1) == 1 { // 第一次呼叫 = 基準窗:健康
			payload = append(spans("SELECT orders", 25, 10_000), spans("GET /users", 25, 12_000)...)
		} else { // 第二次 = 臨界窗:SELECT orders 惡化 9 倍
			payload = append(spans("SELECT orders", 25, 90_000), spans("GET /users", 25, 13_000)...)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"spans": payload}},
		})
	}))
	defer srv.Close()

	src, err := observe.NewJaegerSource(srv.URL, "checkout")
	if err != nil {
		t.Fatalf("NewJaegerSource: %v", err)
	}

	var out, errW strings.Builder
	runObservation(&out, &errW, src, time.Now(), 2*time.Second, 4*time.Second)

	if errW.String() != "" {
		t.Fatalf("不應有警告: %q", errW.String())
	}
	report := out.String()
	if !strings.Contains(report, "瓶頸指向 SELECT orders") {
		t.Fatalf("全管線結論必須指向注入的瓶頸,實際:\n%s", report)
	}
	if !strings.Contains(report, "9.0 倍") {
		t.Fatalf("退化倍率應為注入的 9 倍,實際:\n%s", report)
	}
}

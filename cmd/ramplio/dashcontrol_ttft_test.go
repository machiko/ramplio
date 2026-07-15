package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

// GUI 上傳含 stream: sse 的場景:run 結束後結果必須附 TTFT 快照,
// 且開始回應 ≤ 完整回應(快照自帶配對基準,倒掛不可能)。
func TestDashControllerTTFT_StreamScenarioAttachesSnap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("data: first\n\n"))
		fl.Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	t.Cleanup(srv.Close)

	yaml := []byte(`
name: sse dash
stages:
  - duration: 2s
    target: 2
steps:
  - name: sse step
    method: POST
    url: ` + srv.URL + `
    stream: sse
`)
	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenario(yaml, ""); err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if err := c.Start(dashboard.RunRequest{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("Result 不應為 nil")
	}
	if res.TTFT == nil {
		t.Fatal("stream 場景結果應附 TTFT 快照")
	}
	if res.TTFT.P50Ms < 20 {
		t.Errorf("TTFT p50 %dms 低於注入的首段延遲 20ms", res.TTFT.P50Ms)
	}
	if res.TTFT.P50Ms > res.TTFT.FullP50Ms || res.TTFT.P99Ms > res.TTFT.FullP99Ms {
		t.Errorf("開始回應不可大於完整回應: %+v", res.TTFT)
	}
}

// 非串流 run:TTFT 缺席。
func TestDashControllerTTFT_PlainRunNoSnap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: srv.URL, VUs: 2, Duration: "2s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	if res := c.Result(); res == nil || res.TTFT != nil {
		t.Errorf("非串流 run 不應附 TTFT 快照: %+v", res)
	}
}

// 複審 HIGH 的橋接:rate 模式下 headline KPI(原始值)與 TTFT 卡
// (實感值)同屏,RunResult 必須透傳 corrected 數字讓前端標示區分,
// 否則同一頁兩個「p99」互相衝突,使用者會以為數字算錯。
func TestDashControllerRate_CarriesCorrectedLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: srv.URL, RPS: 20, Duration: "3s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("Result 不應為 nil")
	}
	if res.CorrectedP99Ms <= 0 {
		t.Error("rate 模式應透傳使用者實感 p99(CO 修正值)")
	}
	if res.CorrectedP99Ms < res.P99Ms {
		t.Errorf("實感值不可小於原始值: corrected=%d raw=%d", res.CorrectedP99Ms, res.P99Ms)
	}
}

// VU 模式:無 CO 修正概念,corrected 欄位缺席(零值)。
func TestDashControllerVU_NoCorrectedLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: srv.URL, VUs: 2, Duration: "2s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	if res := c.Result(); res == nil || res.CorrectedP99Ms != 0 {
		t.Errorf("VU 模式不應有 corrected 欄位: %+v", res)
	}
}

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/observe"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

// fakeTraceSource 回傳空 spans:接線正確時結果仍須附上
// insufficient 快照(觀測有跑、樣本不足),而不是卡片缺席。
type fakeTraceSource struct {
	calls atomic.Int32
}

func (f *fakeTraceSource) FetchSpans(_ context.Context, _, _ time.Time) (observe.FetchResult, error) {
	f.calls.Add(1)
	return observe.FetchResult{}, nil
}

func waitDashDone(t *testing.T, c *dashController, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.State() == dashboard.StateDone {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("測試超時:%v 內未進入 done 狀態(state=%s)", timeout, c.State())
}

// rate 模式 + observe 來源:結果必須帶觀測快照,且兩窗各拉一次。
func TestDashControllerObserve_RateModeAttachesSnap(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	src := &fakeTraceSource{}
	c := newDashController(protocols.DefaultHTTPConfig(), src)
	if err := c.Start(dashboard.RunRequest{URL: target.URL, RPS: 20, Duration: "4s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("run 結束後 Result 不應為 nil")
	}
	if res.Observe == nil {
		t.Fatal("rate 模式且有 observe 來源時,結果應附觀測快照")
	}
	if res.Observe.Status != string(observe.StatusInsufficient) {
		t.Errorf("空 spans 應為 insufficient,得到 %q", res.Observe.Status)
	}
	if got := src.calls.Load(); got != 2 {
		t.Errorf("應拉取基準/臨界兩窗各一次,實際 %d 次", got)
	}
}

// VU 模式沒有負載輪廓窗口:即使有 observe 來源也不觀測、不附卡片。
func TestDashControllerObserve_VUModeSkips(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	src := &fakeTraceSource{}
	c := newDashController(protocols.DefaultHTTPConfig(), src)
	if err := c.Start(dashboard.RunRequest{URL: target.URL, VUs: 2, Duration: "3s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("run 結束後 Result 不應為 nil")
	}
	if res.Observe != nil {
		t.Errorf("VU 模式不應附觀測快照,得到 %+v", res.Observe)
	}
	if got := src.calls.Load(); got != 0 {
		t.Errorf("VU 模式不應拉取 trace,實際 %d 次", got)
	}
}

// 審查關發現(HIGH):startGuided 提前 return 繞過 Start() 的窗口重置,
// 上一輪 rate 模式的殘留窗口會讓 guided run 誤觸發觀測(窗口與 guided
// 的 stage 配置毫無對應,語意錯誤且多卡 30 秒)。
// 釘住不變量:任何啟動路徑取得寫鎖後,觀測窗口都必須先歸零。
func TestDashControllerObserve_GuidedResetsStaleWindow(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	src := &fakeTraceSource{}
	c := newDashController(protocols.DefaultHTTPConfig(), src)

	// 模擬上一輪 rate run 的殘留窗口
	c.mu.Lock()
	c.obsRampDur, c.obsHoldDur = time.Second, 2*time.Second
	c.mu.Unlock()

	err := c.Start(dashboard.RunRequest{Profile: &dashboard.GuidedProfile{
		URL:             target.URL,
		ConcurrentUsers: 2,
		TrafficShape:    "steady",
	}})
	if err != nil {
		t.Fatalf("startGuided: %v", err)
	}
	defer func() {
		c.Stop()
		waitDashDone(t, c, 30*time.Second)
	}()

	c.mu.RLock()
	ramp, hold := c.obsRampDur, c.obsHoldDur
	c.mu.RUnlock()
	if ramp != 0 || hold != 0 {
		t.Errorf("guided 啟動後殘留窗口應歸零,得到 ramp=%v hold=%v", ramp, hold)
	}
}

// 審查關發現(MEDIUM 順修):使用者主動 Stop 的 run 不觀測——
// 窗口是照原計畫時長推導的,提前中止後與實際負載不符,觀測數字不可信;
// 同時避免 Stop 後還卡 30 秒拉 trace 才進 done。
func TestDashControllerObserve_StoppedRunSkipsObservation(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	src := &fakeTraceSource{}
	c := newDashController(protocols.DefaultHTTPConfig(), src)
	if err := c.Start(dashboard.RunRequest{URL: target.URL, RPS: 20, Duration: "60s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	c.Stop()
	waitDashDone(t, c, 10*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("Stop 後 Result 不應為 nil")
	}
	if res.Observe != nil {
		t.Errorf("手動停止的 run 不應附觀測快照,得到 %+v", res.Observe)
	}
	if got := src.calls.Load(); got != 0 {
		t.Errorf("手動停止的 run 不應拉取 trace,實際 %d 次", got)
	}
}

// DSN 錯誤必須在 dashboard 啟動前攔截(fail fast),
// 不能等使用者跑完一輪壓測才發現觀測沒接上。
func TestRunDashboard_InvalidObserveDSNFailsFast(t *testing.T) {
	err := runDashboard("", "", 1, 0, "", "", 0, "", protocols.DefaultHTTPConfig(), "bogus://nope")
	if err == nil {
		t.Fatal("無效 DSN 應立即回傳錯誤")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Errorf("錯誤應指出 scheme 不支援,得到:%v", err)
	}
}

// 沒有 observe 來源:rate 模式也不觀測(功能未啟用)。
func TestDashControllerObserve_NilSourceSkips(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: target.URL, RPS: 20, Duration: "4s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("run 結束後 Result 不應為 nil")
	}
	if res.Observe != nil {
		t.Errorf("未啟用 observe 時不應附觀測快照,得到 %+v", res.Observe)
	}
}

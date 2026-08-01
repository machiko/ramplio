package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

// 慢端點(rps < 1)的 run 結束後,RunResult.Verdict.Capacity 必須帶
// RPM 換算——這是 rpm-1 的核心價值,前端結果頁靠這個欄位顯示
// 「≈ 每分鐘 N 個」,與 terminal 同一個伺服器來源,不得由前端自算。
func TestDashControllerSlowRun_CarriesCapacityRPM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: srv.URL, VUs: 1, Duration: "2s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("Result 不應為 nil")
	}
	if res.RPS >= 1 {
		t.Fatalf("測試前提失效:預期慢端點 rps < 1,得到 %.2f", res.RPS)
	}
	cap := res.Verdict.Capacity
	if cap.Value == "" {
		t.Error("Verdict.Capacity.Value 不應為空——結果頁吞吐量的單一來源")
	}
	if cap.RPM == "" {
		t.Error("慢端點結果應帶 RPM 換算,否則 GUI 缺 rpm-1 的每分鐘框架")
	}
	// Value 是一位小數的 sub-1 值(與 terminal 同 roundRate 來源)。
	if !strings.HasPrefix(cap.Value, "0.") {
		t.Errorf("sub-1 rps 的 Value 應為 0.x,得到 %q", cap.Value)
	}
}

// 快端點:RPM 換算缺席(rps ≥ 1 時每分鐘框架是雜訊)。
func TestDashControllerFastRun_NoCapacityRPM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.Start(dashboard.RunRequest{URL: srv.URL, VUs: 2, Duration: "2s"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDashDone(t, c, 30*time.Second)

	res := c.Result()
	if res == nil {
		t.Fatal("Result 不應為 nil")
	}
	if res.Verdict.Capacity.RPM != "" {
		t.Errorf("快端點不應帶 RPM 換算,得到 %q", res.Verdict.Capacity.RPM)
	}
}

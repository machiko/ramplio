package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/protocols"
)

// stopThenCleanUpYAML 2 VU 跑 hold,setup 取 token、teardown 帶 token 登出。
func stopThenCleanUpYAML(url string) []byte {
	return []byte(fmt.Sprintf(`name: stop then clean up
stages:
  - duration: 10ms
    target: 2
  - duration: 60s
    target: 2
setup:
  - name: login
    method: POST
    url: %[1]s/login
    capture:
      token: $.token
steps:
  - name: work
    method: GET
    url: %[1]s/work
teardown:
  - name: logout
    method: POST
    url: %[1]s/logout
    headers:
      Authorization: "Bearer {{capture.token}}"
`, url))
}

// 使用者在儀表板按「停止」會取消 run context;teardown(登出、刪測試資料)
// 仍必須送出,且帶著 setup 拿到的 token——否則測試資料殘留在目標系統。
func TestDashControllerStop_StillRunsTeardown(t *testing.T) {
	var (
		mu         sync.Mutex
		hits       = map[string]int{}
		logoutAuth string
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		if r.URL.Path == "/logout" {
			logoutAuth = r.Header.Get("Authorization")
		}
		mu.Unlock()
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}

	yaml := stopThenCleanUpYAML(target.URL)

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenario(yaml, ""); err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if err := c.Start(dashboard.RunRequest{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for count("/work") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Stop()
	waitDashDone(t, c, 15*time.Second)

	if got := count("/logout"); got != 1 {
		t.Errorf("停止後 teardown 應執行恰好 1 次,實際 %d 次", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if logoutAuth != "Bearer t" {
		t.Errorf("teardown 應帶 setup 的 token,得到 %q", logoutAuth)
	}
}

// 在終端按 Ctrl-C 結束整個儀表板行程時,進行中的測試也必須先停下並跑完
// teardown,而不是隨行程一起消失。
func TestShutdownDashboard_StopsRunAndCleansUp(t *testing.T) {
	var (
		mu   sync.Mutex
		hits = map[string]int{}
	)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/login" {
			_, _ = w.Write([]byte(`{"token":"t"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}

	c := newDashController(protocols.DefaultHTTPConfig(), nil)
	if err := c.LoadScenario(stopThenCleanUpYAML(target.URL), ""); err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if err := c.Start(dashboard.RunRequest{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for count("/work") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	var out bytes.Buffer
	shutdownDashboard(&out, c, 15*time.Second)

	if c.State() == dashboard.StateRunning {
		t.Fatal("shutdown 後測試不應仍在執行")
	}
	if got := count("/logout"); got != 1 {
		t.Errorf("行程中斷時 teardown 應執行恰好 1 次,實際 %d 次", got)
	}
	if !strings.Contains(out.String(), "teardown") {
		t.Errorf("應提示正在清理,得到 %q", out.String())
	}
}

func TestShutdownDashboard_IdleIsSilent(t *testing.T) {
	var out bytes.Buffer
	shutdownDashboard(&out, newDashController(protocols.DefaultHTTPConfig(), nil), time.Second)
	if out.Len() != 0 {
		t.Errorf("沒有進行中的測試時不應輸出,得到 %q", out.String())
	}
}

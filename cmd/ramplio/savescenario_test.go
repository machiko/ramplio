package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastDiscoverArgs keeps a real discover run to one 1.5 s probe at 5 rps, so a
// healthy target ends with SafeLimit = 5.
var fastDiscoverArgs = []string{"--probe-duration", "1500ms", "--max-rps", "5", "--tolerance", "2s"}

const fastSafeLimit = 5

func discoverArgs(extra ...string) []string {
	return append(append([]string{}, extra...), fastDiscoverArgs...)
}

func scenarioPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "out.yaml")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "不該產生 %s", path)
}

// 驗收情境 1：存出的檔案能直接被 scenarios 解析驗證，負載與門檻來自這次探測。
func TestDiscoverSaveScenarioWritesRunnableFile(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)

	out, err := execDiscover(t, "", discoverArgs("--url", srv.URL+"/health", "--save-scenario", path)...)
	require.NoError(t, err, out)

	sc, err := scenarios.ParseFile(path)
	require.NoError(t, err, "產物必須通過 scenarios 解析與驗證")

	require.Len(t, sc.Stages, 2)
	assert.Equal(t, 30*time.Second, sc.Stages[0].Duration, "先在 30s 內爬升")
	assert.Equal(t, fastSafeLimit, sc.Stages[0].TargetRPS)
	assert.Equal(t, 2*time.Minute, sc.Stages[1].Duration, "再維持 2m")
	assert.Equal(t, fastSafeLimit, sc.Stages[len(sc.Stages)-1].TargetRPS, "末段 target_rps = SafeLimit")

	require.Len(t, sc.Steps, 1)
	assert.Equal(t, "GET", sc.Steps[0].Method)
	assert.Equal(t, srv.URL+"/health", sc.Steps[0].URL)

	require.NotNil(t, sc.Thresholds)
	require.NotNil(t, sc.Thresholds.P99Ms)
	require.NotNil(t, sc.Thresholds.ErrorRatePct)
	assert.Equal(t, 2000.0, *sc.Thresholds.P99Ms, "p99_ms = tolerance 毫秒")
	assert.Equal(t, 1.0, *sc.Thresholds.ErrorRatePct)

	assert.Contains(t, out, "ramplio run --scenario "+path)
}

// 驗收情境 2：敏感 header 換成 env 引用；檔案與輸出都不含原值，輸出列出變數名稱。
func TestSaveScenarioMasksSensitiveHeaders(t *testing.T) {
	srv := newRecordingServer(t, "Bearer secret123", "")
	path := scenarioPath(t)
	curl := "curl '" + srv.URL + "' -H 'Authorization: Bearer secret123' -H 'X-Api-Key: k3yval' -H 'Cookie: sid=c00kie' -H 'Accept: application/json'"

	out, err := execDiscover(t, "", discoverArgs("--curl", curl, "--save-scenario", path)...)
	require.NoError(t, err, out)

	saved := readFile(t, path)
	for _, secret := range []string{"secret123", "k3yval", "c00kie"} {
		assert.NotContains(t, saved, secret, "檔案不得含 header 原值")
		assert.NotContains(t, out, secret, "輸出不得含 header 原值")
	}
	assert.Contains(t, saved, `{{env "RAMPLIO_H_AUTHORIZATION"}}`)
	assert.Contains(t, saved, `{{env "RAMPLIO_H_X_API_KEY"}}`)
	assert.Contains(t, saved, `{{env "RAMPLIO_H_COOKIE"}}`)
	assert.Contains(t, saved, "application/json", "非敏感 header 照原文保留")
	for _, name := range []string{"RAMPLIO_H_AUTHORIZATION", "RAMPLIO_H_X_API_KEY", "RAMPLIO_H_COOKIE"} {
		assert.Contains(t, out, name, "輸出要列出需要 export 的變數")
	}
}

// requestLog records the Authorization header and token query of every request.
type requestLog struct {
	mu     sync.Mutex
	auths  []string
	tokens []string
}

func (l *requestLog) handler(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	l.mu.Lock()
	l.auths = append(l.auths, r.Header.Get("Authorization"))
	l.tokens = append(l.tokens, r.URL.Query().Get("token"))
	l.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (l *requestLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.auths, l.tokens = nil, nil
}

func (l *requestLog) snapshot() (auths, tokens []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.auths...), append([]string(nil), l.tokens...)
}

var stageDurationRe = regexp.MustCompile(`(?m)^(\s*-?\s*)duration: \S+$`)

// 驗收情境 3：產生的檔案真的能跑，憑證從環境變數帶進 header 與 query。
// 只把 stage 時長縮短以免測試跑 2.5 分鐘，其餘內容與產物逐字相同。
func TestSavedScenarioRunsWithEnvCredentials(t *testing.T) {
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(log.handler))
	t.Cleanup(srv.Close)
	path := scenarioPath(t)
	curl := "curl '" + srv.URL + "/api?token=orig-tok&page=2' -H 'Authorization: Bearer secret123'"

	out, err := execDiscover(t, "", discoverArgs("--curl", curl, "--save-scenario", path)...)
	require.NoError(t, err, out)

	original := readFile(t, path)
	require.Len(t, stageDurationRe.FindAllString(original, -1), 2, "兩個 stage 的時長都要縮短")
	short := stageDurationRe.ReplaceAllString(original, "${1}duration: 1s")
	shortPath := filepath.Join(t.TempDir(), "short.yaml")
	require.NoError(t, os.WriteFile(shortPath, []byte(short), 0o600))

	t.Setenv("RAMPLIO_H_AUTHORIZATION", "Bearer from-env")
	t.Setenv("RAMPLIO_Q_TOKEN", "env-tok")
	log.reset()

	sum, _, _, err := runScenario(shortPath, "", protocols.DefaultHTTPConfig(), false)
	require.NoError(t, err)
	require.Positive(t, sum.Total)

	auths, tokens := log.snapshot()
	require.NotEmpty(t, auths)
	for i := range auths {
		assert.Equal(t, "Bearer from-env", auths[i], "Authorization 要等於環境變數的值")
		assert.Equal(t, "env-tok", tokens[i], "token 參數要等於環境變數的值")
	}
}

// 驗收情境 4：網址的敏感 query 值換成 env 引用，其他參數保留。
func TestSaveScenarioMasksSensitiveQuery(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)

	out, err := execDiscover(t, "", discoverArgs("--url", srv.URL+"/x?token=abc&page=2", "--save-scenario", path)...)
	require.NoError(t, err, out)

	saved := readFile(t, path)
	assert.NotContains(t, saved, "abc")
	assert.Contains(t, saved, `token={{env "RAMPLIO_Q_TOKEN"}}`)
	assert.Contains(t, saved, "page=2")
	assert.Contains(t, out, "RAMPLIO_Q_TOKEN")
	assert.NotContains(t, out, "abc")
}

// 驗收情境 5（負向）：SafeLimit 為 0 時不產生檔案、回錯誤並說明原因。
func TestSaveScenarioRefusesZeroSafeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	path := scenarioPath(t)

	out, err := execDiscover(t, "", discoverArgs("--url", srv.URL, "--save-scenario", path)...)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "安全上限為 0")
	assertNoFile(t, path)
	assert.NotContains(t, out, "已存成情境檔")
}

// 驗收情境 6（負向）：目標檔已存在時不覆蓋，而且在探測前就擋下。
func TestSaveScenarioRefusesOverwrite(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))

	_, err := execDiscover(t, "", discoverArgs("--url", srv.URL, "--save-scenario", path)...)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "已存在")
	assert.Equal(t, "original", readFile(t, path), "原檔內容不得改變")
	assert.Equal(t, int64(0), srv.total.Load(), "檔案已存在應在探測前擋下")
}

// 驗收情境 7（負向）：寫入型方法沒加 --allow-write 只送 1 發預檢、不產生檔案。
func TestSaveScenarioWriteMethodBlocked(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)

	_, err := execDiscover(t, "", discoverArgs("--url", srv.URL, "--method", "POST", "--save-scenario", path)...)

	require.Error(t, err)
	assert.Equal(t, int64(1), srv.total.Load(), "只能送出 1 發預檢")
	assertNoFile(t, path)
}

// 驗收情境 8（負向）：網址含 userinfo 時不產生檔案，提示改用 Authorization header。
func TestSaveScenarioRefusesUserinfo(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)
	target := strings.Replace(srv.URL, "http://", "http://u:S3CRETPW@", 1)

	out, err := execDiscover(t, "", discoverArgs("--url", target, "--save-scenario", path)...)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authorization")
	assert.NotContains(t, err.Error(), "S3CRETPW")
	assert.NotContains(t, out, "S3CRETPW")
	assertNoFile(t, path)
	assert.Equal(t, int64(0), srv.total.Load(), "應在探測前擋下")
}

// 目標 3：探測被中斷時結果不完整，不產生檔案。
func TestSaveScenarioRefusesInterrupted(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer

	err := runDiscover(ctx, &out, &out, discoverOptions{
		Request:       protocols.Request{Method: "GET", URL: srv.URL},
		Tolerance:     2 * time.Second,
		MaxRPS:        fastSafeLimit,
		ProbeDuration: 1500 * time.Millisecond,
		NoHistory:     true,
		ScenarioFile:  path,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "中斷")
	assertNoFile(t, path)
}

// 敏感值換成 env 名稱時撞名（例如重複的 query key 給了不同值），不能讓兩個值共用一個變數。
func TestSaveScenarioRefusesEnvNameCollision(t *testing.T) {
	srv := newRecordingServer(t, "", "")
	path := scenarioPath(t)

	_, err := execDiscover(t, "", discoverArgs("--url", srv.URL+"/?token=a1&token=b2", "--save-scenario", path)...)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "RAMPLIO_Q_TOKEN")
	assertNoFile(t, path)
}

// body 照原文寫入，非空時提醒以明文存檔。
func TestSaveScenarioWarnsPlaintextBody(t *testing.T) {
	srv := newRecordingServer(t, "", `{"q":1}`)
	path := scenarioPath(t)

	out, err := execDiscover(t, "", discoverArgs("--url", srv.URL, "--body", `{"q":1}`, "--allow-write", "--save-scenario", path)...)
	require.NoError(t, err, out)

	sc, err := scenarios.ParseFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"q":1}`, sc.Steps[0].Body)
	assert.Equal(t, "POST", sc.Steps[0].Method)
	assert.Contains(t, out, "body 以明文存進檔案")
}

// 驗收情境 9：沒加 --save-scenario 的成功探測，結尾提示可存成情境檔且不洩漏 header 值。
func TestDiscoverReportHintsSaveScenario(t *testing.T) {
	srv := newRecordingServer(t, "Bearer secret123", "")
	curl := "curl '" + srv.URL + "' -H 'Authorization: Bearer secret123'"

	out, err := execDiscover(t, "", discoverArgs("--curl", curl)...)
	require.NoError(t, err, out)

	assert.Contains(t, out, "--save-scenario scenario.yaml")
	assert.NotContains(t, out, "secret123")
}

// 探測失敗（SafeLimit 為 0）時不提示存情境檔：存了也是無法通過的門檻。
func TestDiscoverReportNoSaveScenarioHintWhenZeroSafeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	out, err := execDiscover(t, "", discoverArgs("--url", srv.URL)...)
	require.NoError(t, err, out)

	assert.NotContains(t, out, "--save-scenario")
}

func TestScenarioEnvName(t *testing.T) {
	cases := []struct{ prefix, in, want string }{
		{headerEnvPrefix, "Authorization", "RAMPLIO_H_AUTHORIZATION"},
		{headerEnvPrefix, "x-api-key", "RAMPLIO_H_X_API_KEY"},
		{queryEnvPrefix, "access.token", "RAMPLIO_Q_ACCESS_TOKEN"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, scenarioEnvName(c.prefix, c.in))
	}
}

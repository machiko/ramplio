package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedRequest is what the test server saw for one request.
type capturedRequest struct {
	method string
	header http.Header
	body   string
}

// capturingServer records every request so tests can assert exact counts and
// contents (e.g. "the server received exactly 1 request").
type capturingServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []capturedRequest
}

func newCapturingServer(t *testing.T) *capturingServer {
	t.Helper()
	cs := &capturingServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cs.mu.Lock()
		cs.reqs = append(cs.reqs, capturedRequest{method: r.Method, header: r.Header.Clone(), body: string(b)})
		cs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *capturingServer) requests() []capturedRequest {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return append([]capturedRequest(nil), cs.reqs...)
}

// fastEnv keeps a real discover run to about 1.5 s (one probe at 5 rps).
func fastEnv(stdin string, out io.Writer) smartEntryEnv {
	return smartEntryEnv{
		In:  strings.NewReader(stdin),
		Out: out,
		Err: out,
		Tuning: discoverTuning{
			Tolerance:     2 * time.Second,
			MaxRPS:        5,
			ProbeDuration: 1500 * time.Millisecond,
		},
	}
}

func runEntry(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runSmartEntry(context.Background(), newRootCmd(), args, fastEnv(stdin, &out))
	return out.String(), err
}

// execRoot drives the real cobra root so the Args/dispatch wiring is covered.
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// 情境 1：ramplio <URL> 走 discover 並印出容量報告。
func TestRootURLDispatchesDiscover(t *testing.T) {
	srv := newCapturingServer(t)

	out, err := runEntry(t, "", srv.URL+"/health")

	require.NoError(t, err)
	assert.Contains(t, out, "容量報告")
	reqs := srv.requests()
	require.Greater(t, len(reqs), 1, "應有暖機＋探測的多個請求")
	for _, r := range reqs {
		assert.Equal(t, http.MethodGet, r.method)
	}
}

// 情境 3：明確 -X POST 只發 1 發預檢、給 --allow-write 建議、exit 0。
func TestRootWriteMethodOnlyPreflights(t *testing.T) {
	srv := newCapturingServer(t)
	curl := "curl -X POST " + srv.URL + "/orders -H 'Content-Type: application/json' -d '{}'"

	out, err := runEntry(t, "", curl)

	require.NoError(t, err, "單一入口擋下寫入是正常引導，exit 0")
	assert.Len(t, srv.requests(), 1, "server 只能收到 1 個請求")
	assert.Contains(t, out, "--allow-write")
	assert.NotContains(t, out, "容量報告")
}

// 情境 7：Chrome 的隱式 POST（只有 --data-raw）推導為 POST，只發 1 發且帶 header 與 body。
func TestRootImplicitPostOnlyPreflights(t *testing.T) {
	srv := newCapturingServer(t)
	curl := "curl '" + srv.URL + "/api' \\\n  -H 'content-type: application/json' \\\n  -H 'x-trace: 42' \\\n  --data-raw '{\"a\":1}'"

	out, err := runEntry(t, "", curl)

	require.NoError(t, err)
	reqs := srv.requests()
	require.Len(t, reqs, 1, "server 只能收到 1 個請求")
	assert.Equal(t, http.MethodPost, reqs[0].method)
	assert.Equal(t, "application/json", reqs[0].header.Get("Content-Type"))
	assert.Equal(t, "42", reqs[0].header.Get("X-Trace"))
	assert.Equal(t, `{"a":1}`, reqs[0].body)
	assert.Contains(t, out, "--allow-write")
}

// stdin 路徑：pbpaste | ramplio - 帶著 header 探測。
func TestRootStdinCurlProbesWithHeaders(t *testing.T) {
	srv := newCapturingServer(t)
	curl := "curl '" + srv.URL + "/me' -H 'x-api-version: 7' --compressed\n"

	out, err := runEntry(t, curl, "-")

	require.NoError(t, err)
	assert.Contains(t, out, "容量報告")
	reqs := srv.requests()
	require.Greater(t, len(reqs), 1)
	for _, r := range reqs {
		assert.Equal(t, "7", r.header.Get("X-Api-Version"))
	}
}

// 情境 5：打錯字的子指令不開跑，回錯誤附建議與歡迎畫面指引（走真正的 cobra root）。
func TestRootUnknownArgSuggests(t *testing.T) {
	out, err := execRoot(t, "dicover")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "你是不是要 discover")
	assert.Contains(t, out, "ramplio <網址", "要附歡迎畫面指引")
	assert.NotContains(t, out, "正在探測")
}

func TestRootBareHostSuggestsScheme(t *testing.T) {
	_, err := runEntry(t, "", "api.example.com/health")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ramplio https://api.example.com/health")
}

// 情境 6：情境檔／HAR 只印出正確指令，不開跑。
func TestRootFileArgPrintsHint(t *testing.T) {
	tests := []struct {
		arg  string
		want string
	}{
		{"x.yaml", "ramplio run --scenario x.yaml"},
		{"dir/load.yml", "ramplio run --scenario dir/load.yml"},
		{"x.har", "ramplio import x.har"},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			out, err := execRoot(t, tt.arg)

			require.NoError(t, err)
			assert.Contains(t, out, tt.want)
			assert.NotContains(t, out, "正在探測")
		})
	}
}

func TestRootRejectsMultipleTargets(t *testing.T) {
	_, err := runEntry(t, "", "https://a.test", "https://b.test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "一次只能")
}

func TestRootNoArgsShowsWelcome(t *testing.T) {
	out, err := execRoot(t)
	require.NoError(t, err)
	assert.Contains(t, out, "從這裡開始")
}

// 情境 9：帶 Authorization／Cookie 的 curl，輸出與建議指令都不出現 token 原文。
func TestSmartEntryMasksCredentials(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiJ9.SECRET-PAYLOAD"
	const cookie = "session=SECRET-COOKIE-VALUE"

	t.Run("寫入型：只預檢＋建議指令", func(t *testing.T) {
		srv := newCapturingServer(t)
		curl := "curl '" + srv.URL + "/pay' -H 'Authorization: Bearer " + token + "' -b '" + cookie + "' --data-raw 'x=1'"

		out, err := runEntry(t, "", curl)

		require.NoError(t, err)
		reqs := srv.requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, "Bearer "+token, reqs[0].header.Get("Authorization"), "憑證仍要真的送出")
		assert.Contains(t, out, "pbpaste | ramplio discover --curl - --allow-write")
		assert.Contains(t, out, "Authorization: ***")
		assertNoSecrets(t, out, token, cookie)
	})

	t.Run("讀取型：完整探測", func(t *testing.T) {
		srv := newCapturingServer(t)
		curl := "curl '" + srv.URL + "/me' -H 'Authorization: Bearer " + token + "' -H 'Cookie: " + cookie + "'"

		out, err := runEntry(t, "", curl)

		require.NoError(t, err)
		assert.Contains(t, out, "容量報告")
		assert.Contains(t, out, "Cookie: ***")
		assertNoSecrets(t, out, token, cookie)
	})
}

func assertNoSecrets(t *testing.T, out string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		assert.NotContains(t, out, s, "輸出不得出現憑證原文")
	}
	assert.NotContains(t, out, "SECRET")
}

func TestClassifyTarget(t *testing.T) {
	tests := []struct {
		arg  string
		want targetKind
	}{
		{"https://x.test", targetURL},
		{"http://localhost:8080/a", targetURL},
		{"curl https://x.test", targetCurl},
		{"  curl 'https://x.test' -H 'a: b'", targetCurl},
		{"-", targetStdin},
		{"x.yaml", targetYAMLFile},
		{"X.YML", targetYAMLFile},
		{"rec.har", targetHARFile},
		{"dicover", targetUnknown},
		{"ftp://x.test", targetUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyTarget(tt.arg))
		})
	}
}

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// englishLeftovers are phrases from the old English report that must NOT appear
// once the report is fully localised — the capacity-answer positioning depends
// on a consistent plain-Chinese surface.
var englishLeftovers = []string{
	"Capacity Report",
	"Safe limit",
	"Breaking point",
	"What this means",
	"req/sec",
}

func TestWriteDiscoverReport_Default(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit:     200,
		BreakingPoint: 300,
		Probes:        make([]discover.ProbeResult, 9),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"容量報告",
		"安全上限",
		"每秒約 200 個請求",
		"臨界點",
		"每秒約 300 個請求",
		"這代表什麼",
		"穩定處理 200 個請求",
		"ramplio verify", // ground-truth trust badge → one-command self-proof
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

func TestWriteDiscoverReport_Exhausted(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit: 2000,
		Exhausted: true,
		Probes:    make([]discover.ProbeResult, 13),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"通過了全部 13 個測試等級",
		"超過每秒 2000 個請求",
		"--max-rps",
		"測試範圍內未觸及",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exhausted report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

func TestWriteDiscoverReport_Struggling(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{
		SafeLimit:     0,
		BreakingPoint: 5,
		Probes:        make([]discover.ProbeResult, 1),
	}
	writeDiscoverReport(&buf, result, 2*time.Second)
	out := buf.String()

	for _, want := range []string{
		"每秒不到 5 個請求",
		"很低的流量下就吃力",
		"檢查伺服器健康狀態",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("struggling report missing %q\n--- got ---\n%s", want, out)
		}
	}
	assertNoEnglishLeftovers(t, out)
}

// TestWriteDiscoverReport_Alignment verifies the box right border stays straight
// when Chinese text (display width 2 per rune) is mixed in: every content row's
// display width must not exceed the inner box width.
func TestWriteDiscoverReport_Alignment(t *testing.T) {
	var buf bytes.Buffer
	result := discover.DiscoverResult{SafeLimit: 200, BreakingPoint: 300, Probes: make([]discover.ProbeResult, 9)}
	writeDiscoverReport(&buf, result, 2*time.Second)

	for _, line := range strings.Split(buf.String(), "\n") {
		// Only inspect content rows (they start with the "  │  " prefix).
		if !strings.HasPrefix(line, "  │  ") {
			continue
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "  │  "), "│")
		if w := displayWidth(inner); w != reportWidth-2 {
			t.Errorf("row display width = %d, want %d\nrow: %q", w, reportWidth-2, line)
		}
	}
}

func TestWriteDiscoverProbe_Chinese(t *testing.T) {
	var buf bytes.Buffer
	writeDiscoverProbe(&buf, discover.ProbeResult{RPS: 200, P99: 42 * time.Millisecond, ErrorRate: 0.1, Status: discover.ProbePass})
	out := buf.String()

	for _, want := range []string{"每秒", "200", "p99=42ms", "錯誤=0.1%"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe line missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "rps") || strings.Contains(out, "errors") {
		t.Errorf("probe line still contains English: %q", out)
	}
}

func assertNoEnglishLeftovers(t *testing.T, out string) {
	t.Helper()
	for _, bad := range englishLeftovers {
		if strings.Contains(out, bad) {
			t.Errorf("report still contains English leftover %q\n--- got ---\n%s", bad, out)
		}
	}
}

// recordingServer counts requests and remembers whether every one carried the
// expected Authorization header and body.
type recordingServer struct {
	*httptest.Server
	total, mismatched atomic.Int64
}

func newRecordingServer(t *testing.T, wantAuth, wantBody string) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.total.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != wantAuth || string(b) != wantBody {
			rs.mismatched.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func execDiscover(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newDiscoverCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// 情境 8：discover 本身也守門，寫入型方法沒加 --allow-write 只發一發就擋下。
func TestDiscoverWriteMethodRequiresAllowWrite(t *testing.T) {
	srv := newRecordingServer(t, "", "")

	out, err := execDiscover(t, "", "--url", srv.URL, "--method", "POST", "--probe-duration", "1s", "--max-rps", "5")

	require.Error(t, err, "寫入型方法沒加 --allow-write 應回錯誤（exit ≠ 0）")
	assert.Equal(t, int64(1), srv.total.Load(), "只能送出 1 發預檢")
	assert.Contains(t, out, "--allow-write")
	assert.NotContains(t, out, "容量報告", "不該開始探測")
}

// 情境 4（cmd 層）：--curl 解析出的 header 與 body 要帶進每個探測請求。
func TestDiscoverCurlProbeSendsHeadersAndBody(t *testing.T) {
	srv := newRecordingServer(t, "Bearer s3cr3t", `{"a":1}`)
	curl := "curl '" + srv.URL + "' -H 'Authorization: Bearer s3cr3t' --data-raw '{\"a\":1}'"

	out, err := execDiscover(t, "", "--curl", curl, "--allow-write", "--probe-duration", "1500ms", "--max-rps", "5")

	require.NoError(t, err)
	assert.Contains(t, out, "容量報告")
	assert.Greater(t, srv.total.Load(), int64(1))
	assert.Equal(t, int64(0), srv.mismatched.Load(), "每個請求都要帶 Authorization 與 body")
	assert.NotContains(t, out, "s3cr3t", "輸出不得出現 token 原文")
}

// --curl - 從 stdin 讀；明確給的 -H/--method/--body 覆蓋 curl 解析值。
func TestDiscoverCurlStdinExplicitFlagsOverride(t *testing.T) {
	srv := newRecordingServer(t, "Bearer override", "new-body")
	curl := "curl " + srv.URL + " -H 'authorization: Bearer original' --data-raw 'old-body'"

	_, err := execDiscover(t, curl, "--curl", "-",
		"-H", "Authorization: Bearer override", "--body", "new-body", "--method", "PUT", "--allow-write",
		"--probe-duration", "1500ms", "--max-rps", "5")

	require.NoError(t, err)
	assert.Greater(t, srv.total.Load(), int64(1))
	assert.Equal(t, int64(0), srv.mismatched.Load(), "明確旗標必須覆蓋 curl 的值（含大小寫不同的同名 header）")
}

func TestDiscoverRequiresExactlyOneOfURLOrCurl(t *testing.T) {
	_, err := execDiscover(t, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--curl")

	_, err = execDiscover(t, "", "--url", "http://127.0.0.1:1", "--curl", "curl http://127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "二擇一")
}

func TestDiscoverRejectsMalformedHeaderFlag(t *testing.T) {
	_, err := execDiscover(t, "", "--url", "http://127.0.0.1:1", "-H", "no-colon-here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-H")
}

// H1（discover 層）：無法完整解讀的 curl 視同寫入，沒加 --allow-write 只發 1 發。
func TestDiscoverCurlUncertainRequiresAllowWrite(t *testing.T) {
	srv := newRecordingServer(t, "", "")

	out, err := execDiscover(t, "", "--curl", "curl --http3 "+srv.URL, "--probe-duration", "1s", "--max-rps", "5")

	require.Error(t, err)
	assert.Equal(t, int64(1), srv.total.Load(), "只能送出 1 發預檢")
	assert.Contains(t, out, "無法完整解讀")
	assert.Contains(t, out, "已送出 1 發真實 GET 預檢")
	assert.NotContains(t, out, "容量報告")
}

// M3（discover 層）。
func TestDiscoverCurlStdinOverLimitFails(t *testing.T) {
	huge := "curl http://127.0.0.1:1/ -d " + strings.Repeat("a", maxCurlInputBytes)

	_, err := execDiscover(t, huge, "--curl", "-")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "輸入超過 1MiB")
}

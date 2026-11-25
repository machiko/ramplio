package curlparse

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCurl(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantMethod  string
		wantURL     string
		wantHeaders map[string]string
		wantBody    string
		wantWarns   []string // each must appear (substring) in some warning
	}{
		{
			name:       "最簡 GET 只有網址",
			input:      `curl https://api.example.com/health`,
			wantMethod: "GET",
			wantURL:    "https://api.example.com/health",
		},
		{
			name: "Chrome DevTools 格式：反斜線換行、單引號、多個 -H、--compressed",
			input: "curl 'https://api.example.com/v1/users?page=2' \\\n" +
				"  -H 'accept: application/json' \\\n" +
				"  -H 'authorization: Bearer abc.def.ghi' \\\n" +
				"  -H 'user-agent: Mozilla/5.0 (Macintosh)' \\\n" +
				"  --compressed",
			wantMethod: "GET",
			wantURL:    "https://api.example.com/v1/users?page=2",
			wantHeaders: map[string]string{
				"accept":        "application/json",
				"authorization": "Bearer abc.def.ghi",
				"user-agent":    "Mozilla/5.0 (Macintosh)",
			},
		},
		{
			name:        "隱式 POST：有 --data-raw 沒寫 -X",
			input:       `curl 'http://localhost:8080/api' -H 'content-type: application/json' --data-raw '{"a":1}'`,
			wantMethod:  "POST",
			wantURL:     "http://localhost:8080/api",
			wantHeaders: map[string]string{"content-type": "application/json"},
			wantBody:    `{"a":1}`,
		},
		{
			name:       "明確 -X 優先於 body 推導",
			input:      `curl -X PUT https://x.test/items/1 -d 'name=a'`,
			wantMethod: "PUT",
			wantURL:    "https://x.test/items/1",
			wantBody:   "name=a",
		},
		{
			name:       "--request 長旗標與小寫方法會轉大寫",
			input:      `curl --request delete https://x.test/items/1`,
			wantMethod: "DELETE",
			wantURL:    "https://x.test/items/1",
		},
		{
			name:       "-XPOST 黏在一起的寫法",
			input:      `curl -XPOST https://x.test/a`,
			wantMethod: "POST",
			wantURL:    "https://x.test/a",
		},
		{
			name:        "雙引號內的 \\\" 跳脫與 --header 長旗標",
			input:       `curl "https://x.test/a" --header "X-Note: say \"hi\"" --data "{\"k\":\"v\"}"`,
			wantMethod:  "POST",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"X-Note": `say "hi"`},
			wantBody:    `{"k":"v"}`,
		},
		{
			name:       "Chrome ANSI-C $'…'：\\' 與 \\n",
			input:      `curl 'https://x.test/a' --data-raw $'{"msg":"it\'s\nok"}'`,
			wantMethod: "POST",
			wantURL:    "https://x.test/a",
			wantBody:   "{\"msg\":\"it's\nok\"}",
		},
		{
			name:       "--data-binary 與 --url 旗標",
			input:      `curl --url https://x.test/upload --data-binary 'raw'`,
			wantMethod: "POST",
			wantURL:    "https://x.test/upload",
			wantBody:   "raw",
		},
		{
			name:        "-b/--cookie 轉成 Cookie header",
			input:       `curl https://x.test/me -b 'sid=123; theme=dark'`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/me",
			wantHeaders: map[string]string{"Cookie": "sid=123; theme=dark"},
		},
		{
			name:       "忽略 -s -k -L -i",
			input:      `curl -s -k -L -i https://x.test/`,
			wantMethod: "GET",
			wantURL:    "https://x.test/",
		},
		{
			name:       "不認識的旗標產生警告、不默默吃掉",
			input:      `curl https://x.test/ --http2 --max-time`,
			wantMethod: "GET",
			wantURL:    "https://x.test/",
			wantWarns:  []string{"--http2", "--max-time"},
		},
		{
			name:       "多出的位置參數也要警告",
			input:      `curl https://x.test/a https://x.test/b`,
			wantMethod: "GET",
			wantURL:    "https://x.test/a",
			wantWarns:  []string{"https://x.test/b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			require.NoError(t, err)

			assert.Equal(t, tt.wantMethod, got.Method)
			assert.Equal(t, tt.wantURL, got.URL)
			if tt.wantHeaders == nil {
				assert.Empty(t, got.Headers)
			} else {
				assert.Equal(t, tt.wantHeaders, got.Headers)
			}
			assert.Equal(t, tt.wantBody, string(got.Body))

			if len(tt.wantWarns) == 0 {
				assert.Empty(t, got.Warnings, "不該有警告")
			}
			for _, w := range tt.wantWarns {
				assert.True(t, containsSubstring(got.Warnings, w), "警告應提到 %q，實際 %v", w, got.Warnings)
			}
		})
	}
}

func TestParseCurlErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"空字串", "   ", "空"},
		{"不是 curl 開頭", "wget https://x.test", "curl"},
		{"沒有網址", "curl -H 'a: b'", "網址"},
		{"引號沒關", "curl 'https://x.test", "引號"},
		{"旗標缺值", "curl https://x.test -H", "-H"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestParseCurlMultipleDataJoinedWithAmpersand(t *testing.T) {
	got, err := Parse(`curl https://x.test -d a=1 -d b=2`)
	require.NoError(t, err)
	assert.Equal(t, "a=1&b=2", string(got.Body))
}

func TestParseCurlReturnsIndependentCopies(t *testing.T) {
	got, err := Parse(`curl https://x.test -H 'a: 1' -d x`)
	require.NoError(t, err)

	h := got.HeadersCopy()
	h["a"] = "mutated"
	assert.Equal(t, "1", got.Headers["a"], "HeadersCopy 回傳的 map 不能與原值共用")
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

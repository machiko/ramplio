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
		// wantUncertain: the parser could not fully understand the command, so
		// callers must fail closed (treat it like a write).
		wantUncertain bool
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
			input:      `curl https://x.test/ --http2 --max-time 5`,
			wantMethod: "GET",
			wantURL:    "https://x.test/",
			wantWarns:  []string{"--http2", "--max-time"},
			// --http2 is unknown → fail closed
			wantUncertain: true,
		},
		{
			name:       "多出的位置參數也要警告",
			input:      `curl https://x.test/a https://x.test/b`,
			wantMethod: "GET",
			wantURL:    "https://x.test/a",
			wantWarns:  []string{"第 2 個網址參數"},
		},
		// ---- H1：寫入型的各種寫法都要推導出正確方法 ----
		{
			name:          "-F 表單推導為 POST，multipart 無法重現 → Uncertain",
			input:         `curl https://x.test/up -F a=b`,
			wantMethod:    "POST",
			wantURL:       "https://x.test/up",
			wantWarns:     []string{"-F"},
			wantUncertain: true,
		},
		{
			name:          "--form-string 同樣推導為 POST",
			input:         `curl https://x.test/up --form-string a=b`,
			wantMethod:    "POST",
			wantURL:       "https://x.test/up",
			wantWarns:     []string{"--form-string"},
			wantUncertain: true,
		},
		{
			name:          "-T 上傳檔案推導為 PUT，檔案內容不讀 → Uncertain",
			input:         `curl -T file.bin https://x.test/obj`,
			wantMethod:    "PUT",
			wantURL:       "https://x.test/obj",
			wantWarns:     []string{"-T"},
			wantUncertain: true,
		},
		{
			name:       "--json 推導為 POST 並補 Content-Type／Accept",
			input:      `curl https://x.test/api --json '{"a":1}'`,
			wantMethod: "POST",
			wantURL:    "https://x.test/api",
			wantHeaders: map[string]string{
				"Content-Type": "application/json",
				"Accept":       "application/json",
			},
			wantBody: `{"a":1}`,
		},
		{
			name:        "--json 不覆蓋使用者明確給的 content-type（大小寫不同也算）",
			input:       `curl https://x.test/api -H 'content-type: application/vnd.x+json' --json '{}'`,
			wantMethod:  "POST",
			wantURL:     "https://x.test/api",
			wantHeaders: map[string]string{"content-type": "application/vnd.x+json", "Accept": "application/json"},
			wantBody:    `{}`,
		},
		{
			name:       "--data-urlencode 推導為 POST 並編碼值",
			input:      `curl https://x.test/s --data-urlencode 'q=a b&c'`,
			wantMethod: "POST",
			wantURL:    "https://x.test/s",
			wantBody:   "q=a%20b%26c",
		},
		{
			name:       "-G 把 data 併進網址、方法為 GET",
			input:      `curl -G https://x.test/s?x=0 -d a=1 -d b=2`,
			wantMethod: "GET",
			wantURL:    "https://x.test/s?x=0&a=1&b=2",
		},
		{
			name:       "-I 推導為 HEAD",
			input:      `curl -I https://x.test/`,
			wantMethod: "HEAD",
			wantURL:    "https://x.test/",
		},
		{
			name:       "短旗標叢集 -sXPOST",
			input:      `curl -sXPOST https://x.test/a`,
			wantMethod: "POST",
			wantURL:    "https://x.test/a",
		},
		{
			name:        "短旗標叢集 -sH 取下一個 token 當值",
			input:       `curl -sH 'a: 1' https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"a": "1"},
		},
		{
			name:       "短旗標叢集 -skd 取下一個 token 當 data",
			input:      `curl -skd x=1 https://x.test/a`,
			wantMethod: "POST",
			wantURL:    "https://x.test/a",
			wantBody:   "x=1",
		},
		{
			name:       "-sSL 全是可略過的布林旗標",
			input:      `curl -sSL https://x.test/a`,
			wantMethod: "GET",
			wantURL:    "https://x.test/a",
		},
		{
			name:          "叢集裡有不認識的字元 → 警告＋Uncertain",
			input:         `curl -sZ https://x.test/a`,
			wantMethod:    "GET",
			wantURL:       "https://x.test/a",
			wantWarns:     []string{"-Z"},
			wantUncertain: true,
		},
		{
			name:          "-d @file 會讀檔，無法重現 → Uncertain",
			input:         `curl https://x.test/a -d @payload.json`,
			wantMethod:    "POST",
			wantURL:       "https://x.test/a",
			wantBody:      "@payload.json",
			wantWarns:     []string{"-d"},
			wantUncertain: true,
		},
		// ---- H2：帶值旗標與憑證旗標 ----
		{
			name:        "-u user:pass 轉 Basic Authorization，不被當成網址",
			input:       `curl -u admin:PW https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"Authorization": "Basic YWRtaW46UFc="},
		},
		{
			name:        "-uadmin:PW 黏在一起",
			input:       `curl -uadmin:PW https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"Authorization": "Basic YWRtaW46UFc="},
		},
		{
			name:        "--user=admin:PW",
			input:       `curl --user=admin:PW https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"Authorization": "Basic YWRtaW46UFc="},
		},
		{
			name:        "--oauth2-bearer 轉 Bearer Authorization",
			input:       `curl --oauth2-bearer TOK https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"Authorization": "Bearer TOK"},
		},
		{
			name:        "-A／-e 轉 User-Agent／Referer",
			input:       `curl -A 'my ua' -e https://ref.test/ https://x.test/a`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"User-Agent": "my ua", "Referer": "https://ref.test/"},
		},
		{
			name:       "帶值但不影響請求的旗標要吃掉下一個 token",
			input:      `curl -o out.txt -m 5 --connect-timeout 3 -w '%{http_code}' https://x.test/a`,
			wantMethod: "GET",
			wantURL:    "https://x.test/a",
			wantWarns:  []string{"-o", "-m", "--connect-timeout", "-w"},
		},
		{
			name:          "--proxy 會改變送達路徑 → 吃掉值並 Uncertain",
			input:         `curl -x http://proxy:3128 https://x.test/a`,
			wantMethod:    "GET",
			wantURL:       "https://x.test/a",
			wantWarns:     []string{"-x"},
			wantUncertain: true,
		},
		// ---- M4：header 大小寫不敏感覆蓋 ----
		{
			name:        "-H cookie 與 -b 只留一個 Cookie",
			input:       `curl https://x.test/a -H 'cookie: a=1' -b 'b=2'`,
			wantMethod:  "GET",
			wantURL:     "https://x.test/a",
			wantHeaders: map[string]string{"Cookie": "b=2"},
		},
		{
			name:       "大寫 CURL 也接受（與 classifyTarget 一致）",
			input:      `CURL https://x.test/a`,
			wantMethod: "GET",
			wantURL:    "https://x.test/a",
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
			assert.Equal(t, tt.wantUncertain, got.Uncertain, "Uncertain")
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

// H2：警告只能出現旗標名稱、第幾個參數、header 名稱，絕不能帶出值。
func TestParseCurlWarningsNeverEchoValues(t *testing.T) {
	const secret = "S3CRETV"
	inputs := []string{
		`curl https://x.test --unknown=` + secret,
		`curl https://x.test -H 'Authorization Bearer ` + secret + `'`,
		`curl https://x.test https://other.test/?k=` + secret,
		`curl https://x.test --proxy-user u:` + secret,
		`curl https://x.test -F 'f=` + secret + `'`,
		`curl https://x.test -d @` + secret,
		`curl https://x.test -sZ` + secret,
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			got, err := Parse(in)
			require.NoError(t, err)
			require.NotEmpty(t, got.Warnings, "這些寫法都應該產生警告")
			for _, w := range got.Warnings {
				assert.NotContains(t, w, secret)
			}
		})
	}
}

func TestMergeHeadersCaseInsensitive(t *testing.T) {
	base := map[string]string{"authorization": "old", "X-Keep": "1"}
	got := MergeHeaders(base, map[string]string{"Authorization": "new"})

	assert.Equal(t, map[string]string{"Authorization": "new", "X-Keep": "1"}, got)
	assert.Equal(t, "old", base["authorization"], "不能改到輸入的 map")
	assert.Nil(t, MergeHeaders(nil, nil))
}

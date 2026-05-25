# Ramplio

以開發者為優先的 HTTP 壓力測試工具。用 YAML 定義漸升、持載、尖峰等測試情境，在終端機或瀏覽器即時觀看結果，並以通過/失敗閾值整合 CI 流程——全部功能都在單一自包含的執行檔中。

---

## 功能特色

- **Capacity Discovery** — 一行指令自動探測網站最大吞吐量：從 5 RPS 遞增探測、偵測臨界點、輸出白話容量報告，適合 PM 與非技術人員直接使用
- **HAR Import** — 從 Chrome/Firefox DevTools 錄製直接產生 scenario.yaml，自動偵測登入步驟並注入 bearer auth
- **Per-step Metrics** — TUI 與 Dashboard 即時顯示每個步驟的 p50/p99/錯誤率，精確定位瓶頸步驟
- **單行指令或 YAML 驅動** — 直接指定 URL，或描述複雜的多階段測試情境
- **精確的百分位數** — 使用 HDR 直方圖計算 p50/p90/p95/p99，無近似誤差
- **即時 TUI** — 終端機內的即時儀表板，顯示階段進度、RPS、延遲與各步驟指標
- **即時網頁儀表板** — 由執行檔本身提供的 Vue 3 + Chart.js SPA，零額外設定
- **Prometheus 整合** — 將指標匯出至 Grafana 或任何相容 Prometheus 的監控系統
- **CI 友善** — 閾值超標時以 exit code `1` 退出
- **DNS 快取** — 選擇性快取 DNS 查詢，讓延遲量測只反映應用程式本身的開銷
- **單一執行檔** — 無執行時期相依套件，無需額外的儀表板程序

---

## 安裝

```bash
go install github.com/ramplio/ramplio/cmd/ramplio@latest
```

或從原始碼建置：

```bash
git clone https://github.com/ramplio/ramplio.git
cd ramplio
make install         # 編譯並安裝至 ~/.local/bin/ramplio
```

---

## 快速開始

### 單行指令

```bash
ramplio run --url https://httpbin.org/get --vus 20 --duration 30s
```

```
Running 20 VUs for 30s → GET https://httpbin.org/get

Results
───────
  Total requests:      2840
  Duration:            30.00s
  Req/sec:             94.6

Latency
───────
  Min:                 85ms
  Mean:                210ms
  p50:                 190ms
  p90:                 340ms
  p95:                 410ms
  p99:                 520ms
  Max:                 980ms

Status
──────
  Success (2xx):       2840 (100.0%)
  Errors:              0 (0.0%)
```

### 情境檔案

```yaml
# smoke.yaml
name: API smoke

stages:
  - duration: 30s
    target: 50      # 漸升至 50 VU
  - duration: 60s
    target: 50      # 持載
  - duration: 30s
    target: 0       # 漸降

steps:
  - name: GET health
    method: GET
    url: https://api.example.com/health
    assertions:
      status: 200

  - name: POST order
    method: POST
    url: https://api.example.com/orders
    headers:
      Content-Type: application/json
    body: '{"item":"widget","qty":1}'
    assertions:
      status: 201

thresholds:
  error_rate_pct: 1.0
  p99_ms: 800
```

```bash
ramplio run --scenario smoke.yaml
```

執行多步驟情境時，TUI 會在整體指標下方顯示 per-step 表格：

```
  Step                                Total       p50       p99   Err%
  ──────────────────────────────────────────────────────────────────────
  GET health                           3240      12ms      48ms    0.0%
  POST order                           3240      85ms     340ms    0.2%
```

---

## Capacity Discovery

不需要了解 VU 或 RPS 是什麼——直接告訴 Ramplio 網址，它自動幫你找出網站的最大承載量：

```bash
ramplio discover --url https://example.com
```

```
  Target:    https://example.com
  Tolerance: p99 < 2s, error rate < 1%
  Probes:    up to 9 levels (est. 2–4 min)

  Probing throughput capacity...

      5 rps  ✓  p99=180ms    errors=0.0%
     10 rps  ✓  p99=210ms    errors=0.0%
     20 rps  ✓  p99=340ms    errors=0.0%
     40 rps  ✓  p99=820ms    errors=0.0%
     75 rps  ⚠  p99=1.9s     errors=0.3%
    125 rps  ✗  p99=4.2s     errors=2.8%

  ┌──────────────────────────────────────────────┐
  │  Capacity Report                             │
  ├──────────────────────────────────────────────┤
  │  Safe limit:     ~40 req/sec                 │
  │  Breaking point: ~125 req/sec                │
  ├──────────────────────────────────────────────┤
  │  What this means:                            │
  │                                              │
  │  Your site handles about 40 requests per     │
  │  second comfortably. Above that, response    │
  │  times climb beyond 2s.                      │
  └──────────────────────────────────────────────┘
```

也可以從網頁儀表板的「⚡ 探測上限」分頁直接操作，包含即時探測進度表格與容量報告卡片，PM 不需要技術背景也能讀懂結果。

**常用選項：**

```bash
# 使用更嚴格的回應時間標準
ramplio discover --url https://api.example.com --tolerance 500ms

# 探測到更高的 RPS
ramplio discover --url https://example.com --max-rps 1000

# 縮短每個探測點的時間（較快但精準度略低）
ramplio discover --url https://example.com --probe-duration 10s
```

---

## HAR Import

從瀏覽器錄製直接產生 scenario.yaml，無需手寫：

**錄製步驟（Chrome/Edge）：**
1. DevTools → Network 分頁
2. 執行要壓測的完整操作流程（登入、查詢、下單…）
3. Network 面板空白處右鍵 → **Save all as HAR with content**

```bash
# 輸出到 stdout（預覽）
ramplio import recording.har

# 儲存到檔案
ramplio import recording.har -o scenario.yaml

# 不過濾靜態資源
ramplio import recording.har --no-filter

# 自訂測試時長
ramplio import recording.har -o scenario.yaml -d 5m
```

Import 會自動：
- **過濾靜態資源**（.js/.css/圖片/字型），只保留 API 呼叫
- **偵測登入步驟**（評分系統），自動加入 `capture: token` 提取 JWT
- **注入 bearer auth**，後續步驟的原始 token 替換為 `{{capture.token}}`

產生的 scenario.yaml 可直接執行：

```bash
ramplio validate --scenario scenario.yaml
ramplio run --scenario scenario.yaml
```

---

## 需要登入的網站（Login + CAPTCHA）

對有登入保護的網站做壓力測試，主要面對兩種情境：

### 情境 A：測試環境可停用 CAPTCHA（最簡單）

在測試環境關閉或 bypass CAPTCHA，讓每個 VU 在第一個步驟自行登入，session cookie 會自動保存在 VU 的 cookie jar，後續步驟無需手動帶入：

```yaml
steps:
  - name: POST /auth/login
    method: POST
    url: https://staging.example.com/auth/login
    headers:
      Content-Type: application/json
    body: '{"email":"loadtest@example.com","password":"testpass"}'
    assertions:
      status: 200
    pause: 200ms

  - name: GET /dashboard
    method: GET
    url: https://staging.example.com/dashboard
    assertions:
      status: 200
```

若後端回傳的是 JWT 而非 session cookie，用 `capture` 把 token 存起來，後續步驟以 `auth.bearer` 注入：

```yaml
steps:
  - name: POST /auth/login
    method: POST
    url: https://staging.example.com/auth/login
    headers:
      Content-Type: application/json
    body: '{"email":"loadtest@example.com","password":"testpass"}'
    assertions:
      status: 200
    capture:
      jwt: "$.access_token"      # JSONPath 從 response body 取出 token
    pause: 200ms

  - name: GET /dashboard
    method: GET
    url: https://staging.example.com/dashboard
    auth:
      bearer: "{{capture.jwt}}"  # 自動注入 Authorization: Bearer <token>
    assertions:
      status: 200
```

### 情境 B：生產環境有真實 CAPTCHA（Session Pool 方法）

生產環境不能停用 CAPTCHA 時，在測試執行**前**預先取得 N 組真實 session，以 CSV 分發給各 VU：

**第一步：產生 sessions.csv**

```bash
BASE_URL=https://example.com \
COOKIE_NAME=session \
COUNT=200 \
./scripts/generate_sessions.sh
```

腳本輸出 `sessions.csv`（格式：`session_cookie,user_id`）。

**第二步：在 scenario 直接注入 cookie**

```yaml
name: 登入後功能壓測

vars:
  base_url: "https://example.com"

vars_from:
  file: sessions.csv    # session_cookie,user_id
  mode: sequential      # 每個 VU 分配一組，CSV 行數需 >= 最大 VU 數

stages:
  - duration: 30s
    target: 10
  - duration: 3m
    target: 100
  - duration: 30s
    target: 0

steps:
  - name: GET /dashboard
    method: GET
    url: "{{vars.base_url}}/dashboard"
    headers:
      Cookie: "{{data.session_cookie}}"
    assertions:
      status: 200

  - name: POST /api/action
    method: POST
    url: "{{vars.base_url}}/api/action"
    headers:
      Content-Type: application/json
      Cookie: "{{data.session_cookie}}"
    body: '{"user_id":"{{data.user_id}}"}'
    assertions:
      status: 2xx
    pause: 500ms

thresholds:
  error_rate_pct: 1.0
  p95_ms: 500
```

```bash
ramplio run --scenario testdata/post-login-load.yaml
```

### `capture` 欄位一覽

`capture` 可以從 response 中提取值，供後續步驟以 `{{capture.key}}` 引用：

| 表達式 | 說明 | 範例 |
|--------|------|------|
| `$.path.to.value` | JSONPath（從 response body 提取） | `$.data.token` |
| `header:Name` | 從 response header 提取（取第一個值） | `header:X-Request-Id` |
| `cookie:name` | 從 `Set-Cookie` 提取特定 cookie 的值 | `cookie:session` |
| `regex:(pattern)` | 正規表達式（第一個 capture group） | `regex:token=([a-z0-9]+)` |

```yaml
steps:
  - name: POST /auth/refresh
    method: POST
    url: "{{vars.base_url}}/auth/refresh"
    headers:
      Cookie: "{{data.session_cookie}}"
    capture:
      new_session: "cookie:session"   # 取出 session cookie 的新值
    assertions:
      status: 200

  - name: GET /api/data
    method: GET
    url: "{{vars.base_url}}/api/data"
    headers:
      Cookie: "session={{capture.new_session}}"
    assertions:
      status: 2xx
```

---

## 即時網頁儀表板

```bash
# 啟動儀表板（預設 port 9999）
ramplio run --dashboard

# 指定 port
ramplio run --dashboard --dashboard-port 8080

# 停止儀表板（殺掉佔用該 port 的所有 process）
ramplio stop
ramplio stop --port 8080
```

或透過 Makefile：

```bash
make dashboard              # 啟動（port 9999）
make dashboard PORT=8080    # 啟動（自訂 port）
make stop-dashboard         # 停止（port 9999）
make stop-dashboard PORT=8080
```

儀表板提供四種操作模式：

| 分頁 | 說明 |
|------|------|
| URL 模式 | 直接填入 URL 啟動測試，支援 VU 或固定 RPS |
| 情境模式 | 上傳 HAR 或 YAML 情境檔執行多步驟測試 |
| 引導模式 | PM 精靈：幾個業務問題自動轉換成測試配置 |
| ⚡ 探測上限 | 自動探測網站最大吞吐量，輸出容量報告 |

RPS、延遲百分位數、錯誤率與活躍 VU 數的即時時序圖表——透過內嵌的 Vue 3 SPA 直接由執行檔提供服務，無需任何額外部署。

---

## Prometheus

```bash
ramplio run --scenario smoke.yaml --prometheus :9100
# Prometheus → http://:9100/metrics
```

公開的指標：`ramplio_requests_total`、`ramplio_errors_total`、`ramplio_error_rate_pct`、`ramplio_rps`、`ramplio_latency_p50/p90/p99_ms`、`ramplio_mean_latency_ms`、`ramplio_active_vus`、`ramplio_elapsed_seconds`。

---

## CLI 參數說明

```
ramplio run [flags]

Flags:
  -u, --url string            目標 URL（與 --scenario 互斥）
  -s, --scenario string       YAML 情境檔案路徑
      --vus int               虛擬使用者數量，僅 URL 模式（預設 1）
  -d, --duration string       測試時長，僅 URL 模式（預設 "30s"）
      --method string         HTTP 方法（預設 "GET"）
  -H, --header stringArray    HTTP 標頭，可重複使用：-H "Key: Value"
      --body string           請求 body
  -o, --output string         將結果儲存為 JSON 或 JUnit XML 檔案
      --timeout string        單次請求逾時，覆蓋情境設定（例如 10s）
      --dns-cache             快取 DNS 查詢（TTL 60 秒）
      --dashboard             開啟即時網頁儀表板
      --dashboard-port int    儀表板 HTTP 埠（預設 9999）
      --prometheus string     公開 Prometheus 指標端點（例如 :9100）
```

```
ramplio discover [flags]

Flags:
  -u, --url string             目標 URL（必填）
      --tolerance string       可接受的 p99 回應時間（預設 "2s"，例如 500ms、1s）
      --max-rps int            最高探測速率（預設 500）
      --probe-duration string  每個探測點的持續時間（預設 "15s"）
```

```
ramplio import <recording.har> [flags]

Flags:
  -o, --output string     將 scenario YAML 寫入檔案（預設輸出到 stdout）
      --no-filter         不過濾靜態資源（JS/CSS/圖片）
  -d, --duration string   覆蓋預設測試時長（例如 2m、90s）
```

```
ramplio stop [flags]

Flags:
      --port int   要停止的儀表板 port（預設 9999）
```

---

## CI 整合

閾值讓 Ramplio 成為 CI 流程的效能門禁：

```yaml
# .github/workflows/perf.yml
- name: 壓力測試
  run: ramplio run --scenario testdata/smoke.yaml
```

Exit code `0` → 所有閾值通過。Exit code `1` → 閾值超標或有錯誤，流程中止。

---

## 儲存與重新載入結果

```bash
ramplio run --scenario smoke.yaml --output results.json
ramplio report --input results.json
```

---

## 開發

```bash
make build           # 編譯 → ./bin/ramplio
make install         # 編譯並安裝至 ~/.local/bin/ramplio
make test            # 執行所有測試
make race            # 以 -race 偵測器執行測試
make cover           # 產生覆蓋率報告
make lint            # golangci-lint
make dashboard       # 啟動即時網頁儀表板（port 9999）
make stop-dashboard  # 停止儀表板
```

執行單一測試：

```bash
go test ./internal/engine/... -run TestRampUp -v
```

---

## 架構

```
ramplio/
├── cmd/ramplio/       # cobra CLI 指令（run、import、validate、report…）
├── internal/
│   ├── engine/          # VU 池與多階段 ramp 調度
│   ├── protocols/       # HTTP executor、DNS 快取、per-VU cookie jar
│   ├── metrics/         # HDR 直方圖收集器、per-step 分桶
│   ├── scenarios/       # YAML 解析器與驗證器
│   ├── reporter/        # 終端摘要、JSON、JUnit XML、TUI、Prometheus
│   ├── importer/        # HAR 解析、靜態資源過濾、登入偵測、Scenario 轉換
│   └── dashboard/       # WebSocket 伺服器 + 內嵌 Vue SPA
└── testdata/            # 測試用 YAML 情境與 HAR fixture
```

每個虛擬使用者（VU）在獨立的 goroutine 中運行。Engine 透過 semaphore 控制每個階段的 VU 數量；指標樣本透過有緩衝的 channel 流向單一的聚合 goroutine。Context cancellation 在所有層之間乾淨地傳播。

---

## 文件

| 文件 | 說明 |
|------|------|
| [docs/getting-started.md](docs/getting-started.md) | 完整安裝指南、CLI 參數說明與範例 |
| [docs/scenario-schema.md](docs/scenario-schema.md) | 完整 YAML 情境 schema 與所有欄位說明 |
| [docs/roadmap.md](docs/roadmap.md) | Milestone 計畫與開發生命週期 |
| [CHANGELOG.md](CHANGELOG.md) | 版本異動記錄 |

---

## 授權條款

MIT

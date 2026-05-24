# Ramplio

以開發者為優先的 HTTP 壓力測試工具。用 YAML 定義漸升、持載、尖峰等測試情境，在終端機或瀏覽器即時觀看結果，並以通過/失敗閾值整合 CI 流程——全部功能都在單一自包含的執行檔中。

---

## 功能特色

- **單行指令或 YAML 驅動** — 直接指定 URL，或描述複雜的多階段測試情境
- **精確的百分位數** — 使用 HDR 直方圖計算 p50/p90/p95/p99，無近似誤差
- **即時 TUI** — 終端機內的即時儀表板，顯示階段進度、RPS 與延遲
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

---

## 即時網頁儀表板

```bash
ramplio run --scenario smoke.yaml --dashboard
# Dashboard → http://localhost:9999
```

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
  -o, --output string         將結果儲存為 JSON 檔案
      --timeout string        單次請求逾時，覆蓋情境設定（例如 10s）
      --dns-cache             快取 DNS 查詢（TTL 60 秒）
      --dashboard             開啟即時網頁儀表板
      --dashboard-port int    儀表板 HTTP 埠（預設 9999）
      --prometheus string     公開 Prometheus 指標端點（例如 :9100）
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
make build    # 編譯 → ./bin/ramplio
make install  # 編譯並安裝至 ~/.local/bin/ramplio
make test     # 執行所有測試
make race     # 以 -race 偵測器執行測試
make cover    # 產生覆蓋率報告
make lint     # golangci-lint
```

執行單一測試：

```bash
go test ./internal/engine/... -run TestRampUp -v
```

---

## 架構

```
ramplio/
├── cmd/ramplio/       # cobra CLI 指令
├── internal/
│   ├── engine/          # VU 池與多階段 ramp 調度
│   ├── protocols/       # HTTP executor、DNS 快取
│   ├── metrics/         # HDR 直方圖收集器
│   ├── scenarios/       # YAML 解析器與驗證器
│   ├── reporter/        # 終端摘要、JSON、TUI、Prometheus
│   └── dashboard/       # WebSocket 伺服器 + 內嵌 Vue SPA
└── testdata/            # 測試用 YAML 情境檔案
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

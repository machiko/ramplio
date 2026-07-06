# 開發生命週期與歷程

> Ramplio 從零到生產就緒的完整發展路徑。每個 Milestone 都是一個可獨立運行的版本，具備明確的交付項目與 Definition of Done。

---

## 開發原則

貫穿所有 Milestone 的不可妥協原則：

- **TDD 優先**：每個功能先寫測試（RED），再實作（GREEN），再整理（REFACTOR）
- **Race Detector 必過**：`go test -race ./...` 在每個 Milestone 結束前全數通過
- **每個 Milestone 可獨立執行**：CLI 在當下 Milestone 範圍內必須可用
- **Semantic Versioning**：`v0.x.0` 為 pre-release，`v1.0.0` 為第一個穩定版

---

## Milestone 0 — Bootstrap `v0.0.1`

**目標**：建立可工作的開發環境，任何人 clone 後能立刻開始貢獻。

### 交付項目

| 項目 | 說明 |
|------|------|
| `go.mod` | `go mod init github.com/machiko/ramplio` |
| 目錄骨架 | `cmd/`、`internal/`、`docs/`、`testdata/` |
| `.golangci.yml` | golangci-lint 設定，含 errcheck、staticcheck、govet |
| `.github/workflows/ci.yml` | lint + test + race detector |
| `Makefile` | `build`、`test`、`lint`、`run` 快捷指令 |
| `testdata/example.yaml` | 第一個 scenario 範例（暫不可執行，作為 DSL 草稿） |

### Definition of Done

```bash
make build   # 編譯成功
make test    # 通過（此時測試極少，主要驗證 CI 可運行）
make lint    # 無 lint 錯誤
```

CI pipeline 在 push 後自動觸發並全綠。

---

## Milestone 1 — Core Engine MVP `v0.1.0`

**目標**：最小可用版本。一行指令對任何 URL 發動固定並發壓力。

```bash
ramplio run --url https://example.com --vus 10 --duration 30s
```

### 交付項目

| 檔案 | 職責 |
|------|------|
| `internal/protocols/http.go` | HTTP Executor（GET/POST、自訂 headers、body） |
| `internal/engine/engine.go` | 固定並發 VU pool，goroutine semaphore，Context cancellation |
| `internal/metrics/collector.go` | Buffered channel + 單一 aggregator goroutine |
| `internal/metrics/summary.go` | 基本統計：total count、error count、min/max/mean latency |
| `internal/reporter/terminal.go` | 執行結束後印出純文字摘要表格 |
| `cmd/ramplio/run.go` | CLI `run` 指令（`--url`、`--vus`、`--duration`） |

### 並發模型確認點

```
[VU goroutine × N] ──► chan metrics.Sample ──► [aggregator goroutine]
```

VU hot path 寫入 channel 不阻塞，aggregator 是唯一讀寫者，無需 mutex。

### Definition of Done

- 單元測試覆蓋率 ≥ 80%（`go test -cover ./...`）
- `go test -race ./...` 全通過
- 手動驗證：對 `httptest.Server` 發動 50 VUs / 30s，印出的 count 與 error rate 數字正確

---

## Milestone 2 — Scenario DSL `v0.2.0`

**目標**：從 YAML 檔案驅動測試，支援多階段 ramp-up / hold / ramp-down。

```bash
ramplio run --scenario testdata/smoke.yaml
ramplio validate --scenario testdata/smoke.yaml
```

### 交付項目

| 檔案 | 職責 |
|------|------|
| `internal/scenarios/parser.go` | YAML 解析：stages、steps、assertions、thresholds |
| `internal/scenarios/validator.go` | 解析時驗證：duration 格式、VU 數合理性、必填欄位 |
| `internal/engine/ramp.go` | Stage 切換邏輯，semaphore 動態調整 VU 數 |
| `cmd/ramplio/validate.go` | CLI `validate` 指令 |
| `docs/scenario-schema.md` | YAML 欄位完整說明（日後另行建立） |

### YAML 最小可用結構

```yaml
name: smoke test
stages:
  - duration: 30s
    target: 50
  - duration: 60s
    target: 50
  - duration: 30s
    target: 0
steps:
  - name: GET /api/health
    method: GET
    url: https://example.com/api/health
    assertions:
      - status: 200
thresholds:
  error_rate_pct: 1.0
  p99_ms: 1000
```

### Definition of Done

- `testdata/smoke.yaml`（含 3 個 stages）執行結果正確
- validate 指令對畸形 YAML 回傳清晰的行號錯誤訊息
- `go test -race ./...` 全通過

---

## Milestone 3 — Rich Metrics & Reporting `v0.3.0`

**目標**：生產等級的量測精度，以及多格式報告輸出與 CI 整合。

### 交付項目

| 檔案 | 職責 |
|------|------|
| `internal/metrics/histogram.go` | 以 `hdrhistogram-go` 提供 p50/p90/p95/p99/p100 |
| `internal/reporter/json.go` | 將 `metrics.Summary` 序列化為 JSON 結果檔 |
| `internal/reporter/html.go` | 使用 `embed` 內嵌 Chart.js 模板，生成靜態 HTML 報告 |
| `cmd/ramplio/report.go` | CLI `report` 指令（從既有 JSON 重新生成報告） |

### CI 整合行為

```bash
ramplio run --scenario smoke.yaml --output results.json
# thresholds 未達標 → exit code 1 → CI pipeline 失敗
# thresholds 全達標 → exit code 0 → CI pipeline 通過
```

### Definition of Done

- HDR 百分位數精度測試通過（已知輸入 → 驗證輸出在誤差範圍內）
- HTML 報告可在瀏覽器正常渲染折線圖
- `echo $?` 在 threshold 失敗時確認回傳 1

---

## Milestone 4 — Live Terminal Dashboard `v0.4.0`

**目標**：執行中即時顯示動態指標，取代靜態等待的黑屏體驗。

```
┌─ Ramplio ──────────────────────────────────────┐
│  Stage 2/3: Hold 50 VUs  [████████░░] 60%  1m02s │
├──────────────────────────────────────────────────┤
│  RPS      p50      p99      Error                │
│  423      42ms     187ms    0.2%                 │
└──────────────────────────────────────────────────┘
```

### 交付項目

| 項目 | 說明 |
|------|------|
| `internal/reporter/tui.go` | 以 `bubbletea` 建立 TUI，訂閱 metrics channel，每秒刷新 |
| Stage 進度條 | 顯示當前 stage index、剩餘時間、VU 數變化 |
| 即時指標列 | RPS、p50、p99、error rate |

### Definition of Done

- 在真實終端機執行，畫面刷新流暢、無閃爍
- Ctrl+C 中斷後，仍印出完整的靜態摘要（與 Milestone 1 的 terminal reporter 一致）

---

## Milestone 5 — Web Dashboard `v0.5.0`

**目標**：瀏覽器即時監控，binary 自帶，零額外部署成本。

```bash
ramplio run --scenario smoke.yaml --dashboard
# 自動開啟 http://localhost:9999
```

### 交付項目

| 項目 | 說明 |
|------|------|
| `dashboard/` | Vue 3 SPA（Vite 打包），含即時時序圖 |
| `/ws/metrics` | Go WebSocket endpoint，每秒推送 metrics snapshot |
| `embed.FS` 整合 | `go build` 後 dist/ 嵌入 binary，無需外部靜態檔案 |
| 圖表 | RPS、latency percentiles、error rate、active VUs（時序軸） |

### 量測誤差驗證

WebSocket 推送跑在獨立 goroutine，不阻塞 VU hot path。以 benchmark 驗證：

```bash
# 1000 RPS 場景，帶 --dashboard 與不帶 --dashboard 的 p99 差距 < 1ms
go test ./internal/engine/... -bench=BenchmarkEngine -benchtime=30s
```

### Definition of Done

- `go build` 後 binary 可獨立啟動 dashboard
- 1000 RPS 場景下，dashboard 不影響量測誤差（benchmark 驗證）

---

## Milestone 6 — Hardening & `v1.0.0`

**目標**：生產就緒。效能驗證、邊界處理、可觀測性、文件完整。

### 交付項目

| 項目 | 說明 |
|------|------|
| 連線池調優 | `http.Transport` 參數（`MaxIdleConns`、`MaxConnsPerHost`）可在 YAML 設定 |
| DNS 快取控制 | 避免 DNS 查詢延遲污染 latency 量測，提供 `--dns-cache` 選項 |
| Prometheus export | `--prometheus :9100` 啟動 `/metrics` endpoint |
| 自我壓測 | 以 Ramplio 壓測 Ramplio 自帶的 mock server，10,000 VUs / 10 分鐘 |
| 記憶體穩定性 | pprof heap profile 驗證無洩漏 |
| 文件補齊 | `docs/getting-started.md`、`docs/scenario-schema.md` |
| Changelog | `CHANGELOG.md`，含 Breaking Changes 標記 |

### Definition of Done

- 10,000 VU 場景連跑 10 分鐘，heap 成長 < 5%（pprof 驗證）
- 所有 `docs/` 文件經過實際操作驗證（文件描述的指令可執行）
- `go test -race ./...` 全通過

---

## 版本時間軸概覽

```
v0.0.1  Bootstrap          ── 開發環境就緒
v0.1.0  Core Engine MVP    ── 可對 URL 施壓，看到數字
v0.2.0  Scenario DSL       ── YAML 驅動多階段測試
v0.3.0  Rich Reporting     ── 精確量測 + CI 整合
v0.4.0  Terminal Dashboard ── 即時 TUI 監控
v0.5.0  Web Dashboard      ── 瀏覽器即時監控
v1.0.0  Hardening          ── 生產就緒
```

---

## 各 Milestone 驗收標準速查

| Milestone | 關鍵驗收指令 |
|-----------|-------------|
| 0 | `make build && make test && make lint` |
| 1 | `ramplio run --url ... --vus 50 --duration 30s` |
| 2 | `ramplio validate --scenario testdata/smoke.yaml` |
| 3 | `echo $?` 確認 threshold 失敗時回傳 exit code 1 |
| 4 | 終端機執行，Ctrl+C 後印出摘要 |
| 5 | `go build` 單一 binary 啟動 dashboard |
| 6 | pprof heap profile，10min 無洩漏 |

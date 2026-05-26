# 變更日誌

Ramplio 的所有重要變更都記錄於此。
版本號遵循 [語義化版本管理](https://semver.org/)。

---

## [Unreleased]

### 新增
- **分散式測試基礎架構 (Phase 3)**: Coordinator-Worker 模式突破單進程 TCP 連線限制，支援 4 個 Worker 分散負載、健康檢查、VU 自動分配、結果合併。
- **`ramplio worker` 子命令**: 獨立 Worker 進程，監聽指定 port，接收場景並執行本地引擎。
- **`--worker` 旗標**: 在 `ramplio run` 中指定 Worker 位址（可重複），自動成為 Coordinator。
- **EvalCondition 複雜邏輯**: 支援 AND、OR、NOT、括號優先級的條件評估，用於 `if` 欄位控制步驟執行。
- **詳細的條件邏輯示例**: 三個完整 YAML 場景 (`simple-if-example.yaml`, `complex-conditions.yaml`, `conditional-flow.yaml`) 示範實際用法。
- **README 重組**: 新增快速導航、層級結構清晰的 6 大主題章節，提升文檔可讀性。

### 已驗證
- 分散式測試: Coordinator × 1、Worker × 3，單機測試通過，TUI 合併指標正確。
- 條件邏輯: 所有 3 個示例場景通過驗證，支援複雜 AND/OR 表達式。
- 文檔: README 結構優化，所有鏈接有效。

---

## [v1.0.0] — 生產就緒版 (2026-05-24)

---

## [v1.0.0] — Production Ready (2026-05-24)

### Added
- **`mock-server` 指令** (`--port`, `--latency`): 內建本機 HTTP mock server，供自壓測與 CI smoke test 使用。
- **`--version` 旗標**: `ramplio --version` 回傳目前版本號。
- **`testdata/self-stress.yaml`**: 10,000 VU × 10 分鐘 4 階段自壓測場景。
- **Web 控制面板 Run/Stop API** (`POST /api/run`, `POST /api/stop`, `GET /api/status`): PM 可直接從瀏覽器填表啟動測試，無需 CLI。
- **Dashboard Controller 介面**: `dashboard.Controller` 統一管理引擎生命週期，支援 Setup → Live → Result 三視圖流程。

### Changed
- Dashboard 從純觀測模式升級為完整控制面板，可在瀏覽器中填入 URL、VUs、Duration 後點擊 Run。

### Verified
- `go test -race ./...` 全數通過。
- 10,000 VU × 10 分鐘場景：heap 成長 < 5%（pprof 驗證）、error_rate < 1%、p99 < 500ms。
- Dashboard overhead benchmark：有無 dashboard 的 p99 差距 < 1ms。

---

## [v0.6.0] — 強化穩定性

### 新增
- **Prometheus 指標端點** (`--prometheus :9100`): 執行時公開即時指標（Prometheus 文字格式）。指標包括 `ramplio_requests_total`、`ramplio_errors_total`、`ramplio_error_rate_pct`、`ramplio_rps`、`ramplio_latency_p50/p90/p99_ms`、`ramplio_mean_latency_ms`、`ramplio_active_vus`、`ramplio_elapsed_seconds`。
- **DNS 快取** (`--dns-cache`): 基於 TTL 的 DNS 查詢快取（預設 TTL 60 秒），防止重複 DNS 查詢增加單次請求延遲。
- **單次請求逾時** (`--timeout`): 從 CLI 覆蓋情境的 `request_timeout_ms`，無需編輯 YAML。
- **HTTP 連接池調整** (`http.max_idle_conns`、`http.max_idle_conns_per_host`、`http.request_timeout_ms`): 針對高 VU 負載調整傳輸層參數。
- **`HTTPExecutor.CloseIdleConnections()`**: 執行結束後顯式清理 keep-alive 連接，用於記憶體穩定性測試。
- **記憶體穩定性測試** (`TestMemoryStability`、`TestRampMemoryStability`): 執行 50 VU × 5 秒，驗證引擎停止後無 goroutine 洩漏。
- **文檔**: `docs/getting-started.md` 和 `docs/scenario-schema.md`。

### 修正
- `prometheus.go`: 將 `fmt.Fprintf` 替換為 `fmt.Fprint`（非常數格式字符串警告）。

---

## [v0.5.0] — 網頁儀表板

### 新增
- **即時網頁儀表板** (`--dashboard`、`--dashboard-port`): Vue 3 + Chart.js SPA，由 Go 執行檔透過 `embed.FS` 提供。展示 RPS、延遲百分位數、錯誤率、活躍 VU 數的實時時序圖表。
- **WebSocket 端點** (`/ws/metrics`): 每 500 ms 推送 `LiveSnapshot` JSON；連接斷開時自動重連。
- 引擎 context 取消時儀表板乾淨關閉。

---

## [v0.4.0] — 終端實時儀表板

### 新增
- **TUI 即時儀表板** (bubbletea + lipgloss): 實時終端視圖，顯示階段進度條、RPS、p99、錯誤率、活躍 VU 數；每秒刷新一次。
- 優雅的 Ctrl+C 處理: 取消引擎並列印完整摘要。
- `reporter.LiveProvider` 介面和 `LiveSnapshot` 結構體在 TUI 和網頁儀表板間共用。

---

## [v0.3.0] — 豐富的指標與報告

### 新增
- **HDR 直方圖** (`hdrhistogram-go`): 用精確的 p50/p90/p95/p99 百分位數取代簡單的 min/max。
- **JSON 輸出** (`--output results.json`): 將 `metrics.Summary` 序列化到磁碟。
- **閾值退出碼**: 超過 `error_rate_pct` 或 `p99_ms` 閾值時以退出碼 `1` 結束（CI 友善）。
- `metrics.Collector.LiveSummary()` 和 `LivePercentiles()`: 為 TUI 和儀表板提供執行緒安全的讀取。

---

## [v0.2.0] — 情境 DSL

### 新增
- **YAML 情境檔案** (`--scenario`): 在單一檔案中宣告階段、步驟、斷言和閾值。
- **`ramplio validate` 命令**: 解析並驗證情境而不執行。
- **RampEngine** (`internal/engine/ramp.go`): 多階段 VU 漸升，在 `target` 值間進行線性插值。
- **單步驟斷言**: `status` 程式碼檢查；失敗計為錯誤。
- `scenarios.ParseFile()` 在解析時驗證時長字符串和 VU 數。

---

## [v0.1.0] — 核心引擎 MVP

### 新增
- `ramplio run --url --vus --duration`: 最小化的單行負載測試。
- `internal/engine`: 固定 VU 池，支援 context 取消。
- `internal/protocols`: HTTP executor，支援可配置的方法、標頭、body。
- `internal/metrics`: 緩衝 channel 收集器 + 聚合 goroutine。
- `internal/reporter`: 每次執行後列印終端摘要表。

---

## [v0.0.1] — 引導版

### 新增
- Go 模組 (`github.com/ramplio/ramplio`)。
- 完整目錄結構: `cmd/`、`internal/`、`docs/`、`testdata/`。
- `golangci-lint` 配置 (`.golangci.yml`)。
- GitHub Actions CI 流程 (lint + test + race detector)。
- `Makefile`，包含 `build`、`test`、`lint`、`run` 目標。
- `testdata/example.yaml`: 第一個情境 fixture。

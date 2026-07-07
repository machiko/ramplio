# 變更日誌

Ramplio 的所有重要變更都記錄於此。
版本號遵循 [語義化版本管理](https://semver.org/)。

---

## [v2.1.1] — Module Path 統一 (2026-07-06)

### 已變更
- **Module path 統一為 `github.com/machiko/ramplio`**(74 檔): 原路徑 `github.com/ramplio/ramplio` 與實際 repo 不符。統一 go.mod、全部 import、README 安裝指引與 GoReleaser 設定。GitHub Releases 的 binary 下載不受影響。

### 已知限制
- **`go install` 尚無法安裝 v2.x**: Go modules 規則要求 v2+ 的 module path 帶 `/v2` 字尾,目前 go.mod 未加,`go install ...@v2.1.1` 會被拒(實測確認)。請改用 GitHub Releases 下載或原始碼建置;`/v2` 遷移已列入追蹤。

---

## [v2.1.0] — 分散式測試 + 智慧 Dashboard (2026-07-06)

### 新增
- **發布打包管線**: GoReleaser 跨平台建置（macOS arm64/amd64、Linux amd64/arm64、Windows amd64）、tag 觸發的 GitHub Actions 發布 workflow、PR/main push 的 CI 品質閘門（build + race test + golangci-lint）、Homebrew tap 自動更新。版本號改由 ldflags 注入（`make build` 自動帶 git tag）。
- **分散式測試基礎架構 (Phase 3)**: Coordinator-Worker 模式突破單進程 TCP 連線限制，支援 4 個 Worker 分散負載、健康檢查、VU 自動分配、結果合併。
- **`ramplio worker` 子命令**: 獨立 Worker 進程，監聽指定 port，接收場景並執行本地引擎。
- **`--worker` 旗標**: 在 `ramplio run` 中指定 Worker 位址（可重複），自動成為 Coordinator。
- **EvalCondition 複雜邏輯**: 支援 AND、OR、NOT、括號優先級的條件評估，用於 `if` 欄位控制步驟執行。
- **詳細的條件邏輯示例**: 三個完整 YAML 場景 (`simple-if-example.yaml`, `complex-conditions.yaml`, `conditional-flow.yaml`) 示範實際用法。
- **README 重組**: 新增快速導航、層級結構清晰的 6 大主題章節，提升文檔可讀性。
- **Dashboard 首頁選擇卡片**: 將 Setup View 的 4 個平行 tab（URL 模式 / 情境模式 / 引導模式 / 探測上限）整合為首頁選擇流程。新增 Home View 展示 3 張清晰的選擇卡片（帶我設定/快速測試/上傳場景）+ 1 個小 link（探測上限），讓非技術背景用戶能直觀地完成第一次測試。

### 已變更
- **Dashboard UX 改進**: 首次進入 Dashboard 預設顯示首頁選擇卡片（而非 URL 表單），「帶我設定」（引導模式）標記為推薦選項，引導新手選擇最友善的路徑。

### 已驗證
- 分散式測試: Coordinator × 1、Worker × 3，單機測試通過，TUI 合併指標正確。
- 條件邏輯: 所有 3 個示例場景通過驗證，支援複雜 AND/OR 表達式。
- 文檔: README 結構優化，所有鏈接有效。
- Dashboard 首頁: HTML 包含所有選擇卡片、事件綁定正確、CSS 樣式完整、初始 view 設為 'home'、返回流程通暢。

---

## [v2.0.0] — 企業級功能與 Dashboard 升級 (2026-05-24)

### 新增
- **Cookie 捕獲與自動化會話管理**: 從回應中自動擷取 cookie，後續請求自動帶入，支援有狀態應用測試。
- **`ramplio init` 指令**: 交互式初始化新場景檔案，引導使用者設定基本參數。
- **吞吐量自動探測（`discover` 模式）**: 快速掃描目標上限，無需手工設定 VU 數。
- **Data File 參數化 (`--data-file`)**: 從外部 CSV/JSON 檔案載入測試資料，支援行迴圈替換。
- **HTML 報表匯出**: CLI 執行完成後輸出互動式 HTML 報表，含圖表與詳細數據。
- **Per-step 指標（各步驟個別分析）**: 每個步驟獨立蒐集 RPS、延遲、錯誤率，洞察瓶頸步驟。
- **HAR Import 指令 (`ramplio import`)**: 從 Chrome DevTools HAR 檔案匯入場景，自動生成 YAML 步驟。
- **Think Time（用戶思考延遲）**: `think_time_ms` 欄位模擬真實使用者行為間隔。
- **Cookie Session 管理**: 在場景中宣告 `cookies: {...}` 實現多 VU 隔離的會話。
- **JUnit XML 報表**: `--output-junit` 輸出相容 CI/CD 平臺（Jenkins、GitHub Actions）的測試結果格式。
- **Status 萬用字元 (`2xx`, `4xx`, `5xx`)**: Assertions 支援範圍檢查，簡化常見場景。
- **M1–M8 功能擴充**: 8 大增強功能集（詳見 `docs/roadmap.md`）。
- **Dashboard Result View 重設計**: 新增容量判讀卡片、曲線穩定性改進、指標說明 Tooltip。
- **Dashboard 即時進度條**: Setup 與 Live 視圖並排，實時掌握執行進度。
- **CLI 中文化**: 所有命令與錯誤訊息支援繁體中文輸出。
- **Dashboard setup/teardown 支援**: 可在 YAML 中宣告 `setup` 和 `teardown` 步驟，Dashboard 自動執行。
- **穩定性與可用性修補 (S1/S2)**: 修復多個邊界條件、改進錯誤訊息清晰度、提升整體穩定性。

### 已變更
- **Dashboard 架構**: 從純觀測升級為完整控制面板，統一管理 Setup → Live → Result 三視圖流程。
- **Scenario 格式擴充**: 支援 `init`、`setup`、`teardown` 步驟，以及複雜的資料參數化結構。
- **CLI 命令擴充**: 新增 `import`、`init`、`discover` 等便捷指令。

### 修正
- 修復 Result View 曲線在高負載下消失的問題。
- 修復 self-stress.yaml assertions 格式（物件非 list）。
- 改進條件邏輯評估的邊界情況。

### 已驗證
- 完整 E2E 測試覆蓋率 >= 80%。
- HAR import 支援主流瀏覽器匯出格式（Chrome、Firefox、Safari）。
- Data File 參數化支援大檔案（>100MB）不阻塞引擎。
- Dashboard 支援 50,000+ VU 並列監測無性能退化。

---

## [v1.0.0] — 生產就緒版 (2026-05-24)

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
- Go 模組 (`github.com/machiko/ramplio`)。
- 完整目錄結構: `cmd/`、`internal/`、`docs/`、`testdata/`。
- `golangci-lint` 配置 (`.golangci.yml`)。
- GitHub Actions CI 流程 (lint + test + race detector)。
- `Makefile`，包含 `build`、`test`、`lint`、`run` 目標。
- `testdata/example.yaml`: 第一個情境 fixture。

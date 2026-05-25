# Ramplio 專案分析快照

> **如何使用這份文件**
> 每次新 session 要求分析專案時，先讀這份文件，再執行：
> ```
> git diff <last_analyzed_commit>..HEAD --stat
> ```
> 只重新掃描有異動的檔案，然後更新對應模組的條目與底部的 metadata。

---

## Metadata

| 欄位 | 值 |
|------|-----|
| 分析時間 | 2026-05-25 |
| 分析基準 commit | `9694120` |
| 完整 commit hash | `969412012eeca4af88bfbe2f2b1eaeef88f70ac6` |
| Go 版本 | (見 go.mod) |

---

## 模組摘要

### engine/ramp.go
- **功能**：多階段負載引擎，支援 VU 模式（線性插值）與 RPS 模式（令牌桶）；資料檔案 sequential/random 分發
- **主要型別**：`RampEngine`, `RampStep`, `RampConfig`
- **主要方法**：`Run()`, `runRate()`, `runVU()`, `renderRequest()`, `applyCaptures()`
- **已知限制**：VU 與 RPS 在同一場景互斥；Sample 緩衝區滿時靜默丟棄；資料檔案隨機使用偽隨機（非加密安全）

### engine/engine.go
- **功能**：基礎固定 VU 引擎，無階段支援（早期版本遺留）
- **主要型別**：`Engine`, `Config`
- **已知限制**：無狀態，每個 VU 重複同一請求；RampEngine 完全取代其功能但尚未移除

### scenarios/scenario.go
- **功能**：YAML 場景資料結構定義（Scenario, Stage, Step, Auth, Capture, Assertions, Thresholds）
- **已知限制**：Thresholds 只支援 `error_rate_pct` 和 `p99_ms`；StatusCheck 以原始字串儲存，無預先驗證

### scenarios/parser.go
- **功能**：YAML 解析與驗證（`Parse()`, `ParseFile()`）；驗證必填欄位、VU/RPS 混用
- **已知限制**：無嵌套場景；無動態時間戳支援

### scenarios/assertions.go
- **功能**：斷言評估（狀態碼、Body contains/regex、JSON path、Header 比對）
- **主要方法**：`EvalAssertions()`, `JSONPathToGJSON()`
- **已知限制**：無複合邏輯（AND/OR）；JSON path 轉換為 gjson 格式，複雜陣列路徑可能不準確

### scenarios/template.go
- **功能**：模板標記替換（`{{uuid}}`, `{{timestamp}}`, `{{env.X}}`, `{{vars.X}}`, `{{capture.X}}`, `{{data.X}}`）
- **主要型別**：`VarContext`
- **已知限制**：無巢狀標記；env 無預設值（失敗返回空字串）

### scenarios/datafile.go
- **功能**：從 CSV / JSON 載入資料行供 vars_from 使用
- **已知限制**：無型別驗證；CSV 不規則列填充空值

### protocols/executor.go
- **功能**：執行器介面定義（`Executor`, `Request`, `Result`）
- **已知限制**：無重試或熔斷器支援

### protocols/http.go
- **功能**：HTTP 執行器（連線池、cookie jar、超時、DNS 快取）
- **主要型別**：`HTTPExecutor`, `HTTPConfig`
- **主要方法**：`NewSession()`, `Execute()`
- **已知限制**：回應 Body 限制 1MB；多值 Header 只取第一個；無自訂 TLS

### protocols/dns_cache.go
- **功能**：DNS 查詢 TTL 快取（RWMutex 保護）
- **已知限制**：無最大快取大小限制（記憶體洩漏風險）；過期後無更新機制

### metrics/collector.go
- **功能**：Channel 驅動 Sample 聚集器；計算 HDR 直方圖百分位；支援步驟級別指標
- **主要方法**：`Add()`, `Stop()`, `LiveSummary()`, `LiveStepMetrics()`
- **已知限制**：緩衝區 = maxVUs × 10，滿時丟棄；無暖身期排除

### metrics/sample.go
- **功能**：單一請求測量資料結構（時間戳、延遲、狀態碼、位元組、步驟名稱）

### metrics/summary.go
- **功能**：聚集統計（計數、延遲百分位、吞吐量）
- **已知限制**：無中位數或加權計算

### reporter/terminal.go
- **功能**：靜態終端文字摘要輸出

### reporter/html.go
- **功能**：使用 go:embed 樣板生成自包含 HTML 報告
- **已知限制**：無互動圖表；模板路徑寫死

### reporter/json.go
- **功能**：Summary ↔ JSON Report 雙向序列化（所有延遲轉為毫秒）
- **已知限制**：無版本控制

### reporter/junit.go
- **功能**：JUnit XML 報告（CI/CD 集成），閾值違反標記為 failure
- **已知限制**：testsuite 名稱寫死為 "ramplio"

### reporter/prometheus.go
- **功能**：`/metrics` 端點，Prometheus 文字格式，500ms 更新間隔
- **指標**：requests_total, errors_total, error_rate_pct, rps, p50/p90/p99_ms, active_vus
- **已知限制**：無認證；更新間隔無法設定

### reporter/tui.go
- **功能**：Bubbletea 即時終端儀表板（RPS、延遲、VU、步驟指標表）
- **主要型別**：`LiveSnapshot`, `LiveProvider`
- **已知限制**：步驟名稱截斷 31 字元；無滾動；進度條固定 10 字元

### dashboard/server.go
- **功能**：HTTP + WebSocket 伺服器；嵌入式 HTML 儀表板；REST API
- **端點**：`GET /`、`GET /ws`、`POST /api/run`、`POST /api/stop`、`GET /api/status`、`POST /api/scenario`、`POST /api/import-har`
- **已知限制**：WebSocket 允許所有來源（無 CORS 限制）；場景上傳 1MB，HAR 10MB；無認證

### dashboard/controller.go
- **功能**：Controller 介面實現，管理測試生命週期與狀態轉換（Idle → Running → Done）
- **主要方法**：`Start()`, `Stop()`, `LoadScenario()`, `Snapshot()`
- **已知限制**：一次只允許一個測試；無執行排隊

### dashboard/guided.go
- **功能**：PM 導向測試精靈（業務輸入 → 技術配置 → 判決）
- **主要型別**：`GuidedProfile`, `RampPlan`, `GuidedVerdict`
- **traffic_shape**：steady（動態持續時間）/ spike（固定 60s）/ soak（固定 5 分鐘）
- **判決邏輯**：pass（error<1% 且 p95≤target）/ warn（p95≤1.5×target）/ fail（其他）
- **已知限制**：VU 夾在 [1, 5000]；場景種類只影響方法推斷；無趨勢分析

### dashboard/listener.go
- **功能**：TCP 監聽器建立工具

### dashboard/embed.go
- **功能**：go:embed 包含 `templates/dashboard.html`

### importer/har.go
- **功能**：HAR JSON 結構解析（harFile, harEntry, harRequest, harResponse）
- **已知限制**：無版本驗證；無重定向鏈支援

### importer/converter.go
- **功能**：HAR 項目 → 場景 YAML；自動偵測認證、擷取標記、JWT 登入
- **主要方法**：`Convert()`, `ConvertBytes()`, `buildScenario()`, `entryToStep()`
- **預設階段**：30s 加速 → 60s 保持 → 30s 冷卻
- **已知限制**：啟發式基於硬編碼閾值（登入評分 0.7）；無 multipart 支援；Header 過濾清單寫死

### importer/detector.go
- **功能**：HAR 啟發式偵測（預認證 token、登入項目）
- **主要方法**：`findPreAuthToken()`, `findLoginEntry()`, `loginScore()`
- **已知限制**：token 最小長度 20 字元；假正例風險（字串模式匹配）

### importer/filter.go
- **功能**：過濾靜態資產（JS/CSS/圖片/字體）
- **已知限制**：副檔名匹配前剝除查詢字串；某些 API 端點可能被誤判

### cmd/ramplio/main.go
- **功能**：Cobra 根命令，版本 1.0.0，註冊子命令

### cmd/ramplio/run.go
- **功能**：主要 `run` 命令；支援 URL/RPS/場景/儀表板四種模式
- **旗標**：url, method, vus, rps, duration, headers, body, scenario, output, dashboard, prometheus, timeout
- **已知限制**：VU/RPS 互斥；錯誤率 >0 自動以 exit code 1 結束（無 --ignore-errors）

### cmd/ramplio/import.go
- **功能**：`import` 命令，HAR 轉場景 YAML；支援 --no-filter, --duration 覆蓋

### cmd/ramplio/dashcontrol.go
- **功能**：儀表板用 Controller 實現（與 dashboard/controller.go 功能重疊）
- **已知限制**：與 `dashboard/controller.go` 存在職責重疊，可能需要整合

### cmd/ramplio/mock.go
- **功能**：本地 mock HTTP 伺服器（GET /, /healthz; POST /login; GET /profile）
- **已知限制**：無速率限制；/profile 只檢查 Authorization header 存在（不驗證值）

### cmd/ramplio/report.go
- **功能**：`report` 命令，從 JSON 結果生成 HTML 報告

### cmd/ramplio/stop.go
- **功能**：`stop` 命令，lsof + kill -9 終止儀表板伺服器
- **已知限制**：Unix 限定（無 Windows 支援）；直接 SIGKILL

### cmd/ramplio/validate.go
- **功能**：`validate` 命令，解析 YAML 場景並回報階段/步驟計數

---

## 已知問題與技術債

| 優先度 | 項目 | 位置 |
|--------|------|------|
| HIGH | `engine/engine.go` 功能被 RampEngine 完全取代，可考慮移除 | engine/engine.go |
| HIGH | `dashboard/controller.go` 與 `cmd/ramplio/dashcontrol.go` 職責重疊 | 兩者皆是 |
| MED | DNS 快取無大小上限（記憶體洩漏風險） | protocols/dns_cache.go |
| MED | WebSocket 無 CORS 限制、API 無認證 | dashboard/server.go |
| MED | Sample 緩衝區滿時靜默丟棄，無告警 | metrics/collector.go |
| MED | `stop` 命令直接 SIGKILL，Windows 不相容 | cmd/ramplio/stop.go |
| LOW | Thresholds 只支援 2 個指標 | scenarios/scenario.go |
| LOW | HTML 報告無互動圖表 | reporter/html.go |
| LOW | mock server /profile 不驗證 token 值 | cmd/ramplio/mock.go |

---

## 功能覆蓋對照（與主流工具比較）

| 功能 | k6 | Vegeta | Ramplio | 備注 |
|------|:--:|:------:|:-------:|------|
| 多階段 VU | ✓ | - | ✓ | |
| 固定 RPS | ✓ | ✓ | ✓ | |
| YAML 場景 DSL | - | - | ✓ | 差異化優勢 |
| HAR Import | ✓ | - | ✓ | |
| 即時 TUI | ✓ | - | ✓ | |
| Web Dashboard | - | - | ✓ | 差異化優勢 |
| Guided Mode | - | - | ✓ | 差異化優勢 |
| JUnit XML | ✓ | - | ✓ | |
| Prometheus | ✓ | - | ✓ | |
| WebSocket 測試 | ✓ | - | - | 尚未支援 |
| gRPC 測試 | ✓ | - | - | 尚未支援 |
| Script (JS) | ✓ | - | - | 刻意不做 |
| 分散式測試 | ✓ | ✓ | - | 尚未支援 |
| 資料檔案 | ✓ | - | ✓ | |
| 模板變數 | ✓ | - | ✓ | |
| Cookie Session | ✓ | - | ✓ | |
| Per-step 指標 | - | - | ✓ | 差異化優勢 |

---

## 如何進行增量更新

下次需要分析時，執行：

```bash
# 1. 取得上次分析後的異動
git diff 9694120..HEAD --stat

# 2. 只讀異動的檔案
# 3. 更新對應模組的條目
# 4. 更新 Metadata 的 commit hash 和分析時間
```

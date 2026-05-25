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
| 分析時間 | 2026-05-25（更新：discover UX + 冷連線 bug 修復） |
| 分析基準 commit | `b16d800`（+ WIP：discover CLI & Web UI 整合，尚未 commit） |
| 完整 commit hash | `b16d800002ce00ead5c1a2008574fa9e29349c47` |
| Go 版本 | (見 go.mod) |

---

## 模組摘要

### engine/ramp.go
- **功能**：多階段負載引擎，大幅擴充：
  - VU 模式（線性插值）與 **Ramping RPS 模式**（`target_rps` per stage + 令牌桶線性插值）
  - **Setup/Teardown** lifecycle hooks（`SetupSteps`/`TeardownSteps`，執行一次，captures 共享給所有 VU）
  - **CircuitBreaker**：連續失敗達閾值時停止所有 VU（`consecutiveFails` atomic counter）
  - **Step.If** 條件評估（呼叫 `scenarios.EvalCondition`）
  - **Step.Loop** 重複執行
  - **Step.Group** 指定聚合群組
  - **Step.Pause** think time
  - **Protocol 選擇**：每步可指定 `http`（預設）或 `websocket`，透過 `stepExecutor()` 路由
  - **RetryingExecutor 整合**：`stepExecutor()` 自動包裝 retry 層
  - `dataSource` 分發資料行（sequential/random，Fibonacci hash 代替 lock contention）
- **主要型別**：`RampEngine`, `RampStep`, `RampConfig`, `dataSource`
- **主要方法**：`Run()`, `runVUs()`, `runRate()`, `runRateWorker()`, `runVU()`, `stepExecutor()`, `pickExecutor()`, `executeSingleStep()`, `renderRequest()`, `applyCaptures()`, `isCircuitTripped()`
- **已知限制**：
  - VU 與 RPS stage 在同一場景互斥（解析層阻擋）
  - CircuitBreaker 跨所有 VU atomic 計數，高並發下可能因短暫抖動誤觸發
  - `runRate()` worker 數 = maxRPS × 5（上限 5000），高 RPS 場景記憶體壓力大
  - Assertion 失敗不觸發 retry（assertions 在 executor 返回後才評估）
  - regex capture 每次請求重新 Compile（效能問題）
  - setup 步驟的 Sample 不計入指標（靜默執行）

### engine/engine.go
- **功能**：基礎固定 VU 引擎，無階段支援（早期版本遺留）
- **主要型別**：`Engine`, `Config`
- **已知限制**：無狀態，每個 VU 重複同一請求；RampEngine 完全取代其功能但尚未移除

### scenarios/scenario.go
- **功能**：YAML 場景資料結構定義，大幅擴充：
  - `Scenario.Setup`/`Teardown`（Steps 陣列）
  - `Stage.TargetRPS`（Ramping Arrival Rate）
  - `Step.Pause`（think time）、`Step.Group`、`Step.Protocol`/`WSMessage`/`WSExpect`、`Step.If`、`Step.Loop`
  - `Step.Retry`（`RetryConfig`：count、on、backoff_ms）
  - `Thresholds` 全面擴充：P50Ms、P90Ms、P95Ms、MaxMs、ThroughputRps、`Steps` map（per-step thresholds）
  - `StepThresholds`（per-step P95Ms、P99Ms、ErrorRatePct）
  - `CircuitBreakerConfig`（consecutive_failures）
  - `ScenarioHTTP`（max_idle_conns、max_idle_conns_per_host、request_timeout_ms）
- **已知限制**：`StatusCheck` 以原始字串儲存，無預先驗證；`Step.If` 只支援 == / != 簡單比較

### scenarios/parser.go
- **功能**：YAML 解析與驗證（`Parse()`, `ParseFile()`）；驗證必填欄位、VU/RPS 混用
- **已知限制**：無嵌套場景；無動態時間戳支援

### scenarios/assertions.go
- **功能**：斷言評估（狀態碼、Body contains/regex、JSON path、Header 比對）
- **主要方法**：`EvalAssertions()`, `JSONPathToGJSON()`
- **已知限制**：無複合邏輯（AND/OR）；JSON path 轉換為 gjson 格式，複雜陣列路徑可能不準確

### scenarios/template.go
- **功能**：模板標記替換與條件評估
  - `RenderString()`：替換所有 `{{token}}` 標記
  - 支援：`{{uuid}}`, `{{timestamp}}`, `{{timestamp_ms}}`, `{{env "VAR"}}`, `{{vars.X}}`, `{{capture.X}}`, `{{data.X}}`
  - 新增 `EvalCondition()`：先 render 再評估 `LHS == RHS` / `LHS != RHS`；parse 失敗返回 true（不跳過步驟）
  - 新增 `RenderHeaders()`：批次渲染 header map
- **主要型別**：`VarContext`（含 Vars、Captures、Data 三個 map）
- **已知限制**：無巢狀標記；env 無預設值（失敗返回空字串）；`{{env "X"}}` 語法與其他 token 不一致；`EvalCondition` 不支援 AND/OR 複合邏輯；parse 失敗靜默（可能掩蓋設定錯誤）

### scenarios/datafile.go
- **功能**：從 CSV / JSON 載入資料行供 `vars_from` 使用（`LoadDataFile()`）；CSV 第一列為 header，JSON 為 `[]map[string]string`
- **已知限制**：無型別驗證；CSV 不規則列填充空值；JSON 僅支援字串值

### protocols/executor.go
- **功能**：執行器介面定義（`Executor`, `Request`, `Result`）

### protocols/retry.go（新增）
- **功能**：`RetryingExecutor` 包裝任何 `Executor`，根據設定自動重試
  - `count`：最多重試幾次（不含第一次）；`onCodes`：限定觸發重試的 HTTP 狀態碼（空 = 任何錯誤）；`backoff`：固定等待時間
  - 尊重 `ctx.Done()` 中斷重試
- **已知限制**：僅固定 backoff，無指數退避或 jitter；assertion 失敗（非 HTTP 錯誤）不會觸發 retry

### protocols/ws.go（新增）
- **功能**：`WSExecutor` 實作 WebSocket 協定：dial → 發送 text frame → 讀一個回應 → 關閉連線
  - 使用 `gorilla/websocket`；支援 `X-WS-Expect` header 做回應內容驗證（substring match）
  - 狀態碼：握手失敗取 HTTP 狀態碼，成功為 101
- **已知限制**：每次請求開新連線（無持久連線）；只支援單次收發（無雙向串流）；握手時無法設定自訂 headers（無 WebSocket auth）；使用 `websocket.DefaultDialer`（無 TLS 設定）

### protocols/http.go
- **功能**：HTTP 執行器（連線池、cookie jar、超時、DNS 快取）
- **主要型別**：`HTTPExecutor`, `HTTPConfig`
- **主要方法**：`NewSession()`, `Execute()`
- **已知限制**：回應 Body 限制 1MB；多值 Header 只取第一個；無自訂 TLS

### protocols/dns_cache.go
- **功能**：DNS 查詢 TTL 快取（RWMutex 保護）
- **已知限制**：無最大快取大小限制（記憶體洩漏風險）；過期後無更新機制

### metrics/collector.go
- **功能**：Channel 驅動 Sample 聚集器；計算 HDR 直方圖百分位；支援步驟與群組級別指標
- **新增**：`GroupSummary` 型別；per-group HDR 直方圖（`groupHists`/`groupSums`/`groupOrder`）；`LiveGroupMetrics()` 即時群組快照
- **主要方法**：`Add()`, `Stop()`, `LiveSummary()`, `LivePercentiles()`, `LiveStepMetrics()`, `LiveGroupMetrics()`
- **已知限制**：緩衝區 = maxVUs × 10，滿時靜默丟棄；無暖身期排除；步驟/群組的直方圖記憶體不受限

### metrics/sample.go
- **功能**：單一請求測量資料結構（時間戳、延遲、狀態碼、位元組、步驟名稱、Group）

### metrics/summary.go
- **功能**：聚集統計（計數、延遲百分位、吞吐量）；新增 `Groups []GroupSummary` 欄位
- **已知限制**：無中位數或加權計算

### reporter/terminal.go
- **功能**：靜態終端文字摘要輸出；per-step 表格（Total/p50/p99/Errors，標記最慢步驟 ◀ slowest）；新增 **Group Breakdown** 表格（Total/p50/p95/Errors）；`printInterpretation()` 輸出自然語言判決（PASS/WARN/FAIL + 瓶頸步驟）

### reporter/html.go
- **功能**：使用 go:embed 樣板生成自包含 HTML 報告
- **已知限制**：無互動圖表；模板路徑寫死

### reporter/json.go
- **功能**：Summary ↔ JSON Report 雙向序列化（所有延遲轉為毫秒）
  - 新增 `Verdict` struct（`computeVerdict()`：level/headline/speed/reliability/bottleneck）
  - `StepReport` 增加 `ErrorRate`、`P90Ms`
  - 新增 `ReadJSON()` 從檔案反序列化
- **已知限制**：無版本控制；Groups 不包含在 JSON Report 中；Verdict 閾值寫死（errRate ≥ 5 or p99 ≥ 1000 = fail）

### reporter/sink.go（新增）
- **功能**：`Sink` 介面定義（`Write(sum, scenarioName) error`、`Close() error`）

### reporter/sink_csv.go（新增）
- **功能**：`CsvSink` — 追加寫入 CSV 檔；首次開啟時寫 header；欄位：timestamp/scenario/duration_s/total/errors/error_rate_pct/rps/p50~p99/max_ms
- **已知限制**：每次 Write 都 Flush（高頻寫入效能較低）；不包含 per-step/group 明細

### reporter/sink_influx.go（新增）
- **功能**：`InfluxSink` — DSN 格式 `influxdb://host:port/bucket?token=TOKEN&org=ORG`；推送 InfluxDB v2 line protocol；一筆測試結果 = 一個 measurement（`ramplio_results`）
- **已知限制**：**強制使用 HTTP（非 HTTPS）**；不包含 per-step/group 指標；無批次推送；org 為可選但推薦提供

### reporter/sink_factory.go（新增）
- **功能**：`ParseSink(dsn)` — 根據 DSN 前綴路由到對應 Sink（`csv:` / `influxdb://`）

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
- **端點**：`GET /`、`GET /ws`、`POST /api/run`、`POST /api/stop`、`GET /api/status`、`POST /api/scenario`、`POST /api/import-har`、`GET /api/report`、**`POST /api/discover`**
- **`/api/report`**：生成 HTML 報告並以 `attachment` 下載（用 buffer 避免部分寫入）
- **`/api/discover`**：啟動容量探測；解碼 `DiscoverRequest` 後呼叫 `ctrl.StartDiscover()`
- **wsMessage**：新增 `DiscoverMode bool`、`DiscoverProbes []DiscoverProbeSnap`、`DiscoverResult *DiscoverResultSnap`、`DiscoverCurrent *DiscoverCurrentSnap`、`DiscoverProbeSeq []int`；`buildWSMessage()` 從 `DiscoverProgress()` 讀取全部 5 個回傳值並填充至每次 WebSocket push
- **已知限制**：WebSocket 允許所有來源（無 CORS 限制）；場景上傳 1MB，HAR 10MB；無認證

### dashboard/controller.go
- **功能**：Controller 介面；`RunRequest` 增加 `OverrideVUs`、`OverrideDuration`（場景模式下覆蓋階段設定）
- **新增型別**：
  - `DiscoverRequest`（URL/Tolerance/MaxRPS/ProbeDuration）
  - `DiscoverProbeSnap`（RPS/P99Ms/ErrorPct/Status）
  - `DiscoverResultSnap`（SafeLimit/BreakingPoint/Exhausted）
  - `DiscoverCurrentSnap`（RPS/ElapsedMs/ProbeDurationMs）— 記錄**正在進行中**探測點的即時資訊，供 WebSocket push 顯示進度條與計時器
- **新增介面方法**：`StartDiscover(req DiscoverRequest) error`、`DiscoverProgress() ([]DiscoverProbeSnap, *DiscoverResultSnap, *DiscoverCurrentSnap, []int, bool)`（回傳值依序：完成探測切片、最終結果、當前探測快照、探測序列、是否啟動中）
- **已知限制**：一次只允許一個測試（普通測試 + discover 共用同一狀態機）；無執行排隊

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

### internal/discover/prober.go（新增）
- **功能**：容量自動探測引擎，對外公開 `Prober` 結構
  - `Config`：URL、Method、Tolerance（p99 閾值）、MaxRPS（探測上限）、ProbeDuration（每探測點時長）
  - `ProbeSequence(maxRPS)`：返回 RPS 探測序列（5, 10, 20, 40, 75, 125, 200, 300, 500… 幾何級數），超過 maxRPS 截斷並附加 maxRPS
  - `Run(ctx, onProbeStart, onProbe)`：以遞增 RPS 依序執行探測；`onProbeStart` 在每個探測點**開始前**呼叫（供進度顯示），`onProbe` 在完成後呼叫；首次 ProbeFail 立即中止
  - `probe(ctx, rps)`：單一探測點 — 以 `SetupSteps` 先執行一次暖身請求（結果不計入指標），再啟動計時探測；直接重用共享 `HTTPExecutor` 避免冷 TCP/TLS 開銷
  - `classify(p99, errorRate, tolerance)`：PASS（p99 ≤ tolerance 且 error < 1%）/ WARN（p99 ≤ 1.5× 且 error < 3%）/ FAIL（其他）
  - `DiscoverResult`：`SafeLimit`（最高 PASS RPS）、`BreakingPoint`（首個 FAIL RPS）、`Exhausted`（全通過）
- **設計亮點**：
  - `Prober` 在 `New()` 建立單一 `HTTPExecutor` 並共享給所有探測點，TCP/TLS 連線跨探測點重用
  - 每探測點以 `SetupSteps` 預熱連線池，避免第一批請求因冷連線導致 p99 虛高而誤判 FAIL
  - `onProbeStart` + `onProbe` 雙回調讓 CLI 列印與 Dashboard WebSocket 推送共用同一邏輯，無需重複實作
- **已知限制**：探測序列寫死（無法自訂步距）；每探測點獨立建立 Collector 和 RampEngine，記憶體配置較多

### cmd/ramplio/discover.go（新增）
- **功能**：`ramplio discover` CLI 命令
  - 旗標：`--url/-u`（必填）、`--tolerance`（預設 "2s"）、`--max-rps`（預設 500）、`--probe-duration`（預設 "15s"）
  - 執行前估算探測層數與預計時間並印出摘要
  - `printDiscoverProbe()`：即時列印每個探測點（圖示 ✓/⚠/✗ + RPS + p99 + 錯誤率）
  - `printDiscoverReport()`：用方框繪製容量報告（Safe limit / Breaking point / 白話說明）
  - 支援 SIGINT/SIGTERM 優雅中止
- **依賴**：`internal/discover`、`internal/protocols`

### cmd/ramplio/main.go
- **功能**：Cobra 根命令，版本 1.0.0，註冊子命令（新增 `newDiscoverCmd()`）

### cmd/ramplio/run.go
- **功能**：主要 `run` 命令；支援 URL/RPS/場景/儀表板四種模式；新增 `--report`/`-r` 直接輸出 HTML 報告；載入 VarsFrom 資料檔案並印出行數與模式
- **旗標**：url, method, vus, rps, duration, headers, body, scenario, output, **report**, dashboard, prometheus, timeout
- **已知限制**：VU/RPS 互斥；錯誤率 >0 自動以 exit code 1 結束（無 --ignore-errors）

### cmd/ramplio/import.go
- **功能**：`import` 命令，HAR 轉場景 YAML；支援 --no-filter, --duration 覆蓋

### cmd/ramplio/dashcontrol.go
- **功能**：儀表板用 Controller 實現；新增 `lastSummary`/`lastSummarySet` 儲存最後一次結果供 `WriteReport` 使用；`buildOverrideStages` 根據 `OverrideVUs`/`OverrideDuration` 重建 3 階段（加速/保持/冷卻）
- **新增 discover 狀態欄位**：`discoverActive bool`、`discoverProbes []DiscoverProbeSnap`、`discoverResult *DiscoverResultSnap`、`discoverCurrentRPS int`、`discoverProbeStart time.Time`、`discoverProbeDur time.Duration`、`discoverProbeSeq []int`
- **新增方法**：
  - `StartDiscover(req)`：解析 tolerance/probeDuration/maxRPS；設 `state=Running`、`discoverActive=true`；goroutine 呼叫 `runDiscover()`
  - `runDiscover(ctx, url, tol, maxRPS, pd)`：預先計算並儲存 `probeSeq`；透過 `onProbeStart` 回調設置 `discoverCurrentRPS` + `discoverProbeStart`；每完成一個探測點在 `onProbe` 中更新 `discoverProbes` 並清除 `discoverCurrentRPS`；完成後設 `state=Done`、`discoverResult`
  - `DiscoverProgress()`：RLock 讀取所有 discover 欄位；若 `discoverCurrentRPS > 0` 則建構 `DiscoverCurrentSnap`（含已過去毫秒數），讓 WS push 得以顯示即時進度
- **普通測試 Start/startGuided**：啟動時清除 `discoverActive=false` 讓 WebSocket 不再推送 discover 欄位
- **已知限制**：與 `dashboard/controller.go` 存在職責重疊，可能需要整合

### dashboard/templates/dashboard.html
- **功能**：Vue 3 SPA；四種操作模式（URL / 情境 / 引導精靈 / ⚡ 探測上限）
- **新增 Discover 模式**：
  - 第四個 tab「⚡ 探測上限」：URL + 可接受回應時間（下拉）+ 最大 RPS 表單
  - `discover-live` 視圖（全面改版）：
    - **探測序列步進器**：預先渲染所有計畫 RPS 等級的圓點（pending → active → done/warn/fail），圓點間有連接線，一目了然知道探測在哪一步
    - **當前探測卡**：pulse 動點 + 「正在測試 X RPS」+ 已用秒數 / 總秒數計時器 + 進度條（`ElapsedMs / ProbeDurationMs`），每 500ms 由 WebSocket push 更新
    - **已完成探測表格**：顯示累積完成的每個探測點（RPS/P99/錯誤率/狀態）
    - 控制列：「N / Total 完成」計數器 + Stop 按鈕
  - `discover-result` 視圖：安全上限 / 臨界點兩張容量卡片、白話說明段落（根據實際探測資料顯示具體 p99 值或錯誤率，不再使用籠統文案）、探測詳情表格、重新探測按鈕
  - WebSocket 處理器新增 `discover_mode` 分支，與現有 live/result 流程完全隔離（`return` 防止干擾）
  - **新增 Vue ref**：`discoverCurrent`（當前探測快照）、`discoverProbeSeq`（全部計畫 RPS 序列）
  - **新增函式**：`probeStepClass(rps)` 根據探測結果或當前狀態回傳 CSS class（done/warn/fail/active/pending）
  - `discoverForm` reactive 物件（url/tolerance/maxRPS）；`discoverProbes`/`discoverResult` ref
  - `startDiscover()` 呼叫 `POST /api/discover`
- **header 狀態**：同步支援 `discover-live`（Scanning）與 `discover-result`（Done）狀態顯示
- **已知限制**：Discover 視圖無圖表（不需要）；步進器圓點點擊無互動（純展示）

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
| HIGH | **InfluxSink 強制 HTTP**（無 HTTPS），生產環境存在安全風險 | reporter/sink_influx.go |
| MED | CircuitBreaker 使用全域 atomic counter，高並發短暫抖動可能誤觸發 | engine/ramp.go |
| MED | `runRate()` worker 數 = maxRPS × 5（上限 5000），高 RPS 下記憶體壓力大 | engine/ramp.go |
| MED | Assertion 失敗不觸發 retry（retry 包裝在 executor 層，assertion 在其後） | engine/ramp.go, protocols/retry.go |
| MED | DNS 快取無大小上限（記憶體洩漏風險） | protocols/dns_cache.go |
| MED | WebSocket 無 CORS 限制、API 無認證 | dashboard/server.go |
| MED | Sample 緩衝區滿時靜默丟棄，無告警 | metrics/collector.go |
| MED | `stop` 命令直接 SIGKILL，Windows 不相容 | cmd/ramplio/stop.go |
| MED | regex capture 每次請求重新 Compile，高頻測試下有效能開銷 | engine/ramp.go |
| MED | CSV/InfluxDB Sink 只包含全域 Summary，缺少 per-step/group 明細 | reporter/sink_csv.go, sink_influx.go |
| MED | `WSExecutor` 無法在握手時設定自訂 headers（無 WebSocket 層認證） | protocols/ws.go |
| MED | `WSExecutor` 每次請求開新連線（無持久連線），不適合模擬真實 WS 用戶行為 | protocols/ws.go |
| LOW | `EvalCondition` parse 失敗靜默返回 true，設定錯誤不易察覺 | scenarios/template.go |
| LOW | `EvalCondition` 不支援 AND/OR 複合條件 | scenarios/template.go |
| LOW | HTML 報告無互動圖表 | reporter/html.go |
| LOW | mock server /profile 不驗證 token 值 | cmd/ramplio/mock.go |
| LOW | `{{env "X"}}` 語法與 `{{vars.X}}` 等不一致，學習曲線高 | scenarios/template.go |
| LOW | Verdict 判斷閾值寫死（errRate ≥ 5% = fail），無法自訂 | reporter/json.go |
| LOW | Setup 步驟執行結果（延遲、錯誤）不計入指標，debug 困難 | engine/ramp.go |

---

## 功能覆蓋對照（與主流工具比較）

> ✓ = 完整支援，△ = 部份支援，- = 不支援

| 功能 | JMeter | k6 | Vegeta | Ramplio | 缺口優先度 |
|------|:------:|:--:|:------:|:-------:|:---------:|
| 多階段 VU | ✓ | ✓ | - | ✓ | — |
| 固定 RPS | ✓ | ✓ | ✓ | ✓ | — |
| Ramping Arrival Rate | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| **Capacity Discovery（自動探測上限）** | - | - | - | ✓ | ✅ 差異化優勢 |
| YAML 場景 DSL | - | - | - | ✓ | 差異化優勢 |
| HAR Import | △ | ✓ | - | ✓ | — |
| 即時 TUI | - | △ | - | ✓ | 差異化優勢 |
| Web Dashboard | - | - | - | ✓ | 差異化優勢 |
| Guided Mode | - | - | - | ✓ | 差異化優勢 |
| JUnit XML | ✓ | ✓ | - | ✓ | — |
| Prometheus | △ | ✓ | - | ✓ | — |
| 多 Output Sink（CSV/InfluxDB） | ✓ | ✓ | ✓ | △ | ✅ 部份完成（缺 Loki） |
| WebSocket 測試（基礎） | ✓ | ✓ | - | △ | ✅ 部份完成（單次收發，無持久連線） |
| **gRPC 測試** | ✓ | ✓ | - | - | MED |
| Lifecycle Hooks（Setup/Teardown） | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| Transactions/Groups | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| Logic Controllers（If/Loop） | ✓ | - | - | △ | △ 部份（無 AND/OR） |
| **分散式測試** | ✓ | ✓ | ✓ | - | HIGH |
| 擴充 Thresholds（p50-max + per-step） | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| Script (JS/Groovy) | ✓ | ✓ | - | - | 刻意不做 |
| 資料檔案 | ✓ | ✓ | - | ✓ | — |
| 模板變數 | ✓ | ✓ | - | ✓ | — |
| Cookie Session | ✓ | ✓ | - | ✓ | — |
| Per-step 指標 | △ | - | - | ✓ | 差異化優勢 |
| 請求重試 + Circuit Breaker | ✓ | - | - | ✓ | ✅ 已完成 |
| Think Time（Pause） | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| **Test Suite（串接場景）** | ✓ | - | - | - | LOW |
| **Pause / Resume** | ✓ | ✓ | - | - | LOW |
| InfluxDB 輸出 | ✓ | ✓ | - | ✓ | ✅ 已完成 |
| **Grafana Loki 輸出** | - | △ | - | - | MED |

---

## 開發補足建議

### ✅ 已完成（b16d800 + WIP）

| 項目 | 狀態 |
|------|------|
| A1 Ramping Arrival Rate | `target_rps` per stage + 線性插值 |
| A2 多 Output Sink | `reporter.Sink` 介面 + CSV + InfluxDB v2 |
| A3 WebSocket 基礎 | `protocols/ws.go`（單次收發） |
| B1 Lifecycle Hooks | `setup`/`teardown` YAML 區塊 |
| B2 Transactions/Groups | `Step.Group` + GroupSummary |
| B3 擴充 Thresholds | p50~max + per-step thresholds |
| B4 請求重試 / Circuit Breaker | `RetryingExecutor` + `CircuitBreakerConfig` |
| B5 InfluxDB 輸出 | `InfluxSink`（line protocol v2） |
| B6 Logic Controllers（部份） | `Step.If` + `Step.Loop` |
| **C1 Capacity Discovery（CLI）** | `internal/discover/prober.go` + `cmd/ramplio/discover.go`；幾何級數探測序列、白話容量報告 |
| **C2 Capacity Discovery（Web UI）** | Dashboard 第四分頁「⚡ 探測上限」；WebSocket 即時推送探測進度；容量卡片報告視圖 |
| **C3 Discover 即時 UX 強化** | 探測序列步進器（全程可見所有 RPS 等級）；當前探測進度卡（pulse 點 + 計時器 + 進度條）；具體化錯誤訊息（顯示實際 p99 / 錯誤率而非籠統文案） |
| **C4 冷連線 bug 修復** | `Prober` 共享單一 `HTTPExecutor`（TCP/TLS 跨探測重用）；每探測點以 `SetupSteps` 暖身；解決跨 Region 測試時首個探測點因冷連線誤判 FAIL 的問題 |

---

### 現存缺口（建議下一步）

依優先度排列。

#### 新增缺口（本輪引入）

0. **Discover 探測序列無法自訂步距**
   - 現況：`baseSequence` 寫死（5, 10, 20, 40, 75, 125…），用戶無法插入自訂 RPS 點
   - 修法：新增 `--probe-sequence "5,10,25,50,100"` flag 覆蓋預設序列
   - 工作量：極小

0. **Discover 無法對多端點並行探測**
   - 現況：一次只探測單一 URL；測試 API 集群需分別執行
   - 修法：`--url` 接受多值或 YAML 清單，並行探測後各自輸出報告
   - 工作量：中

---

#### 緊急修補（安全/可靠性）

1. **InfluxSink HTTPS 支援**
   - 現況：`sink_influx.go:36` 硬編碼 `http://`，明文傳送 token
   - 修法：解析 DSN scheme（`influxdb://` → HTTP，`influxdbs://` → HTTPS），或直接偵測 `?tls=true`
   - 工作量：極小

2. **CircuitBreaker 防誤觸**
   - 現況：全域 atomic counter，任何 VU 錯誤都累計，高並發下易誤觸
   - 修法：可考慮時間窗口（sliding window）或分 VU 隔離
   - 工作量：小

#### HIGH 優先度

3. **分散式測試（Agent 模式）**
   - `ramplio agent --coordinator <addr>` 啟動 worker；coordinator 分發 stage 計畫並聚合指標
   - 影響：突破單機瓶頸，支援數萬 VU；對齊 JMeter Remote / k6 Cloud
   - 工作量：大（需 coordinator-worker 協定、指標聚合設計）

#### MED 優先度

4. **Sink 補充 per-step/group 明細**
   - `Sink` 介面擴充為 `WriteDetailed(sum Summary, steps []StepSummary, groups []GroupSummary)`
   - 影響：CSV/InfluxDB 可輸出細粒度指標，接入 Grafana dashboard
   - 工作量：小（介面調整 + 兩個 sink 補充欄位/measurement）

5. **WebSocket 持久連線模式**
   - 目前每次請求開新連線，不符合真實 WS 用戶行為
   - 新增 `ws_mode: persistent`；VU 保持連線並多次收發
   - 工作量：中（需設計 VU 生命週期中連線管理）

6. **WebSocket 握手 Headers**
   - 目前無法在 WS 握手時傳送 Authorization 等 header
   - 修法：`req.Headers` 映射到 `websocket.Dialer.Header`
   - 工作量：極小

7. **Loki Output Sink**
   - 補完 Phase B5（InfluxDB 已完成，Loki 未做）
   - DSN：`loki://host:port?labels=key=val`
   - 工作量：中

8. **gRPC 協定支援**
   - `protocols/grpc.go`；protobuf schema 從 `.proto` 載入
   - 工作量：大

9. **EvalCondition 加強**
   - 目前只支援單一 == / != 表達式；錯誤時靜默
   - 修法：加入 AND/OR；parse 失敗輸出警告而非靜默
   - 工作量：小

10. **engine/engine.go 清理**
    - 功能完全被 RampEngine 取代，仍保留造成維護負擔
    - 工作量：極小（刪除檔案 + 確認無引用）

11. **dashboard/controller.go 整合**
    - `dashboard/controller.go` 與 `cmd/ramplio/dashcontrol.go` 職責重疊
    - 工作量：小（重構）

#### LOW 優先度

| 功能 | 說明 |
|------|------|
| Test Suite | 多場景串接執行，整合 CI pipeline |
| Pause / Resume | 透過 REST API 暫停測試 |
| 自訂 Verdict 閾值 | 目前 FAIL 條件寫死（errRate ≥ 5% or p99 ≥ 1s） |
| Setup 步驟計入指標 | 方便 debug setup 請求效能 |
| RetryExecutor 指數退避 | 目前只支援固定 backoff，加 jitter 和指數退避更實用 |

---

## 如何進行增量更新

下次需要分析時，執行：

```bash
# 1. 取得上次分析後的異動（discover WIP 尚未 commit，分析基準仍為 b16d800）
git diff b16d800..HEAD --stat
git status --short   # 含未追蹤的新檔案

# 2. 只讀異動的檔案
# 3. 更新對應模組的條目
# 4. 更新 Metadata 的 commit hash 和分析時間
```

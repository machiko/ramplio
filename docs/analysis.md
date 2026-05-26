# Ramplio 專案分析快照

> 增量更新：讀此檔 → `git diff <last_analyzed_commit>..HEAD --stat` → 只掃異動檔案 → 更新對應條目與 Metadata。

---

## Metadata

| 欄位 | 值 |
|------|-----|
| 分析時間 | 2026-05-26 |
| 分析基準 commit | `72a7b52` |
| 完整 commit hash | `72a7b523c274a2c10f34318d1f2076d4ed174159` |

---

## 模組摘要

| 模組 | 說明 |
|------|------|
| `engine/ramp.go` | 多階段負載引擎：VU 模式（線性插值）+ Ramping RPS（token bucket）；Setup/Teardown lifecycle；Circuit Breaker（滑動時間窗口 ring buffer）；Step.If/Loop/Group/Pause/Retry；dataSource（sequential/random）；預編譯 regex capture cache |
| `scenarios/scenario.go` | YAML 場景資料結構：Stage、Step（Protocol/Auth/Capture/Assertions/Retry）、Thresholds（per-step）、CircuitBreakerConfig（WindowSeconds）、VarsFrom、ScenarioHTTP |
| `scenarios/parser.go` | YAML 解析與驗證（`Parse`/`ParseFile`）；VU/RPS 混用阻擋 |
| `scenarios/template.go` | `RenderString`/`RenderHeaders`/`EvalCondition`；支援 uuid/timestamp/env/vars/capture/data token；EvalCondition parse 失敗輸出 stderr 警告（不靜默） |
| `scenarios/datafile.go` | CSV/JSON 資料檔載入（`LoadDataFile`） |
| `scenarios/assertions.go` | 斷言評估：狀態碼/BodyContains/BodyMatches/BodyJSON/HeaderEquals |
| `protocols/http.go` | HTTP 執行器：連線池/cookie jar/超時/DNS 快取；回傳 RawSetCookies |
| `protocols/ws.go` | WebSocket 執行器：握手帶自訂 headers（排除標準 WS headers）；單次收發 |
| `protocols/retry.go` | RetryingExecutor：count/onCodes/backoff 固定延遲 |
| `protocols/dns_cache.go` | DNS TTL 快取；FIFO 淘汰（maxEntries=1024）|
| `protocols/executor.go` | Executor 介面；Request/Result（含 RawSetCookies） |
| `metrics/collector.go` | Channel 驅動聚集器；HDR 直方圖；per-step/group；緩衝區滿計數 dropped 並在 Stop() 輸出警告 |
| `metrics/summary.go` | 聚集統計；`RPS() = Total/WallTime`；`MeanLatency` |
| `metrics/sample.go` | 單一請求測量資料結構 |
| `reporter/terminal.go` | 靜態摘要（繁中）；per-step/group 表格；droppedSamples 警告；PrintInterpretation |
| `reporter/html.go` | go:embed HTML 報告 |
| `reporter/json.go` | Summary ↔ JSON；Verdict 計算（閾值寫死）|
| `reporter/tui.go` | Bubbletea 即時終端儀表板 |
| `reporter/prometheus.go` | `/metrics` 端點，500ms 更新 |
| `reporter/junit.go` | JUnit XML（CI/CD 用） |
| `reporter/sink_csv.go` | CSV Sink：追加寫入全域 Summary |
| `reporter/sink_influx.go` | InfluxDB v2 Sink；支援 `influxdb://`（HTTP）與 `influxdbs://`（HTTPS）|
| `reporter/sink_factory.go` | `ParseSink(dsn)` 路由 |
| `dashboard/server.go` | HTTP + WebSocket 儀表板伺服器；Bearer Token 保護（POST 端點 + WS `?token=`）；端點：`/api/run,stop,status,scenario,import-har,report,discover` |
| `dashboard/controller.go` | Controller 介面；RunRequest/DiscoverRequest；Discover 回調型別 |
| `dashboard/guided.go` | PM 導向精靈：steady/spike/soak → RampPlan；GuidedVerdict |
| `dashboard/embed.go` | go:embed `templates/dashboard.html`（Vue 3 SPA；4 分頁含 Discover） |
| `internal/discover/prober.go` | 容量自動探測：幾何級數 RPS 序列；共享 HTTPExecutor；onProbeStart/onProbe 回調 |
| `importer/har.go` | HAR JSON 解析 |
| `importer/converter.go` | HAR → YAML 場景；自動偵測認證/captures/JWT |
| `importer/detector.go` | HAR 啟發式偵測（pre-auth token、login entry）|
| `importer/filter.go` | 靜態資產過濾（JS/CSS/圖片/字體）|
| `cmd/ramplio/run.go` | `run` 命令：URL/RPS/場景/儀表板四模式；`--ignore-errors`；`--dashboard-token` |
| `cmd/ramplio/dashcontrol.go` | 儀表板 Controller 實作；Discover 狀態機；buildOverrideStages |
| `cmd/ramplio/validate.go` | `validate` 命令：stages/steps/captures/vars/thresholds/circuit breaker 詳細輸出 |
| `cmd/ramplio/stop.go` | `stop` 命令：SIGTERM → 5s 輪詢 → SIGKILL 升級 |
| `cmd/ramplio/discover.go` | `discover` 命令；探測序列預估；即時列印每探測點 |
| `cmd/ramplio/init.go` | `init` 引導精靈：cookie/JWT 認證；steady/spike/soak 流量模式 |
| `cmd/ramplio/import.go` | `import` 命令：HAR → YAML |
| `cmd/ramplio/report.go` | `report` 命令：JSON → HTML |
| `cmd/ramplio/mock.go` | 本地 mock HTTP 伺服器 |

---

## 已知問題與技術債

| 優先度 | 項目 | 位置 |
|--------|------|------|
| HIGH | 分散式測試（Agent 模式）缺失 | 架構層 |
| MED | `dashboard/controller.go` 與 `dashcontrol.go` 職責重疊 | 兩者 |
| MED | `runRate()` worker 數 = maxRPS×5（上限 5000），高 RPS 記憶體壓力 | engine/ramp.go |
| MED | Assertion 失敗不觸發 retry（assertion 在 executor 後才評估）| engine/ramp.go |
| MED | Sink（CSV/InfluxDB）只含全域 Summary，缺 per-step/group 明細 | reporter/sink_*.go |
| MED | WebSocket 每次請求開新連線（無持久連線）| protocols/ws.go |
| LOW | `EvalCondition` 不支援 AND/OR 複合條件 | scenarios/template.go |
| LOW | HTML 報告無互動圖表 | reporter/html.go |
| LOW | Verdict 判斷閾值寫死（errRate≥5% or p99≥1s）| reporter/json.go |
| LOW | Setup 步驟執行結果不計入指標 | engine/ramp.go |
| LOW | RetryExecutor 只支援固定 backoff（無指數退避/jitter）| protocols/retry.go |
| LOW | `stop` 命令 Unix-only（lsof/kill）| cmd/ramplio/stop.go |
| LOW | Discover 探測序列寫死（無法自訂步距）| discover/prober.go |

---

## 功能覆蓋對照

> ✓ 完整支援　△ 部份支援　- 不支援

| 功能 | JMeter | k6 | Vegeta | Ramplio |
|------|:------:|:--:|:------:|:-------:|
| 多階段 VU | ✓ | ✓ | - | ✓ |
| 固定 RPS / Ramping RPS | ✓ | ✓ | ✓ | ✓ |
| Capacity Discovery | - | - | - | ✓ |
| YAML 場景 DSL | - | - | - | ✓ |
| HAR Import | △ | ✓ | - | ✓ |
| 即時 TUI | - | △ | - | ✓ |
| Web Dashboard + Guided Mode | - | - | - | ✓ |
| JUnit XML / Prometheus | ✓ | ✓ | - | ✓ |
| CSV / InfluxDB Sink | ✓ | ✓ | ✓ | △ |
| WebSocket | ✓ | ✓ | - | △ |
| gRPC | ✓ | ✓ | - | - |
| Lifecycle Hooks（Setup/Teardown）| ✓ | ✓ | - | ✓ |
| Groups / Transactions | ✓ | ✓ | - | ✓ |
| Per-step Thresholds | ✓ | ✓ | - | ✓ |
| Retry + Circuit Breaker | ✓ | - | - | ✓ |
| Logic Controllers（If/Loop）| ✓ | - | - | △ |
| 分散式測試 | ✓ | ✓ | ✓ | - |
| Cookie / Data File | ✓ | ✓ | - | ✓ |

---

## 下一步建議

依優先度排列（Sprint S1 & S2 已於 `72a7b52` 完成）：

1. **Sink per-step/group 明細**（MED，小）— `Sink` 介面擴充 `WriteDetailed`，CSV/InfluxDB 補充細粒度欄位
2. **WebSocket 持久連線模式**（MED，中）— `ws_mode: persistent`，VU 生命週期內保持連線
3. **Loki Output Sink**（MED，中）— DSN `loki://host:port?labels=key=val`
4. **gRPC 協定支援**（MED，大）— `protocols/grpc.go` + .proto 載入
5. **分散式測試**（HIGH，大）— coordinator-worker 協定 + 指標聚合
6. **EvalCondition AND/OR**（LOW，小）
7. **Test Suite（多場景串接）**（LOW，中）

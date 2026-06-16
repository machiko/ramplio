# Ramplio 專案分析快照

> 增量更新：讀此檔 → `git diff <last_analyzed_commit>..HEAD --stat` → 只掃異動檔案 → 更新對應條目與 Metadata。

---

## Metadata

| 欄位 | 值 |
|------|-----|
| 分析時間 | 2026-06-16 |
| 分析基準 commit | `f09a456` |
| 完整 commit hash | `f09a456dd770ee9a1e61c998225b06cc19a330c4` |

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
| `scenarios/eval_condition.go` | **新增** 條件運算式 Lexer/Parser/AST：支援 AND/OR/NOT、括號、`== != < <= > >=`；運算子優先序 NOT>AND>OR；`EvalCondition` 解析失敗時印警告並回傳 true（不跳過步驟） |
| `distributed/coordinator.go` | **新增** 協調者：health check → VU 分配（整數最大餘數法）→ broadcast `/assign` → 輪詢 `/live`（1s）聚合 → 收集 `/result` → `mergePartials` 合併 |
| `distributed/worker.go` | **新增** 工作節點：`/assign /stop /live /result /health` HTTP 端點；`scaleScenario` 依分配 VU 等比縮放 stage target；單節點跑 RampEngine |
| `distributed/api.go` | **新增** 分散式 DTO：AssignRequest / PartialSummary / LiveMetricsResponse / StatusResponse |
| `config/distributed.go` | **新增** DistributedConfig（Workers/ListenAddr/Secret/PollIntervalMs/AssignTimeoutSec）；Secret 與部分欄位定義但未被使用 |
| `reporter/sink.go` | **新增** Sink 介面 + DetailedSink 選用介面（WriteDetailed 輸出 per-step/group 明細） |
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
| `cmd/ramplio/run.go` | `run` 命令：URL/RPS/場景/儀表板/**分散式**五模式；`--worker`（可重複）；`--ignore-errors`；`--dashboard-token` |
| `cmd/ramplio/worker.go` | **新增** `worker` 命令：啟動工作節點 HTTP server（`--addr`）；SIGINT/SIGTERM 優雅關閉 |
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
| MED | 分散式僅走明文 HTTP（`http://`），無 TLS；poll interval 寫死 1s（`PollIntervalMs` 未生效）| distributed/coordinator.go |
| MED | `dashboard/controller.go` 與 `dashcontrol.go` 職責重疊 | 兩者 |
| MED | `runRate()` worker 數 = maxRPS×5（上限 5000），高 RPS 記憶體壓力 | engine/ramp.go |
| MED | Assertion 失敗不觸發 retry（assertion 在 executor 後才評估）| engine/ramp.go |
| MED | WebSocket 每次請求開新連線（無持久連線）| protocols/ws.go |
| LOW | HTML 報告無互動圖表 | reporter/html.go |
| LOW | Verdict 判斷閾值寫死（errRate≥5% or p99≥1s）| reporter/json.go |
| LOW | Setup 步驟執行結果不計入指標 | engine/ramp.go |
| LOW | RetryExecutor 只支援固定 backoff（無指數退避/jitter）| protocols/retry.go |
| LOW | `stop` 命令 Unix-only（lsof/kill）| cmd/ramplio/stop.go |
| LOW | Discover 探測序列寫死（無法自訂步距）| discover/prober.go |

> 已解決（自 `72a7b52`）：~~分散式測試缺失~~、~~Sink per-step 明細~~、~~EvalCondition AND/OR~~。
> 已解決（Phase 4，本次）：~~分散式百分位合併錯誤~~（改用 HDR 直方圖序列化合併，`metrics.MergeExports`，含 step/group 明細）、~~repo 產物被追蹤~~（已移除並補 `.gitignore`）、~~Worker 端點無認證~~（shared secret + Bearer middleware）、~~分散式 Setup/Teardown stub~~（coordinator 集中執行 setup，captures 透過 `RampConfig.SeedCaptures` 廣播注入 worker）。

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
| CSV / InfluxDB Sink（含 per-step 明細）| ✓ | ✓ | ✓ | ✓ |
| WebSocket | ✓ | ✓ | - | △ |
| gRPC | ✓ | ✓ | - | - |
| Lifecycle Hooks（Setup/Teardown）| ✓ | ✓ | - | ✓ |
| Groups / Transactions | ✓ | ✓ | - | ✓ |
| Per-step Thresholds | ✓ | ✓ | - | ✓ |
| Retry + Circuit Breaker | ✓ | - | - | ✓ |
| Logic Controllers（If/Loop + AND/OR/NOT）| ✓ | - | - | ✓ |
| 分散式測試 | ✓ | ✓ | ✓ | ✓（HDR 直方圖合併、shared secret 認證、集中 setup/teardown；TLS 待補）|
| Cookie / Data File | ✓ | ✓ | - | ✓ |

---

## 下一步建議

分散式測試的「快樂路徑」已通，但有正確性、安全性、認證三道缺口讓它尚不可用於真實場景。下一階段應為 **「分散式硬化」**——把 Phase 3 的骨架補成可信賴的功能，而非急著開新功能。

**Phase 4 — 分散式硬化（✅ 已完成）**
1. ✅ **HDR 直方圖合併** — `metrics.HistogramExport` + `Collector.Export()` + `MergeExports()`；worker 序列化直方圖、coordinator 合併求真實百分位（含 step/group）；測試以「合併結果 == 單一 collector 全量 ground truth」驗證正確性
2. ✅ **Worker 認證** — `Worker.SetSecret` + Bearer middleware；`Coordinator.SetSecret` 帶 Authorization header；CLI `--secret`/`--worker-secret` 與 `RAMPLIO_WORKER_SECRET` env
3. ✅ **分散式 Setup/Teardown** — engine 暴露 `RunSetup`/`RunTeardown` 與 `RampConfig.SeedCaptures`；coordinator 集中執行 setup，captures 廣播注入各 worker
4. ✅ **repo 衛生** — 移除追蹤的 `ramplio`/`sessions.csv`/`scenario.yaml`，補 `.gitignore`

**Phase 5+ — 功能擴張**
1. **分散式 TLS + 設定生效**（MED，中）— 支援 `https://` worker 連線；讓 `PollIntervalMs`/`AssignTimeoutSec` 真正套用
2. **WebSocket 持久連線模式**（MED，中）— `ws_mode: persistent`，VU 生命週期內保持連線
3. **gRPC 協定支援**（MED，大）— `protocols/grpc.go` + .proto 載入
4. **Loki Output Sink**（MED，中）— DSN `loki://host:port?labels=key=val`
5. **Test Suite（多場景串接）**（LOW，中）

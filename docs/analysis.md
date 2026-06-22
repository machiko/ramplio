# Ramplio 專案分析快照

> 增量更新：讀此檔 → `git diff <last_analyzed_commit>..HEAD --stat` → 只掃異動檔案 → 更新對應條目與 Metadata。

---

## Metadata

| 欄位 | 值 |
|------|-----|
| 分析時間 | 2026-06-22 |
| 分析基準 commit | `ed8b0a0` |
| 完整 commit hash | `ed8b0a0172a6a35b397979e94ea3a206720936e6` |

---

## 模組摘要

| 模組 | 說明 |
|------|------|
| `engine/ramp.go` | 多階段負載引擎：VU 模式（線性插值）+ Ramping RPS（**單一 dispatcher + worker 池**，dispatcher 以 Reserve+capped sleep 排定每請求「應送時間戳」注入 buffered jobs channel，避免 rate 0 卡死並按 ramp 追隨速率）；Setup/Teardown lifecycle；Circuit Breaker（滑動時間窗口 ring buffer）；Step.If/Loop/Group/Pause/Retry；dataSource（sequential/random）；預編譯 regex capture cache |
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
| `protocols/trace.go` | **新增** `ExecuteTraced`：httptrace 拆單發請求 DNS/TCP/TLS/TTFB/total + Reused 旗標；僅供診斷（pre-flight），hot path Execute 不受影響 |
| `scenarios/eval_condition.go` | **新增** 條件運算式 Lexer/Parser/AST：支援 AND/OR/NOT、括號、`== != < <= > >=`；運算子優先序 NOT>AND>OR；`EvalCondition` 解析失敗時印警告並回傳 true（不跳過步驟） |
| `distributed/coordinator.go` | **新增** 協調者：health check → VU 分配（整數最大餘數法）→ broadcast `/assign` → 輪詢 `/live`（1s）聚合 → 收集 `/result` → `mergePartials` 合併 |
| `distributed/worker.go` | **新增** 工作節點：`/assign /stop /live /result /health` HTTP 端點；`scaleScenario` 依分配 VU 等比縮放 stage target；單節點跑 RampEngine |
| `distributed/api.go` | **新增** 分散式 DTO：AssignRequest / PartialSummary / LiveMetricsResponse / StatusResponse |
| `config/distributed.go` | **新增** DistributedConfig（Workers/ListenAddr/Secret/PollIntervalMs/AssignTimeoutSec）；Secret 與部分欄位定義但未被使用 |
| `reporter/sink.go` | **新增** Sink 介面 + DetailedSink 選用介面（WriteDetailed 輸出 per-step/group 明細） |
| `metrics/collector.go` | Channel 驅動聚集器；HDR 直方圖（service + **corrHist 協調遺漏修正**：`At−ScheduledAt` floor 於 service latency）；per-step/group；緩衝區滿計數 dropped 並在 Stop() 輸出警告 |
| `metrics/errorkind.go` | **新增** 失敗分類：`ClassifyError(err,status)→ErrorKind`（DNS/連線被拒/中斷/逾時/TLS/4xx/5xx/斷言/其他）；以 `errors.As` 拆 url→net→syscall/x509，status>0+err≠nil 判定為斷言失敗；`DominantErrorKind` 取主因與占比；`DisplayOrder` 穩定排序 |
| `metrics/summary.go` | 聚集統計；`RPS() = Total/WallTime`；`MeanLatency`；`ErrorBreakdown map[ErrorKind]int64`（record() 失敗時懶初始化分類累加） |
| `metrics/export.go` | 分散式 HDR 合併；`HistogramExport.ErrorBreakdown` 跨節點加總（`Export`/`MergeExports`） |
| `metrics/sample.go` | 單一請求測量資料結構 |
| `reporter/terminal.go` | 靜態摘要（繁中）；per-step/group 表格；**失敗原因分類小表**（`ErrorBreakdownRows`）；droppedSamples 警告；PrintInterpretation |
| `reporter/html.go` | go:embed HTML 報告 |
| `reporter/json.go` | Summary ↔ JSON；`error_breakdown` 失敗分類列；Verdict = `Interpret(sum)`（共用白話解讀）|
| `reporter/interpret.go` | 單一來源白話解讀 `Interpret(sum)`：整體結論／反應速度／穩定度／承受能力／一句話總結；門檻統一（fail err≥5% 或 p99≥3s；warn err≥1% 或 p99≥1s）；**rate 模式整體結論改採 corrected p99（`verdictP99`）**，誠實反映使用者實感；終端／JSON／HTML 共用，Dashboard JS 鏡像同門檻與用語 |
| `reporter/confidence.go` | **新增** `MeasurementConfidence`：依產生器自我健康度（丟樣本比例 / GC 佔測試時長）給高／中等／偏低三級可信度判語；供終端、JSON、診斷共用 |
| `reporter/errorcause.go` | **新增** 失敗歸因白話：`errorCopyByKind` 單一來源（每類 Title/Cause/Action/Label）；`failureCauseFinding` 產最高優先 Finding（err≥1% 才出）；`reachabilityDominates` 抑制連線層失敗被誤判為「超出負荷」；`ErrorBreakdownRows` 表格列；`ExplainErrorKind`/`IsReachabilityFailure` 供 pre-flight 使用 |
| `reporter/tui.go` | Bubbletea 即時終端儀表板 |
| `reporter/prometheus.go` | `/metrics` 端點，500ms 更新 |
| `reporter/junit.go` | JUnit XML（CI/CD 用） |
| `reporter/sink_csv.go` | CSV Sink：追加寫入全域 Summary |
| `reporter/sink_influx.go` | InfluxDB v2 Sink；支援 `influxdb://`（HTTP）與 `influxdbs://`（HTTPS）|
| `reporter/sink_loki.go` | Grafana Loki Sink：`loki://`/`lokis://`；指標以 JSON log line 推送，stream label 低基數（job+scenario+使用者 labels），支援 `?labels=`/`?org=`/`?token=` |
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
| `cmd/ramplio/run.go` | `run` 命令：URL/RPS/場景/儀表板/**分散式**五模式；`--worker`（可重複）；`--ignore-errors`；`--dashboard-token`；**開跑前 pre-flight 預檢**（`--no-preflight` 略過）|
| `cmd/ramplio/preflight.go` | 開跑前單發探測：`preflightTarget`（URL 模式直用；場景取第一步、含 `{{` 模板則跳過）；`runPreflight` 僅在連線層硬錯（DNS/連線被拒/TLS）時白話中止，逾時/4xx/5xx 不擋；**連得上時印「連線分解」**（DNS/連線/TLS/TTFB，用 `ExecuteTraced`）|
| `cmd/ramplio/worker.go` | **新增** `worker` 命令：啟動工作節點 HTTP server（`--addr`）；SIGINT/SIGTERM 優雅關閉 |
| `cmd/ramplio/dashcontrol.go` | 儀表板 Controller 實作；Discover 狀態機；buildOverrideStages |
| `cmd/ramplio/validate.go` | `validate` 命令：stages/steps/captures/vars/thresholds/circuit breaker 詳細輸出 |
| `cmd/ramplio/stop.go` | `stop` 命令：SIGTERM → 5s 輪詢 → SIGKILL 升級 |
| `cmd/ramplio/discover.go` | `discover` 命令；探測序列預估；即時列印每探測點 |
| `cmd/ramplio/init.go` | `init` 引導精靈：cookie/JWT 認證；steady/spike/soak 流量模式 |
| `cmd/ramplio/import.go` | `import` 命令：HAR → YAML |
| `cmd/ramplio/report.go` | `report` 命令：JSON → HTML |
| `cmd/ramplio/mock.go` | 本地 mock HTTP 伺服器；**確定性延遲注入**（`--latency` 固定 / `--latency-fast`+`--latency-slow`+`--slow-pct` 雙峰）；`latencyProfile.pickLatency` 純函式可單測，作為 ground-truth 驗證標的 |
| `internal/engine/groundtruth_test.go` | **新增** ground-truth 自我驗證：對已知延遲分佈施壓，斷言量測百分位在容差內（固定延遲 + 雙峰分尾） |

---

## 已知問題與技術債

| 優先度 | 項目 | 位置 |
|--------|------|------|
| MED | `dashboard/controller.go` 與 `dashcontrol.go` 職責重疊 | 兩者 |
| LOW | 單機 `runScenario` 仍依賴 TUI，非 TTY 下亦會提早結束（分散式已修，單機未做）| cmd/ramplio/run.go |
| MED | WebSocket 每次請求開新連線（無持久連線）| protocols/ws.go |
| LOW | HTML 報告無互動圖表 | reporter/html.go |
| LOW | Setup 步驟執行結果不計入指標 | engine/ramp.go |
| LOW | RetryExecutor 只支援固定 backoff（無指數退避/jitter）| protocols/retry.go |
| LOW | `stop` 命令 Unix-only（lsof/kill）| cmd/ramplio/stop.go |
| LOW | Discover 探測序列寫死（無法自訂步距）| discover/prober.go |

> 已解決（自 `72a7b52`）：~~分散式測試缺失~~、~~Sink per-step 明細~~、~~EvalCondition AND/OR~~。
> 已解決（Phase 4）：~~分散式百分位合併錯誤~~（改用 HDR 直方圖序列化合併，`metrics.MergeExports`，含 step/group 明細）、~~repo 產物被追蹤~~（已移除並補 `.gitignore`）、~~Worker 端點無認證~~（shared secret + Bearer middleware）、~~分散式 Setup/Teardown stub~~（coordinator 集中執行 setup，captures 透過 `RampConfig.SeedCaptures` 廣播注入 worker）、~~worker 執行 context 隨 HTTP 請求取消導致 0 請求~~（改用 `context.WithoutCancel`）。
> 失敗白話化（2026-06-19）：新增失敗分類（`metrics.ErrorKind`/`ClassifyError`）、失敗歸因白話（`reporter/errorcause.go`，連線被拒/DNS/TLS/逾時/4xx/5xx/斷言各有人話+下一步）、修正連線層失敗被誤判為「超出負荷」、開跑前 pre-flight 預檢（連線層硬錯即時白話中止）。錯誤分類隨 `HistogramExport` 跨節點合併。
> 鞏固設計+透明度（P2，2026-06-22）：~~runRate worker = maxRPS×5 記憶體壓力~~（改 **grow-on-demand**：`ratePool` idle/total/capHit，dispatcher 送前 `maybeGrow`，低延遲目標只生 ~需求量，達 cap 才阻塞——完整保留 CO 背壓語義；CO 回歸測試全綠）、~~assertion 失敗不觸發 retry 疑似漏洞~~（**釐清為刻意設計**：assertion 失敗代表真實缺陷，retry 會掩蓋並虛低錯誤率，比照 k6/Gatling 不 retry；補 `assertion_retry_test.go` 回歸守 + ramp.go 意圖註解）。新增透明度：`Summary.GeneratorWorkerCapHit` → rate 達 worker 上限時，「量測可信度」判語標示「產生器自身可能是瓶頸」，避免假性 overload 被誤歸因於目標。
> 已解決（Phase 5）：~~分散式僅明文 HTTP / 無 TLS~~（worker `ListenAndServeTLS` + coordinator scheme-aware URL 與可注入 TLS client；CLI `--tls-cert/--tls-key/--tls-ca/--tls-skip-verify`）、~~PollIntervalMs/AssignTimeoutSec 未生效~~（coordinator `SetTiming` + CLI `--poll-interval/--assign-timeout`，config helper）。

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
| 分散式測試 | ✓ | ✓ | ✓ | ✓（HDR 直方圖合併、shared secret 認證、集中 setup/teardown、TLS、可調 poll/timeout）|
| Cookie / Data File | ✓ | ✓ | - | ✓ |

---

## 公信力（Credibility）路線

> 目標：讓量測數據有公信力，不靠「跟 k6/JMeter 比一比」（會寄生在別人工具正確性上），改以數學 ground-truth 自證 + 修正方法論瑕疵。詳見 `docs/accuracy.md`。

- **Phase 1 — Ground-truth 自我驗證（✅ 已完成）**：`mock.go` 確定性延遲注入（固定/雙峰）；`groundtruth_test.go` 對已知分佈施壓斷言百分位在容差內；`docs/accuracy.md` 方法論。
- **Phase 2 — Coordinated Omission 修正（✅ 已完成）**：`runRate` 改單一 dispatcher（Reserve+capped sleep，避免 rate 0 卡死）+ worker 池消費排定時間戳；`Sample.ScheduledAt` 起算 `corrected = At−ScheduledAt`（floor 於 service）；collector 第二條 `corrHist`；`Summary.CorrectedP*`/`HasCorrected`；`export.go` 跨節點合併 corrected；reporter 整體結論改採 corrected p99（`verdictP99`）、終端「壓力下實際延遲」區塊、JSON `corrected_latency`、diagnose 新增「請求速率超過系統能消化的速度」歸因。VU 模式不套用。
- **Phase 3 — 量測透明度（✅ 已完成）**：`protocols/trace.go` `ExecuteTraced` 用 httptrace 拆 DNS/TCP/TLS/TTFB/total（僅診斷用，hot path 不付成本），pre-flight 連得上時印「連線分解」；collector 取樣尖峰 goroutine + 跑前後 GC 暫停差值 → `Summary.Generator*`；`reporter/confidence.go` `MeasurementConfidence` 三級判語（丟樣本比例 / GC 佔比），終端「量測可信度」區塊、JSON `measurement_confidence`、診斷新增「量測可能被產生器自身的 GC 干擾」。

---

## 下一步建議

分散式測試的「快樂路徑」已通，但有正確性、安全性、認證三道缺口讓它尚不可用於真實場景。下一階段應為 **「分散式硬化」**——把 Phase 3 的骨架補成可信賴的功能，而非急著開新功能。

**Phase 4 — 分散式硬化（✅ 已完成）**
1. ✅ **HDR 直方圖合併** — `metrics.HistogramExport` + `Collector.Export()` + `MergeExports()`；worker 序列化直方圖、coordinator 合併求真實百分位（含 step/group）；測試以「合併結果 == 單一 collector 全量 ground truth」驗證正確性
2. ✅ **Worker 認證** — `Worker.SetSecret` + Bearer middleware；`Coordinator.SetSecret` 帶 Authorization header；CLI `--secret`/`--worker-secret` 與 `RAMPLIO_WORKER_SECRET` env
3. ✅ **分散式 Setup/Teardown** — engine 暴露 `RunSetup`/`RunTeardown` 與 `RampConfig.SeedCaptures`；coordinator 集中執行 setup，captures 廣播注入各 worker
4. ✅ **repo 衛生** — 移除追蹤的 `ramplio`/`sessions.csv`/`scenario.yaml`，補 `.gitignore`

**Phase 5 — 分散式 TLS + 設定生效（✅ 已完成）**
- ✅ **分散式 TLS** — worker `SetTLS` + `ListenAndServeTLS`；coordinator scheme-aware URL（worker 位址可帶 `https://`）+ 可注入 `*http.Client`；CLI `worker --tls-cert/--tls-key`、`run --tls-ca/--tls-skip-verify`
- ✅ **設定生效** — `config.DistributedConfig.PollInterval()/AssignTimeout()` helper；coordinator `SetTiming`；CLI `run --poll-interval/--assign-timeout`

**Phase 6 — Loki sink + 分散式 headless（✅ 已完成）**
- ✅ **Loki Output Sink** — `reporter/sink_loki.go`，DSN `loki://host:port?labels=k=v&org=&token=`（含 `lokis://` HTTPS）；指標以 JSON log line 推送，含 per-step/group 明細
- ✅ **分散式 headless 輸出** — `run --worker` 在非 TTY（CI/pipe）或 `--no-tui` 時改印進度行；修正分散式 run 在 pipeline 中提早結束的問題，並讓整個分散式流程可透過 CLI 端到端完成

**Phase 7+ — 功能擴張（未做）**
1. **WebSocket 持久連線模式**（MED，中）— `ws_mode: persistent`，VU 生命週期內保持連線
2. **gRPC 協定支援**（MED，大）— `protocols/grpc.go` + .proto 載入
3. **單機 headless**（LOW，小）— `runScenario` 比照分散式做非 TTY fallback
4. **Test Suite（多場景串接）**（LOW，中）

# Plan: `ramplio verify` 一鍵量測自證

## Summary
把「容量回答機」定位裡那句信任背書——「數字你能自己驗證」——從**一段文字**變成**一個指令**。`ramplio verify` 在程式內起一個注入已知延遲的 mock server、對它跑短負載、把量到的百分位和注入的 ground truth 比對，印出白話的「✓ 量測準確 / ✗ 量測失準」判語並以 exit code 表態（CI 可用）。本質是把現有 `internal/engine/groundtruth_test.go` 的驗證邏輯變成使用者面向的指令。

## User Story
作為一個正在評估要不要採用 Ramplio 的工程師，
我想要用一個指令、在自己的機器上，親眼證明這工具「沒測錯」，
這樣我才敢把它報出的容量數字拿去做決策——不必靠「跟 k6 比一比」，也不必手動開兩個終端做 mock-server 對拍。

## Problem → Solution
**現狀**：公信力的自證能力分散在兩處——`groundtruth_test.go`（只有跑 `go test` 的開發者看得到）與 `ramplio mock-server` + 手動 `run` + 肉眼比對（兩終端、要自己算誤差）。`discover` 報告末尾叫使用者「用 mock-server 自行驗證」，但那是手動苦工。
**目標**：`ramplio verify` 一個指令端到端完成「注入已知延遲 → 施壓 → 量測 → 比對 → 白話判語 + exit code」。讓「可驗證的公信力」從論述變成 30 秒可重現的體驗。

## Metadata
- **Complexity**: Medium
- **Source PRD**: N/A（源自 `/ecc:prp-plan` 評估建議的 P1）
- **PRD Phase**: N/A
- **Estimated Files**: 3 新 + 4 改 ≈ 7

---

## UX Design

### Before
```
# 開發者要證明工具準，得自己跑：
$ go test ./internal/engine/ -run TestGroundTruth -v   # 只有開發者會做

# 或使用者手動兩終端對拍：
$ ramplio mock-server --latency 50ms &
$ ramplio run --url http://localhost:8080 --rps 200 --duration 20s
# …然後自己看 p50 是不是 ≈ 50ms（要自己算誤差、自己判讀）
```

### After
```
$ ramplio verify
  量測自證 — 對已知延遲分佈施壓，反推 Ramplio 量得準不準
  注入分佈：固定 50ms    施壓：10 VU × 3s    容差：±20ms

  量測結果（注入值 → 量到值）
    p50   50ms → 51ms   ✓
    p90   50ms → 52ms   ✓
    p95   50ms → 52ms   ✓
    p99   50ms → 54ms   ✓

  ✓ 量測準確：所有百分位都落在注入值 +0~20ms 內。
    量測值只會 ≥ 注入值（多了本機往返），這次沒有低於——代表沒有低報延遲的 bug。
$ echo $?
0
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 證明工具準確 | `go test`（開發者）或手動兩終端對拍 | `ramplio verify` 一鍵 | 新指令 |
| `discover` 報告信任背書 | 「ramplio mock-server 注入已知延遲自行驗證」（手動） | 「跑 ramplio verify 一鍵驗證工具量測準不準」 | 收尾閉環 |
| CI 工具可信度把關 | 無 | `ramplio verify` 失準回 exit 1 | 可放進 pipeline |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `internal/engine/groundtruth_test.go` | 全 (1-97) | **驗證邏輯的藍本**：注入分佈、跑 engine、斷言百分位在 [injected, injected+tol]、雙峰 p50 落快帶/p99 落慢帶 |
| P0 | `cmd/ramplio/mock.go` | 17-53, 104-149, 183-188 | `latencyProfile` / `pickLatency` / `describe` / mock handler 與 flag 定義——verify 要復用 |
| P0 | `internal/discover/prober.go` | 132-172 | 程式內起 engine 跑短負載的範例（NewCollector + NewRamp + Run） |
| P1 | `internal/metrics/summary.go` | 5-29 | `Summary` 欄位：`Total` / `P50` / `P90` / `P95` / `P99`；`ErrorRate()` 方法 |
| P1 | `cmd/ramplio/discover.go` | 80-89, 報告末尾信任背書列 | 命令骨架（flag/RunE）與要改的背書措辭；`io.Writer` 純函式輸出 + `displayWidth` 對齊模式 |
| P1 | `cmd/ramplio/main.go` | 23-33 | 註冊新指令 `rootCmd.AddCommand(newVerifyCmd())` |
| P2 | `docs/accuracy.md` | 46-68 | 「自己動手驗證」段落，要改成一鍵 verify |

## External Documentation
無需外部研究——全部復用既有內部模式（cobra 指令、`latencyProfile`、engine 跑負載、`Summary` 百分位）。

---

## Patterns to Mirror

### GROUND_TRUTH_ASSERTION（驗證數學的權威來源）
```go
// SOURCE: internal/engine/groundtruth_test.go:55-59
const tol = 20 * time.Millisecond
assert.GreaterOrEqual(t, sum.P50, known, "p50 must not undercut injected latency")
assert.LessOrEqual(t, sum.P50, known+tol, "p50 drifted beyond tolerance")
// 雙峰（74-96）：p50 ∈ [fast, fast+tol]、p99 ∈ [slow, slow+tol]
```

### LATENCY_PROFILE（復用，不要重寫延遲注入）
```go
// SOURCE: cmd/ramplio/mock.go:22-39
type latencyProfile struct { Fixed, Fast, Slow time.Duration; SlowPct int }
func (p latencyProfile) pickLatency(n int64) time.Duration { ... } // 純函式
```

### RUN_LOAD_IN_PROCESS（程式內跑短負載）
```go
// SOURCE: internal/discover/prober.go:146-156
col := metrics.NewCollector(workerCount)
eng := engine.NewRamp(engine.RampConfig{
	Stages:   []scenarios.Stage{{Duration: p.cfg.ProbeDuration, TargetRPS: targetRPS}},
	Steps:    []engine.RampStep{{Request: req}},
	Executor: p.executor,
}, col)
sum := eng.Run(ctx)
```

### CLI_COMMAND_SHAPE（指令骨架 + io.Writer 純函式輸出）
```go
// SOURCE: cmd/ramplio/discover.go:17-89（命令）、guidance.go:22-27（io.Writer 輸出）
func newDiscoverCmd() *cobra.Command {
	var url string
	cmd := &cobra.Command{Use: "discover", Short: "...", RunE: func(...) error { ... }}
	cmd.Flags().StringVarP(&url, "url", "u", "", "...")
	return cmd
}
```

### CHINESE_BOX_ALIGN（若用框線，沿用 displayWidth）
```go
// SOURCE: cmd/ramplio/discover.go（本專案剛加入）
func displayWidth(s string) int { /* ASCII=1, 其餘=2 */ }
```
> verify 輸出用簡單縮排表格即可（如 UX 範例），不一定要畫框；若畫框務必用 `displayWidth`。

### TEST_STRUCTURE
```go
// SOURCE: cmd/ramplio/discover_test.go（本專案剛加入）
var buf bytes.Buffer
writeXxx(&buf, ...)
if !strings.Contains(buf.String(), want) { t.Errorf(...) }
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `cmd/ramplio/mock.go` | UPDATE | 抽出 `newMockHandler(profile) http.Handler`，mock-server 與 verify 共用（DRY） |
| `cmd/ramplio/verify.go` | CREATE | `newVerifyCmd()` + server bootstrap + evaluate + 輸出 |
| `cmd/ramplio/verify_test.go` | CREATE | evaluate 純函式單元測試（含失準/低報偵測）+ -short 跳過的端到端 |
| `cmd/ramplio/main.go` | UPDATE | 註冊 `newVerifyCmd()` |
| `cmd/ramplio/discover.go` | UPDATE | 報告信任背書改指向 `ramplio verify` |
| `README.md` | UPDATE | 量測公信力段 + CLI 參考新增 `verify` |
| `docs/accuracy.md` | UPDATE | 「自己動手驗證」改成一鍵 `ramplio verify` |

## NOT Building
- **不做任意 URL 的「驗證」**：verify 只對**自己注入延遲的內建 server** 驗證——對外部 URL 沒有 ground truth，無從驗起。`--url` 不在此指令。
- **不引入新的負載引擎或量測路徑**：完全復用 `engine.NewRamp` 與既有 collector。
- **不改 `groundtruth_test.go`**：它仍是開發期回歸防線；verify 是它的使用者面向孿生，不取代它。
- **不做 corrected/CO 模式的驗證**：本指令用 VU（closed-loop）固定/雙峰即可證明量測準確；rate 模式 CO 修正的驗證留在既有測試。
- **不加 JSON/HTML 輸出**：verify 是終端互動 + exit code，先不做多格式。

---

## Step-by-Step Tasks

### Task 1: 抽出共用 mock handler（mock.go）
- **ACTION**: 把 mock-server RunE 內聯的 `mux`/handler 建構抽成套件層函式 `func newMockHandler(profile latencyProfile) http.Handler`，RunE 改呼叫它。
- **IMPLEMENT**:
  - 新增 `newMockHandler(profile latencyProfile) http.Handler`：內部建立 `mux := http.NewServeMux()`、自帶 `var reqCount atomic.Int64`，註冊 `/`、`/healthz`、`/login`、`/profile`（即現有 104-149 的內容原樣搬入）。回傳 `mux`。
  - mock-server RunE 改為 `srv := &http.Server{Addr: ..., Handler: newMockHandler(profile)}`；reqCount 的「Served N requests」總數若要保留，可讓 `newMockHandler` 回傳 `(http.Handler, *atomic.Int64)`，或接受傳入的計數器指標。**最小改法**：簽名改 `newMockHandler(profile latencyProfile, reqCount *atomic.Int64) http.Handler`，由呼叫端持有計數器。
- **MIRROR**: LATENCY_PROFILE（不動 `latencyProfile`/`pickLatency`）。
- **IMPORTS**: 無新增（沿用 net/http、encoding/json、sync/atomic）。
- **GOTCHA**: 現有 `/profile` 對 reqCount 有重複 `Add`（135、143 行各一次）——搬移時**原樣保留**，不要順手「修」，以免動到既有行為與測試。
- **VALIDATE**: `go test ./cmd/ramplio/ -run TestMock`（既有 mock 測試）仍通過；`go build ./...` 無誤。

### Task 2: 驗證判語純函式（verify.go 的核心）
- **ACTION**: 在 `verify.go` 定義可單測的比對函式與輸出函式（與 server/engine 解耦）。
- **IMPLEMENT**:
  - 型別：
    ```go
    type pctCheck struct { Name string; Injected, Measured, Tol time.Duration; OK bool; Undercut bool }
    type verifyOutcome struct { Pass bool; Checks []pctCheck; Headline, Reason string }
    ```
  - `func evaluateFixed(injected, tol time.Duration, sum metrics.Summary) verifyOutcome`：對 p50/p90/p95/p99 各做 `OK = measured >= injected && measured <= injected+tol`；`Undercut = measured < injected`。Pass = 全部 OK。Headline/Reason 依結果：
    - 全過：「✓ 量測準確：所有百分位都落在注入值 +0~{tol} 內。」＋「量測值只會 ≥ 注入值（多了本機往返），這次沒有低於——代表沒有低報延遲的 bug。」
    - 有 Undercut：「✗ 量測失準：有百分位低於注入值。」＋「量到的延遲不該低於伺服器實際注入的延遲，這代表量測有 bug，請回報。」
    - 有超容差但無 undercut：「✗ 量測超出容差：可能是本機負載過高或容差過嚴。」＋「試著降低 --vus 或放寬 --tolerance 再跑一次。」
  - `func evaluateBimodal(fast, slow, tol time.Duration, sum metrics.Summary) verifyOutcome`：依 GROUND_TRUTH_ASSERTION 雙峰規則——`p50 ∈ [fast, fast+tol]`、`p99 ∈ [slow, slow+tol]`（p90/p95 在雙峰下不穩定，**不納入**判定，只在表格顯示）。
  - `func writeVerifyReport(w io.Writer, header verifyHeader, out verifyOutcome)`：印 UX 範例的標頭、量測結果表（縮排對齊，數值用 `fmt.Sprintf("%dms", d.Milliseconds())`）、Headline、Reason。
- **MIRROR**: GROUND_TRUTH_ASSERTION（容差與方向性完全照搬）。
- **IMPORTS**: `io`、`time`、`github.com/machiko/ramplio/internal/metrics`。
- **GOTCHA**: 方向性是重點——**measured < injected 是 bug（最嚴重）**，要與「超出容差」分開措辭；別合併成單一「不在範圍內」。
- **VALIDATE**: Task 5 的純函式測試通過。

### Task 3: verify 指令 — server bootstrap + 跑負載（verify.go）
- **ACTION**: 實作 `newVerifyCmd()`：解析 flag → 程式內起 mock server（ephemeral port）→ 跑短負載 → evaluate → 輸出 → exit code。
- **IMPLEMENT**:
  - Flags：`--latency`（預設 `"50ms"`）、`--latency-fast`、`--latency-slow`、`--slow-pct`、`--tolerance`（預設 `"20ms"`）、`--duration`（預設 `"3s"`）、`--vus`（預設 `10`）。
  - profile 解析沿用 mock.go RunE 的 ParseDuration 迴圈（可抽 `parseLatencyProfile(...)` 共用，或在 verify 內就地解析）。預設：若三個 latency flag 全空 → `profile.Fixed = 50ms`。
  - 起 server：
    ```go
    var reqCount atomic.Int64
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    srv := &http.Server{Handler: newMockHandler(profile, &reqCount)}
    go func() { _ = srv.Serve(ln) }()
    defer srv.Close()
    url := "http://" + ln.Addr().String() + "/"
    ```
  - 跑負載（MIRROR RUN_LOAD_IN_PROCESS）：`col := metrics.NewCollector(vus*2)`；`eng := engine.NewRamp(RampConfig{Stages:[{Duration, Target: vus}], Steps:[{Request:{Method:"GET", URL:url}}], Executor: protocols.NewHTTPExecutor(protocols.DefaultHTTPConfig())}, col)`；`sum := eng.Run(ctx)`（ctx 帶 SIGINT 取消，沿用 discover.go 56-60 模式）。
  - 樣本數防呆：若 `sum.Total < 30` → 印「樣本太少，無法判定（試著加長 --duration）」並回 nil（不誤判 fail）。
  - evaluate：fixed 模式呼叫 `evaluateFixed`，雙峰呼叫 `evaluateBimodal`。
  - `writeVerifyReport(os.Stdout, ...)`；若 `!out.Pass` → `return fmt.Errorf("量測自證未通過")`（cobra 會以 exit 1 結束，沿用既有 main.go 的錯誤路徑）。
- **MIRROR**: CLI_COMMAND_SHAPE、RUN_LOAD_IN_PROCESS。
- **IMPORTS**: `context`、`fmt`、`net`、`net/http`、`os`、`os/signal`、`sync/atomic`、`syscall`、`time`、cobra、engine、metrics、protocols、scenarios。
- **GOTCHA**:
  - `net.Listen("127.0.0.1:0")` 取 ephemeral port，避免和使用者既有服務撞 port（不要寫死 8080）。
  - 固定延遲下，10 VU closed-loop 的吞吐 ≈ vus/latency，50ms→~200 RPS×3s≈600 樣本，足夠；但若使用者把 `--latency` 設很大要提醒樣本數。
  - exit code 經由 `return error` → main.go `os.Exit(1)`；不要自己呼叫 `os.Exit`（會跳過 defer 關 server）。
- **VALIDATE**: `go run ./cmd/ramplio verify` → 印準確判語、`echo $?`=0；`go run ./cmd/ramplio verify --latency-fast 10ms --latency-slow 200ms --slow-pct 10` → 雙峰判語。

### Task 4: 註冊指令（main.go）
- **ACTION**: `rootCmd.AddCommand(newVerifyCmd())`。
- **IMPLEMENT**: 在 `init()` 既有 AddCommand 群組加入一行（緊接 `newMockServerCmd()` 之後，語意相鄰）。
- **MIRROR**: main.go:23-33。
- **IMPORTS**: 無。
- **VALIDATE**: `go run ./cmd/ramplio --help` 列出 `verify`。

### Task 5: 測試（verify_test.go）
- **ACTION**: 純函式單元測試為主，端到端以 -short 跳過。
- **IMPLEMENT**:
  - `buildSummary(p50,p90,p95,p99)` 小工具建 `metrics.Summary{Total:1000, P50:.., ...}`。
  - `TestEvaluateFixed_Pass`：injected=50ms、tol=20ms、量到 51/52/52/54 → Pass，Headline 含「量測準確」。
  - `TestEvaluateFixed_Undercut`：量到 p99=40ms（< injected 50ms）→ !Pass，Reason 含「低於」「bug」。
  - `TestEvaluateFixed_OverTolerance`：量到 p99=90ms（> 50+20）但無 undercut → !Pass，Reason 含「容差」「--vus」。
  - `TestEvaluateBimodal_Pass`：fast=10ms slow=200ms tol=30ms、p50=12ms、p99=210ms → Pass。
  - `TestEvaluateBimodal_TailMissed`：p99=50ms（沒落到慢帶）→ !Pass。
  - `TestWriteVerifyReport`：斷言輸出含「量測結果」「p50」「✓」或判語關鍵字、不含英文殘留。
  - `TestVerify_EndToEnd`（`if testing.Short(){t.Skip()}`）：實際呼叫 `newVerifyCmd().Execute()`（或抽出的 run 函式）對內建 server 驗證固定 50ms，斷言回 nil。
- **MIRROR**: TEST_STRUCTURE、GROUND_TRUTH_ASSERTION。
- **IMPORTS**: `bytes`、`strings`、`testing`、`time`、metrics。
- **GOTCHA**: evaluate 純函式測試**不可依賴真實計時**——直接餵合成 `Summary`。只有 EndToEnd 測試碰網路/計時，且 -short 跳過（比照 groundtruth_test）。
- **VALIDATE**: `go test ./cmd/ramplio/...` 全通過；`go test -short ./cmd/ramplio/...` 也通過（跳過 EndToEnd）。

### Task 6: discover 報告背書改指向 verify（discover.go）
- **ACTION**: 把 `writeDiscoverReport` 末尾兩列信任背書改成一鍵 verify。
- **IMPLEMENT**: 將
  `這個數字怎麼信？工具量測準確度可用` / `ramplio mock-server 注入已知延遲自行驗證。`
  改為
  `這個數字怎麼信？想驗證工具本身量得準不準，` / `跑一行 ramplio verify 即可一鍵自證。`
- **MIRROR**: 既有 `row()` + `displayWidth`。
- **GOTCHA**: 兩列長度仍須 ≤ `reportWidth-2`（44 顯示寬）——`跑一行 ramplio verify 即可一鍵自證。` 約 ASCII 16 + 中文，估算 ≤44；實作後跑 `TestWriteDiscoverReport_Alignment` 確認。
- **VALIDATE**: `go test ./cmd/ramplio/ -run TestWriteDiscoverReport` 通過（含對齊測試）。

### Task 7: 文件（README.md + docs/accuracy.md）
- **ACTION**: README 量測公信力段與 CLI 參考加入 `verify`；accuracy.md 的「自己動手驗證」改一鍵。
- **IMPLEMENT**:
  - README「量測公信力 → Ground-truth 自我驗證」段：在手動 mock-server 範例之前，先放一鍵：
    ```bash
    # 一行證明 Ramplio 量得準（內建注入已知延遲 → 施壓 → 比對 → 判語）
    ramplio verify
    ```
    並說明「失準會以 exit code 1 退出，可放進 CI 當工具可信度把關」。
  - README CLI 參考新增 `ramplio verify [flags]` 區塊（`--latency`/`--latency-fast`/`--latency-slow`/`--slow-pct`/`--tolerance`/`--duration`/`--vus`）。
  - `docs/accuracy.md` 46-68「自己動手驗證」：開頭改成「最簡單：跑 `ramplio verify`」，保留手動 mock-server 法作為「想自訂分佈時」的進階。
- **MIRROR**: README/文件既有繁中條列風格。
- **GOTCHA**: 不要刪 mock-server 手動法——verify 是預設，手動法仍是自訂分佈的進階路徑。
- **VALIDATE**: 人工檢視；範例指令可實際執行。

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| `TestEvaluateFixed_Pass` | inj=50ms tol=20ms, 量 51/52/52/54 | Pass，含「量測準確」 | 否 |
| `TestEvaluateFixed_Undercut` | p99=40ms < 50ms | !Pass，Reason 含「低於」「bug」 | 邊界（最嚴重） |
| `TestEvaluateFixed_OverTolerance` | p99=90ms，無 undercut | !Pass，Reason 含「容差」 | 邊界 |
| `TestEvaluateBimodal_Pass` | fast10/slow200 tol30，p50=12 p99=210 | Pass | 否 |
| `TestEvaluateBimodal_TailMissed` | p99=50ms（未入慢帶） | !Pass | 邊界（尾端） |
| `TestWriteVerifyReport` | 一個 verifyOutcome | 含「量測結果」「p50」「✓」；無英文殘留 | 否 |
| `TestVerify_EndToEnd`（-short skip） | 實跑固定 50ms | Execute 回 nil | 計時/網路 |

### Edge Cases Checklist
- [x] 量測值低於注入值（量測 bug）→ 明確標為失準
- [x] 超出容差但未低報 → 區分措辭（建議降載/放寬容差）
- [x] 雙峰尾端沒被分離出來 → fail
- [x] 樣本太少 → 不誤判，提示加長 duration
- [ ] 不適用：外部 URL（本指令不接受 `--url`）
- [ ] 不適用：port 衝突（用 ephemeral 127.0.0.1:0）

---

## Validation Commands

### Static Analysis
```bash
gofmt -l cmd/ramplio/
go vet ./cmd/ramplio/...
```
EXPECT: 我新增/修改的檔案無輸出；vet 乾淨。

### Unit Tests
```bash
go test ./cmd/ramplio/... -v
go test -short ./cmd/ramplio/...
```
EXPECT: 全通過；-short 下 EndToEnd 被跳過仍綠。

### Full Test Suite
```bash
go test -race ./...
```
EXPECT: 無回歸。

### Manual Validation
- [ ] `go run ./cmd/ramplio verify` → 印準確判語、`echo $?`=0
- [ ] `go run ./cmd/ramplio verify --latency-fast 10ms --latency-slow 200ms --slow-pct 10` → 雙峰判語通過
- [ ] `go run ./cmd/ramplio verify --tolerance 1ms` → 多半失準（容差過嚴），Reason 提示放寬、exit 1
- [ ] `go run ./cmd/ramplio --help` 列出 `verify`
- [ ] `go run ./cmd/ramplio discover --url http://localhost:18080 …`（先開 mock-server）→ 報告背書改成指向 `ramplio verify`

---

## Acceptance Criteria
- [ ] `ramplio verify` 端到端可跑，準確時 exit 0、失準時 exit 1
- [ ] 固定與雙峰兩種注入分佈都能驗
- [ ] 「量測值低於注入值」被明確標為 bug（與超容差區分）
- [ ] mock handler 由 verify 與 mock-server 共用（無重複實作）
- [ ] discover 報告背書指向 `ramplio verify`
- [ ] README + accuracy.md 文件更新
- [ ] `go test -race ./...` 無回歸；`-short` 下 EndToEnd 跳過

## Completion Checklist
- [ ] 容差與方向性與 `groundtruth_test.go` 一致（measured ≥ injected）
- [ ] evaluate 為純函式、不依賴計時，可單測
- [ ] 不寫死 port（ephemeral）
- [ ] exit code 經由 `return error`，不自呼 `os.Exit`（保留 defer 關 server）
- [ ] 輸出繁中、口吻對齊現有白話判語
- [ ] commit 遵守 isocialwork-conventions

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| CI 機器負載高導致 verify 偶發失準（誤報） | 中 | 中 | 預設容差 20ms 寬鬆；樣本不足時不判 fail；文件說明可調 `--tolerance`/`--vus` |
| 抽 mock handler 動到既有 mock-server 行為 | 低 | 中 | 原樣搬移（含 /profile 的重複 Add），既有 mock 測試把關 |
| 雙峰 p90/p95 在低樣本下不穩定被誤判 | 中 | 低 | 雙峰只判 p50（快帶）與 p99（慢帶），p90/p95 僅顯示不判定（同 groundtruth_test） |
| 使用者把 `--latency` 設超大導致樣本太少 | 低 | 低 | 樣本 < 30 時提示加長 duration，不判 fail |

## Notes
- 對應評估建議的 **P1（降低信任門檻）**。它把「容量回答機」定位的信任背書從文字變成可重現指令，與 P0（定位收斂，已完成於 `feat/positioning-capacity-answer-engine`）直接接續：discover 報告的背書這次真的有指令可點。
- 刻意**不含 P2**（assertion retry / rate 模式記憶體硬傷）——那是內行人硬傷，與信任門檻是不同槓桿，另案。
- verify 的價值不只給人看：放進 CI 就是「每次 build 都證明這版 Ramplio 沒把量測改壞」的回歸閘門，等於把 `groundtruth_test.go` 的保證延伸到使用者的 pipeline。

# CARD：智慧單一入口 `ramplio <網址或 curl>`

- 分支：`feat/smart-entry`（worktree：`/Users/reyes/Workspace/ramplio-worktrees/smart-entry`）
- 基準：`main` @ 9c10397
- 規模：一般
- 狀態：第 1 段規格已確認（2026-11-25），第 2 段實作中

## 規格

### Context

Reyes 說「連我自己都不太會用」。與 agy（Gemini，plan 模式唯讀）兩輪討論＋Claude 抽查程式後的共識：根因不是少功能，而是**入口太多、每個入口只做一半**：

- 首推的 `discover` 只能打裸 GET（`cmd/ramplio/discover.go:90-95` 只有 5 個旗標；`internal/discover/prober.go:132` 的 `probe()` 只送 Method+URL），真實 API 帶 Authorization 或 POST 就不能用。
- `run --url` 沒有即時畫面（TUI 只在 scenario 路徑 `run.go:499,608`）。
- 兩套精靈（CLI `init` vs dashboard guided）、三種存檔旗標（`--output`／`--report`／`--save-baseline`，compare 只吃 baseline 格式）。

三張卡路線（使用者選定先做單一入口）：
1. **本卡**：`ramplio <網址 | 'curl …' | ->` 自動判斷 → 安全分流 → 走容量探測；discover 補 header／body。
2. 自動留存 `.ramplio/last.json`＋自動與上次比對＋`report`／`compare` 零參數；`run --url/--rps` 補 TUI。
3. discover 結果一鍵固化成帶 thresholds 的 scenario.yaml；收斂雙精靈、移除 init 裡不存在的 `generate_sessions.sh`。

### 規模分級

一般（改到行為）。分支 `feat/smart-entry`（不在 main 上 commit）。

### 目標

- `ramplio https://api.x.com/health` 直接開始容量探測（等同 `discover --url`）。
- `ramplio 'curl -X GET https://… -H "Authorization: Bearer …"'` 與 `pbpaste | ramplio -` 都能解析 DevTools「Copy as cURL」，帶著 header／body 探測。
- 寫入型方法（POST/PUT/PATCH/DELETE）**不自動探測**：只送一發預檢、印出結果，並給出明確的進階指令（`ramplio discover --curl … --allow-write`）。
- `discover` 新增 `-H`、`--method`、`--body`、`--curl`、`--allow-write`。
- 歡迎畫面 ① 改為 `ramplio <網址或貼上 curl>`。

### 非目標

- 不做 `.yaml`／`.har` 自動開跑（本卡只印出正確指令，例如「這是情境檔，請用 `ramplio run --scenario x.yaml`」）。
- 不做自動留存與 TUI（第二張卡）。
- 不支援 curl 的全部旗標：只支援 DevTools 產出的子集，遇到不認識的旗標要警告，不能默默吃掉。
- 不新增第三方相依套件。

### 驗收情境（給定／當／則／對應測試）

1. 給定一個本機 httptest server，當執行 `ramplio <URL>`，則走 discover 流程並印出容量報告。→ `smartentry_test.go: TestRootURLDispatchesDiscover`
2. 給定 DevTools 格式的 curl（含 `\` 換行、單雙引號、`-H` 多個、`--data-raw`、`--compressed`），當解析，則 Method/URL/Headers/Body 正確；遇到不認識的旗標產生警告。→ `internal/curlparse/parse_test.go`（表格驅動）
3. 給定 `curl -X POST …`，當執行 `ramplio '<curl>'`，則 server 只收到 1 個請求，輸出含 `--allow-write` 的建議指令，exit 0。→ `TestRootWriteMethodOnlyPreflights`（負向：斷言請求數 == 1）
4. 給定 `discover --curl '<含 Authorization 的 curl>'`，當探測，則 server 收到的每個請求都帶該 header 與 body。→ `internal/discover/prober_test.go: TestProbeSendsHeadersAndBody`
5. 給定打錯字 `ramplio dicover`（不是 URL、curl、檔案），則不開跑，回錯誤並附「你是不是要 discover？」與歡迎畫面指引，exit ≠ 0。→ `TestRootUnknownArgSuggests`
6. 給定 `ramplio x.yaml`／`x.har`，則印出對應的 `run --scenario`／`import` 指令，不開跑。→ `TestRootFileArgPrintsHint`
7. 隱式 POST：給定 `curl 'http://…' --data-raw '{"a":1}'`（沒有 `-X`，Chrome 的實際格式），則推導為 POST，只發 1 發預檢（**帶 header 與 body**），並要求加 `--allow-write`。→ `TestRootImplicitPostOnlyPreflights`
8. discover 本身也守門：給定 `ramplio discover --url … --method POST`（沒加 `--allow-write`），則跟單一入口一樣只發 1 發就擋下，不開後門。→ `TestDiscoverWriteMethodRequiresAllowWrite`
9. 不外洩憑證：給定帶 `Authorization`／`Cookie` 的 curl，則終端輸出與建議指令都不出現 token 原文（建議指令改用 `pbpaste | ramplio discover --curl - --allow-write`，header 只顯示名稱並遮罩值）。→ `TestSmartEntryMasksCredentials`

### 實作計畫（檔案）

- 新增 `internal/curlparse/parse.go`＋`parse_test.go`：shell-like tokenizer（單引號、雙引號、`\` 換行、Chrome 的 ANSI-C `$'…'`，要處理 `\'`、`\n`、`\"`）＋旗標對應：`-X/--request`、`-H/--header`、`-d/--data/--data-raw/--data-binary`（有 body 且沒寫 -X 時預設 POST）、`--url`、`-b/--cookie`→Cookie header、`--compressed/-s/-k/-L/-i` 忽略；其餘旗標放進 `Warnings`。回傳不可變的 `Request{Method, URL, Headers, Body, Warnings}`。
- 修改 `internal/discover/prober.go`：`Config` 加 `Headers map[string]string`、`Body []byte`，在 `probe()` 填進 `protocols.Request`（SetupSteps 跟 Steps 共用同一個 req）。
- 修改 `cmd/ramplio/discover.go`：新增旗標；移除 `MarkFlagRequired("url")`（`discover.go:91`，否則 `--curl` 進不了 RunE），改在 RunE 驗證 `--url`／`--curl` 二擇一；`--curl` 跟 `-H/--method/--body` 同時給時，以明確給的旗標覆蓋 curl 解析出來的值；`--curl -` 從 stdin 讀；非冪等方法沒加 `--allow-write` 就只發一發預檢並擋下。把建 cfg 和印報告抽成 `runDiscover(ctx, w, cfg, …)`，讓 root 共用。
- 修改 `cmd/ramplio/preflight.go`：`runPreflight` 改成接收 `protocols.Request`（目前 `preflight.go:55-63` 寫死只送 Method+URL，寫入型預檢會因為少了 Authorization／Content-Type 拿到 401/415）；既有呼叫端（`run.go`）跟著調整。
- 新增 `cmd/ramplio/smartentry.go`＋`smartentry_test.go`：`classifyTarget(arg) → url | curl | stdin | yamlFile | harFile | unknown`；分派邏輯；寫入方法改用既有的 `runPreflight`（`cmd/ramplio/preflight.go:55`）只發一發。
- 修改 `cmd/ramplio/main.go`：`rootCmd.Args = cobra.ArbitraryArgs`；`Run` 沒有參數時維持 `printWelcome`，有參數時呼叫 smartentry。未知參數要用 cobra 的 `SuggestionsFor` 給建議（避免打錯字的子指令被當成目標）。
- 修改 `cmd/ramplio/guidance.go`：更新歡迎畫面 ①。
- 修改 `README.md`、`docs/getting-started.md`：快速開始改成單一入口；新增 `changelog.d/` 片段（如果專案有這個慣例）。

可重用：`runPreflight`／`preflightTarget`（preflight.go）、`printDiscoverReport`／`printDiscoverProbe`（discover.go）、`baseline.FromDiscover`、`discover.ProbeSequence`。

### 完成檢查

`go test ./internal/curlparse/... ./internal/discover/... ./cmd/ramplio/... -run 'Curl|Probe|Root'`（實作前紅、實作後綠），接著 `go test -race ./...`、`golangci-lint run`、`go build ./...`。

### 驗證（端到端）

1. `go run ./cmd/ramplio mock-server` 起本機目標。
2. `go run ./cmd/ramplio http://localhost:<port>/` → 看到探測進度與容量報告。
3. 在瀏覽器 DevTools 複製一則 GET 的 curl → `pbpaste | go run ./cmd/ramplio -` → 帶 header 探測。
4. 複製一則 POST 的 curl → 確認只有一發預檢加上建議指令。
5. `go run ./cmd/ramplio dicover` → 有建議、不開跑。


## Decisions

- 2026-11-25：第一張卡先做單一入口，不做 agy 建議的「自動留存」（使用者決定：要先有亮眼、本人立刻有感的入口）。
- 2026-11-25：寫入型方法預設只發一發預檢，要壓測得明確加 `--allow-write`（agy 提出的資料安全風險）。
- 2026-11-25：curl 解析自己寫，不新增 shlex 相依套件。

## History

- 2026-11-25：與 agy 討論三輪後定出規格；建立分支與 worktree。
- 2026-11-25：使用者確認規格；交給 herdr pane `impl`（wA:p4）實作。

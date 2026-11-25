# CARD：智慧單一入口 `ramplio <網址或 curl>`

- 分支：`feat/smart-entry`（worktree：`/Users/reyes/Workspace/ramplio-worktrees/smart-entry`）
- 基準：`main` @ 9c10397
- 規模：一般
- 狀態：第 4 段複審通過（無 CRITICAL／HIGH／MEDIUM），第 6 段 PR 等 CI（2026-11-25）

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

`go test ./internal/curlparse/... ./internal/discover/... ./cmd/ramplio/... -run 'Curl|Probe|Root|SmartEntry'`（實作前紅、實作後綠；第 5 段第 1 輪起加上 `SmartEntry`，讓情境 9 也在篩選內），接著 `go test -race ./...`、`golangci-lint run`、`go build ./...`。

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
- 2026-11-25（實作）：計畫外但必要的測試檔改動——`cmd/ramplio/preflight_test.go`（跟著 `runPreflight` 新簽名調整呼叫，並新增 `TestRunPreflight_SendsHeadersAndBody`）、`internal/discover/prober_test.go`（情境 4 的測試放這）、`cmd/ramplio/discover_test.go`（情境 8）、`cmd/ramplio/guidance_test.go`（歡迎畫面 ① 文字改了，斷言跟著改）。只動測試、不放寬既有斷言。
- 2026-11-25（實作）：專案沒有 `changelog.d/`，既有慣例是 `CHANGELOG.md` 的 `[Unreleased]` 區塊，所以改在那裡的「新增」加一條（只動 Unreleased，不碰歷史版本）。
- 2026-11-25（實作）：`discover` 被 `--allow-write` 擋下時回錯誤（exit ≠ 0）；單一入口照情境 3 回 exit 0。理由：明確下 `discover` 的人（或 CI）要的是探測結果，沒跑成就該失敗；單一入口的擋下是引導。兩者共用 `runDiscover`，都只發一發預檢。
- 2026-11-25（實作）：`main.go` 的 root 抽成 `newRootCmd()`（保留 `rootCmd` 變數），測試才能拿全新的 root 實跑 cobra 分派；cobra v1.8.0 的 `SuggestionsFor` 不會自己套預設距離，root 明確設 `SuggestionsMinimumDistance: 2`。
- 2026-11-25（實作）：`smartentry_test.go` 兩處刻意的錯字 `dicover`（情境 5）被 golangci-lint 的 misspell 抓到，用逐行 `//nolint:misspell` 加理由處理；沒有改 `.golangci.yml`、沒有放寬全域規則。本機沒裝 golangci-lint，用 `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`（v2.15.0，與 CI 的 latest 對齊）跑，不動 go.mod。
- 2026-11-25（第 5 段第 1 輪）：`run.go` 的那 1 行是 `runPreflight` 改吃 `protocols.Request` 的必要連動（實作計畫寫了「既有呼叫端跟著調整」），只把參數包成 Request，`run` 的行為不變；唯一可見差異是預檢輸出的網址現在也會遮罩（M1 改在 `runPreflight` 內，`run` 一起受惠）。
- 2026-11-25（第 5 段第 1 輪）：Uncertain 採 fail-closed。curl 只要有不認識的旗標，或有無法重現的寫法（`-F`、`-T`、`-d @檔案`、`--data-urlencode @檔案`、`-x/--proxy`、`--resolve`、`-K` 等），就設 `Uncertain`。discover 與單一入口在 Uncertain 且沒加 `--allow-write` 時，跟寫入型一樣只送 1 發預檢就擋下，並說明是「無法完整解讀」。只影響 curl 本身輸出的帶值旗標（`-o`、`-m`、`-w`、`--cacert` 等）只警告、不設 Uncertain。
- 2026-11-25（第 5 段第 1 輪）：M2 的處置：被擋下的那 1 發照舊送真實方法（不改成 OPTIONS/HEAD，那要另外回規格確認），擋下後明說「已送出 1 發真實 <METHOD> 預檢（伺服器已實際處理這一發，若是寫入就已經寫入一次）」。
- 2026-11-25（第 5 段第 1 輪）：`discover` 被擋時回 exit ≠ 0、單一入口回 exit 0。**使用者已確認（2026-11-25）**。
- 2026-11-25（第 5 段第 1 輪）：改了一組既有測試的輸入：`parse_test.go` 的「不認識的旗標產生警告」原本用結尾不帶值的 `--max-time` 當「不認識的旗標」。H2 要求 `--max-time` 改成已知的帶值旗標，所以改成合法寫法 `--max-time 5`。斷言沒有放寬：仍要求警告提到兩個旗標，並新增要求 `Uncertain == true`（因為 `--http2`）。
- 2026-11-25（第 5 段第 1 輪）：H2 延伸：`discover -H` 格式錯誤的錯誤訊息原本用 `%q` 印出整段值，跟審查指出的外洩是同一類，改成只說「第 N 個 -H」。看不懂的參數回顯只顯示第一個詞並遮罩網址秘密。
- 2026-11-25（第 5 段第 1 輪）：M4：大小寫不敏感的 header 覆蓋邏輯從 `discover.go` 移到 `curlparse.MergeHeaders`，解析器與 discover 共用同一份。
- 2026-11-25（第 5 段第 2 輪）：M2 查證結論：`compare` 不是靠 URL 找對應資料，只在兩份 baseline 的 `Scenario`（discover 模式＝目標 URL）不同時印警告（`internal/baseline/compare.go:128`）；dashboard 只拿它來顯示。遮罩是確定性的，同一個目標前後兩次遮罩結果相同，比對不受影響。代價是：同一個網址只差在 token 值的兩份 baseline 會被視為同一目標，這本來就是同一目標，可以接受。
- 2026-11-25（第 5 段第 2 輪）：M1 的遮罩改為不分大小寫的子字串比對，刻意寧可多遮：`code` 會連 `country_code`、`zipcode` 一起遮。fragment 只要存在就整段換成 `#***`。原本 `TestSmartEntryMaskURL` 預期 `#frag` 原樣保留，照審查要求改成預期 `#***`（變嚴格，不是放寬）。
- 2026-11-25（第 5 段第 2 輪）：LOW 3 把 `--http2` 列為無害，跟第 1 輪「不認識的旗標」測試案例拿 `--http2` 當例子衝突。改用真的不認識的 `--frobnicate`，斷言不變（仍要求警告提到兩個旗標、`Uncertain == true`）。
- 2026-11-25（第 5 段第 2 輪）：LOW 2 比審查要求多分一類：逾時或連線中斷時，請求可能已經送到伺服器，說「沒有寫入」可能是假的，所以改說「已送出但沒有收到回應，無法確定伺服器是否已處理（可能已寫入一次）」。只有其他沒送出的情況（例如網址無法解析）才說「預檢沒有送達目標，沒有寫入」。實作上把 `runPreflight` 拆成會回傳結果的 `preflightOnce`，`runPreflight` 的簽名不變，`run.go` 不用再動。
- 2026-11-25（第 5 段第 2 輪）：LOW 1 兩層都擋：curl 裡出現的方法覆寫 header 由解析器設 Uncertain；`discover --url … -H "X-HTTP-Method-Override: …"` 明確給的，由 `resolveDiscoverRequest` 檢查。判斷邏輯共用 `curlparse.MethodOverrideHeaderNames`。
- 2026-11-25（第 5 段第 2 輪）：LOW 5 的測試第一次跑就是綠的：第 1 輪已實作「已知帶值旗標缺值就回錯」，這次只是補回歸保護。
- 2026-11-25（實作）：curl 的 tokenizer 跟解析放同一個 `parse.go`，不另開檔，守住「只改計畫列的檔案」。

- 2026-11-25（使用者確認）：被擋下的寫入型請求維持真的送出 1 發預檢（畫面明說已送出），不改成完全不送。
- 2026-11-25（第 4 段第 3 次審查）：LOW 不修、寫進 PR 說明：(1) 遮罩關鍵字沒涵蓋 `pwd`、`bearer`、`otp`、`pin`；(2) 沒有 `=` 的 query 片段（`?<secret>`）不遮，現實罕見。

## History

- 2026-11-25：與 agy 討論三輪後定出規格；建立分支與 worktree。
- 2026-11-25：使用者確認規格；交給 herdr pane `impl`（wA:p4）實作。
- 2026-11-25：第 2 段實作完成（TDD，每塊先看到預期原因的紅燈再實作）。commit：92d0f69 curl 解析器、b95ef92 prober header/body、d83f8ee 預檢送完整請求、1ee3b2f discover 旗標與寫入守門、edcd803 歡迎畫面、c1fe0e2 單一入口分派、fc2398c 文件與 CHANGELOG。
- 2026-11-25：第 3 段測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/curlparse/... ./internal/discover/... ./cmd/ramplio/... -run 'Curl|Probe|Root' -count=1`：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	0.677s
    ok  	github.com/machiko/ramplio/v3/internal/discover	2.670s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	7.443s
    ```
  - `go test -race -count=1 ./...`：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	50.169s
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	3.995s
    ok  	github.com/machiko/ramplio/v3/internal/discover	11.029s
    ok  	github.com/machiko/ramplio/v3/internal/engine	103.662s
    ok  	github.com/machiko/ramplio/v3/internal/scenarios	2.370s
    （其餘 10 個套件同為 ok，exit 0）
    ```
  - `golangci-lint run`（v2.15.0，經 go run）：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - 注意：完成檢查的 `-run 'Curl|Probe|Root'` 篩不到情境 9 的 `TestSmartEntryMasksCredentials`（名稱沿用規格），它在 `go test -race ./...` 整套裡有跑且通過。
  - 觀察到的既有問題（計畫外，未修）：任何指令回錯誤時訊息印兩次（cobra 的 `Error:` 加上 `main()` 自己再印一次）；`runPreflight` 不可達時的「可加 --no-preflight」提示在 discover／單一入口路徑不適用（這兩處沒有該旗標）。
- 2026-11-25：**另開卡（本卡不修）**：(1) 任何指令回錯誤時訊息印兩次（cobra 的 `Error:` 加上 `main()` 自己再印一次）；(2) `runPreflight` 連不到目標時的「可加 --no-preflight」提示在 discover／單一入口路徑不適用。
- 2026-11-25：第 5 段第 1 輪（審查 Block，2 HIGH）修正完成。commit：829c6e9 解析器（H1 旗標補齊＋叢集＋Uncertain、H2 憑證／帶值旗標＋警告不帶值、M4 MergeHeaders、LOW 大小寫一致）、5a4d4e9 cmd 層（H1 fail-closed 守門、M1 網址遮罩、M2 提示、M3 stdin 上限）、本筆（README curl 說明、CARD）。
- 2026-11-25：第 5 段第 1 輪測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/curlparse/... ./internal/discover/... ./cmd/ramplio/... -run 'Curl|Probe|Root|SmartEntry' -count=1`：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	0.348s
    ok  	github.com/machiko/ramplio/v3/internal/discover	2.344s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	11.633s
    ```
  - `go test -race -count=1 ./...`：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	52.876s
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	3.770s
    ok  	github.com/machiko/ramplio/v3/internal/discover	10.080s
    ok  	github.com/machiko/ramplio/v3/internal/engine	101.198s
    ok  	github.com/machiko/ramplio/v3/internal/scenarios	1.919s
    （其餘 10 個套件同為 ok，exit 0）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
- 2026-11-25：**另開卡（第 2 輪新增）**：預檢沒有任何回應（例如網址無法解析）時，既有的 `printPreflightBreakdown` 還是印「✓ 預檢通過（無回應）」，跟緊接著的「預檢沒有送達目標，沒有寫入」矛盾。這段 `run --url` 也在用，是改動前就有的行為；這是最後一輪，修正清單沒列，本卡不修。
- 2026-11-25：第 5 段第 2 輪（複審可進 PR，收 MEDIUM 與便宜的 LOW）修正完成。commit：8907bf3 解析器（LOW 1 方法覆寫 header、LOW 3 無害布林旗標、LOW 5 回歸測試）、f157caf cmd 層（M1 遮罩擴大＋fragment、M2 baseline 遮罩、LOW 1 discover -H、LOW 2 預檢送達說明、LOW 4 -H 去重）、本筆（README、CARD）。
- 2026-11-25：第 5 段第 2 輪測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/curlparse/... ./internal/discover/... ./cmd/ramplio/... -run 'Curl|Probe|Root|SmartEntry' -count=1`：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	0.265s
    ok  	github.com/machiko/ramplio/v3/internal/discover	2.282s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	13.160s
    ```
  - `go test -race -count=1 ./...`：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	56.383s
    ok  	github.com/machiko/ramplio/v3/internal/curlparse	4.639s
    ok  	github.com/machiko/ramplio/v3/internal/discover	13.670s
    ok  	github.com/machiko/ramplio/v3/internal/engine	105.871s
    ok  	github.com/machiko/ramplio/v3/internal/scenarios	3.038s
    （其餘 10 個套件同為 ok，exit 0）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
- 2026-11-25（第 4 段）：第 3 次獨立審查（go-reviewer，只拿規格＋diff）結論「可以進 PR」，無 CRITICAL／HIGH／MEDIUM，LOW 2 條記入 Decisions；Claude 主對話重跑 `go test -race -count=1 ./...` 15 套件 ok、golangci-lint `0 issues.`。進第 6 段。

# CARD：run --url／--rps 即時畫面與一致的中斷收尾（卡 2b）

- 分支：`feat/run-tui`（worktree：`/Users/reyes/Workspace/ramplio-worktrees/run-tui`）
- 基準：`main` @ e2a2eb0
- 規模：一般
- 狀態：第 5 段第 1 輪完成，待複審（2026-11-26）

## 規格

### Context

易用性三張卡的 2b（卡 1 單一入口、卡 2a 自動留存都已合併）。根因同前：**入口太多、每個入口只做一半**。讀程式後確認的現況：

- `run --scenario`（`runScenario`，`cmd/ramplio/run.go:449`）與 `run --worker`（`runDistributed`，`run.go:588`）是「引擎在背景 goroutine 跑＋`provider` 提供即時快照＋TTY 開 `reporter.RunTUI`、非 TTY 或 `--no-tui` 改 `runHeadlessProgress`」的結構。
- `run --url`（`runURL`，`run.go:666`）與 `run --rps`（`runRPS`，`run.go:695`）在前景呼叫 `eng.Run(context.Background())`：
  - 跑的時候只有一行「Running …」，之後什麼都沒有，新手以為程式卡住。
  - **沒有任何訊號處理**：Ctrl-C／SIGTERM 讓行程直接結束，摘要、`--output`、`--report`、2a 的自動紀錄全部不會產生。
- 中斷收尾各路徑不一致：
  - scenario＋TTY：bubbletea 把 Ctrl-C 當按鍵收（`tui.go:69`）→ cancel → 照常印摘要、存紀錄、依 thresholds 決定 exit code；**SIGTERM 沒人攔，行程直接死**。
  - scenario＋非 TTY：`runHeadlessProgress` 攔 SIGINT／SIGTERM → cancel → 同上。
  - url／rps：見上，直接死。
  - 2a Decisions 已記：「run 情境模式按 Ctrl-C 中斷時，現有流程分不出是不是跑完了，照樣會存……列為已知限制」；discover 被中斷則不存。
- `--no-tui` 旗標已存在（`run.go:319`），說明文字已寫「auto-enabled when output is not a terminal」，目前只有 scenario 與 distributed 讀它。
- TUI 用 bubbletea 預設的 inline 模式（`tui.go:146` 沒有 `WithAltScreen`），結束時最後一個畫面留在終端，之後才印摘要；所以「摘要被畫面蓋掉」不會發生，但要用測試守住順序。

### 規模分級

一般（改到行為：新增即時畫面、改中斷收尾與 exit code；不碰 CI、認證授權、migration、機密設定）。預估產品程式 diff 約 150–250 行（不含測試），一天內可合併，**不拆卡**。

### 目標

- 抽出共用的「看著一次執行跑完」函式 `watchRun`：決定開 TUI 或非 TTY 模式、攔 SIGINT／SIGTERM、回報這次是否被中斷。`runURL`、`runRPS`、`runScenario`、`runDistributed` 都用它。
- 「是否開 TUI」的判斷與 TUI 本身、訊號來源都可注入（`liveEnv`），測試不依賴真終端、不對測試行程送真訊號。
- TTY 下 `run --url`、`run --rps` 開即時畫面（重用 `reporter.RunTUI` 與 `rampProvider`）。
- 非 TTY（CI、管線、重新導向）或 `--no-tui`：`run --url`／`--rps` 不開 TUI，**stdout 跟現在一致**（不新增進度行，見 Decisions D1）。
- 中斷（Ctrl-C 或 SIGTERM，TTY 與非 TTY 都一樣）：引擎停止 → 印摘要 → `--output`／`--report`／`--save-baseline`／`--sink` 照常產生 → **不存自動紀錄**（跟 2a 的 discover 一致）→ 結束碼見 Decisions D2。第二次 Ctrl-C 仍可強制結束（沿用 `signal.Stop` 的做法）。
- TUI 結束後，預檢輸出、「Running …」行、摘要、自動比較段落各出現一次、順序固定：預檢 → Running → （TUI 最後畫面）→ 摘要 → 紀錄／比較。

### 非目標

- 不改 TUI 的版面（`reporter.tuiModel.View`），除了單一請求模式不顯示只有一列的 step 表（見實作計畫）。
- 不改 `--dashboard` 路徑（它有自己的訊號處理與 `shutdownDashboard`）。
- 分散式 `--worker`：只換成共用的 `watchRun`（等於多了 TUI 模式下的 SIGTERM 收尾），**不改它的 exit code 與輸出**；它本來就不存自動紀錄。
- 不改單一入口 `ramplio <網址>`（走 discover，已有 `signal.NotifyContext`）。
- 不做 alt screen、不加按鍵操作（暫停、調 VU）。
- 不新增旗標、不新增相依套件。

### 驗收情境（給定／當／則／對應測試）

測試都在 `cmd/ramplio/`，用 `liveEnv` 注入假的 TTY 判斷、假的 TUI（記錄有沒有被呼叫、收到的 provider 能取快照）與假的訊號 channel。新測試檔 `cmd/ramplio/runlive_test.go`。

開不開畫面

1. 給定 `liveEnv` 回報是 TTY、本機 httptest server，當執行 `run --url <server> -d 1s`，則假 TUI 被呼叫 1 次，它拿到的 provider 快照 `Total` 會增加，`run` 正常結束、印摘要。→ `TestRunLiveURLStartsTUIOnTTY`
2. 同上改成 `run --rps 5 -d 1s`，則假 TUI 被呼叫 1 次，快照帶 `StageTotal == 3`（ramp／hold／ramp-down）。→ `TestRunLiveRPSStartsTUIOnTTY`
3. 負向：給定 `liveEnv` 回報不是 TTY，當 `run --url`／`run --rps`，則假 TUI 一次都沒被呼叫，stdout 除了摘要與紀錄之外沒有 `[…] VUs=` 進度行（D1 照推薦時）。→ `TestRunLiveNonTTYSkipsTUI`
4. 負向：給定是 TTY 但帶 `--no-tui`，則假 TUI 沒被呼叫。→ `TestRunLiveNoTUIFlag`
5. 判斷函式本身：`shouldUseTUI(noTUI, isTTY)` 只有 `(false, true)` 時為 true。→ `TestShouldUseTUI`（表格測試）

中斷收尾

6. 給定非 TTY、`run --url -d 30s`，當跑到一半從注入的訊號 channel 送 `os.Interrupt`，則在 5 秒內結束（不是跑滿 30 秒）、stdout 有摘要、stderr 有「已中斷」說明、紀錄目錄沒有新增檔案、RunE 回傳中斷錯誤（D2 照推薦時）。→ `TestRunLiveInterruptURL`
7. 同 6，送 `syscall.SIGTERM`，結果相同。→ `TestRunLiveSigtermURL`
8. 同 6 改 `run --rps`，結果相同。→ `TestRunLiveInterruptRPS`
9. 給定 TTY 模式，當假 TUI 模擬使用者按 Ctrl-C（呼叫它收到的 cancel 後回傳），則跟 6 一樣：摘要照印、不存紀錄、回中斷錯誤。→ `TestRunLiveTUICtrlC`
10. 給定 TTY 模式，當 TUI 開著時從訊號 channel 送 SIGTERM，則引擎停止、TUI 收到 done 結束、收尾同 6（修正 TUI 路徑沒攔 SIGTERM）。→ `TestRunLiveTUISigterm`
11. 給定中斷，當帶 `--output x.json --report x.html`，則兩個檔都有產生（中斷不等於沒結果）。→ `TestRunLiveInterruptKeepsOutputs`
12. 給定 `run --scenario`（非 TTY）被中斷，則不存自動紀錄、回中斷錯誤（取代 2a 的已知限制；D3 照推薦時）。→ `TestRunLiveInterruptScenario`

輸出順序

13. 給定 TTY 模式、已有同目標的紀錄，當 `run --url` 正常跑完，則 stdout 依序出現「Running」→ 假 TUI 寫入的標記 → 摘要 → 「跟上一次」段落，每段各 1 次。→ `TestRunLiveOutputOrder`

回歸（負向）

14. `TestRunScenarioHeadless_CompletesWithoutTTY` 不改、照樣綠（非 TTY 場景跑滿）。
15. 2a 的 `History*` 測試、`compare_test.go`、`baselinesave_test.go`、`smartentry_test.go` 不改、照樣綠。
16. 給定分散式（假 worker 用既有測試的方式），非 TTY 下跑完照樣印摘要、exit 0；只確認換成 `watchRun` 後行為不變。→ 先找既有分散式 cmd 測試，有就沿用；沒有就新增 `TestRunDistributedUsesWatchRun`（只驗非 TTY 跑完、不開 TUI）。
17. `--dashboard` 路徑不呼叫 `watchRun`（程式碼層面不動 `runDashboard`），由審查對照 diff 確認，不另寫測試。

### 實作計畫（檔案）

- 新增 `cmd/ramplio/runlive.go`（約 100 行）：
  - `type liveEnv struct { isTTY func() bool; runTUI func(reporter.LiveProvider, context.CancelFunc, <-chan struct{}) error; signals func() (<-chan os.Signal, func()) }`；`defaultLiveEnv()` 接 `isTerminal`、`reporter.RunTUI`、`signal.Notify(os.Interrupt, syscall.SIGTERM)`／`signal.Stop`。
  - package 變數 `newLiveEnv = defaultLiveEnv`，測試替換（`cmd/ramplio` 測試沒有 `t.Parallel`，安全）。
  - `shouldUseTUI(noTUI, isTTY bool) bool`。
  - `watchRun(env liveEnv, opts watchOpts, provider, cancel, done) (interrupted bool)`：
    - 一律先註冊訊號；收到訊號 → 標記中斷、`stop()`（第二次 Ctrl-C 恢復預設、強制結束）、`cancel()`。
    - TUI 模式：傳給 TUI 的 cancel 包一層，被呼叫就標記中斷；TUI 回錯誤時照舊 `<-done`。
    - 非 TUI 模式：`opts.progress` 為 true 才印進度行（scenario／distributed 維持現狀），否則只等 done 或訊號。
    - 中斷旗標用 `atomic.Bool`（TUI goroutine 與訊號 goroutine 都會寫）。
  - 把 `runHeadlessProgress` 移進來改成吃注入的訊號 channel（行為不變）。
- 修改 `cmd/ramplio/run.go`：
  - `runURL`／`runRPS`：改成背景 goroutine 跑 `eng.Run(ctx)`＋`rampProvider`＋`watchRun`，多回傳 `interrupted bool`、多收 `noTUI`。單一請求模式的 provider 不帶 `StepMetrics`（只有一列的 step 表沒有資訊量），用 `rampProvider` 加一個欄位控制。
  - `runScenario`、`runDistributed`：TUI／headless 那段換成 `watchRun`，scenario 多回傳 `interrupted`；distributed 忽略它（非目標）。
  - `RunE`：拿到 `interrupted` 後，摘要與 `--output`／`--report`／`--save-baseline`／`--sink` 照舊；`!noHistory && !interrupted` 才 `recordAndDiff`；被中斷時 stderr 印一行說明，最後依 D2 回傳。threshold 與錯誤率的 `os.Exit(1)` 在中斷時不判（結果不完整，判了也不準），直接走中斷結束。
  - `--no-tui` 說明文字更新（現在 `--url`／`--rps` 也適用）。
- 新增 `cmd/ramplio/runlive_test.go`（情境 1–13、16）。
- 修改 `README.md`（「即時 TUI」段補一句 `run --url`／`--rps` 也有、`--no-tui`、中斷時的行為）、`CHANGELOG.md` 的 `[Unreleased]`（專案沒有 `changelog.d/`，沿用卡 1、2a 的決定）。

可重用：`rampProvider`（run.go:421）、`reporter.RunTUI`／`LiveProvider`、`isTerminal`、`runHeadlessProgress`、`waitForCleanup`、`rateStages`／`rateProfile`、2a 測試的 `execRun`／`useHistoryDir`／`historyFiles`／`newDelayServer`。

### 完成檢查

`go test ./cmd/ramplio/... -run 'RunLive|ShouldUseTUI' -count=1`（實作前紅：`liveEnv`／`shouldUseTUI` 不存在、url 模式不呼叫 TUI、中斷後仍存紀錄；實作後綠），接著 `go test -race -count=1 ./...`、`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`、`go build ./...`。

### 驗證（端到端，第 3 段在主對話做）

1. `go run ./cmd/ramplio mock-server` 起本機目標。
2. 終端裡 `ramplio run --url http://localhost:<port>/ -d 10s` → 看到即時畫面，跑完畫面停在最後一格，下面接摘要與紀錄。
3. 同指令跑到一半按 Ctrl-C → 摘要有印、「已中斷、未存紀錄」說明、`echo $?` 符合 D2；`.ramplio/runs/` 沒有新增。
4. `ramplio run --url … -d 10s | cat` → 沒有畫面，輸出跟 main 的 binary 一樣（`diff` 兩邊輸出，除時間與數字外相同）。
5. 另開視窗 `kill -TERM <pid>`（TTY 模式）→ 跟步驟 3 相同的收尾。

### 讀程式發現的風險

- **R1 TUI 路徑沒攔 SIGTERM**（scenario、distributed 都是）：bubbletea 只把 Ctrl-C 當按鍵收，`kill` 或容器停止時行程直接死，teardown 不會跑。本卡由 `watchRun` 一律註冊訊號修掉。
- **R2 TUI 無法初始化**（例如沒有 /dev/tty，`RunTUI` 回錯誤）：原本的程式只 `<-done` 等到跑完，訊號沒人處理；`watchRun` 註冊訊號後仍能停止壓測。（第 5 段第 1 輪 M-2 更正：原本寫「stdin 不是終端時 Ctrl-C 變成 SIGINT」不成立，見 Decisions。）
- **R3 `--observe` 在中斷時**：`runObservation` 用 `-d` 推算 ramp／hold 區間，中斷的 run 區間不對，關聯結果不可信。本卡在中斷時略過觀測並印一行說明（`--strict-trust` 時算不可信）；如果審查覺得要更細，另開卡。
- **R4 中斷旗標的競態**：TUI 的 cancel 與訊號 goroutine 可能同時寫，用 `atomic.Bool`；必跑 `-race`。
- **R5 測試時間**：中斷測試用 30s 的 `-d` 但在 1 秒內送訊號，斷言「5 秒內結束」，避免拖慢整套測試。
- **R6 「Running …」那行沒遮罩網址**（2a 已記為另開卡），本卡不處理。

## Decisions

- 2026-11-26（使用者確認，D1）：非 TTY 下 `run --url`／`--rps` 不印進度行，stdout 跟改動前完全一致。理由：不影響已經在解析 stdout 的腳本；CI 想看進度可改用 scenario 或 `--dashboard`。
- 2026-11-26（使用者確認，D2）：被中斷的 run，RunE 回傳錯誤「測試被中斷，結果不完整（未存執行紀錄）」→ exit 1。理由：CI 裡被 SIGTERM 砍掉的 run 不該是綠的；不用改 `main.go`，測試直接斷言 RunE 的錯誤。
- 2026-11-26（使用者確認，D3）：`run --scenario` 也套用「中斷不存紀錄＋D2 結束碼」，消掉 2a 記下的已知限制。**行為變更**：scenario 被中斷時不再依 thresholds 決定 exit code，一律 exit 1；寫進 CHANGELOG `[Unreleased]`。
- 2026-11-26：不拆卡。即時畫面與中斷收尾都落在同一個 `watchRun` 抽取上，分開做會改兩次 `runURL`／`runRPS` 的簽名；預估產品程式 150–250 行。
- 2026-11-26：重用既有 `--no-tui`，不新增旗標。它的說明本來就寫「非終端自動啟用」，只是 url／rps 沒讀它。
- 2026-11-26：可測試性用注入（`liveEnv`＋package 變數 `newLiveEnv`），不在測試裡對自己的行程送真訊號（如果沒先註冊好就會殺掉整個測試行程）。
- 2026-11-26：中斷時 `--output`／`--report`／`--save-baseline`／`--sink` 照樣產生：使用者明確要求的輸出，中斷的結果也有診斷價值（跟 `--strict-trust` 註解「不可信 ≠ 不可看」同一個想法）。只有自動紀錄不存，避免下次自動比較看起來像大退步（跟 2a 的 discover 一致）。

- 2026-11-26（實作，修正規格 R1）：讀 bubbletea v1.3.10 原始碼（`tea.go` `handleSignals`）後發現，bubbletea 自己會 `signal.Notify(SIGINT, SIGTERM)`：SIGTERM 讓 TUI 正常結束（`Run` 回 nil），原本的程式接著就 `cancel()`，**所以 scenario／distributed 的 TUI 路徑收到 SIGTERM 並不會讓行程直接死**，規格 R1 這段寫錯了。真正的問題在 R2：stdin 不是終端時，Ctrl-C 讓 TUI 回 `ErrInterrupted`，原本的程式接著 `<-done`，**壓測會一直跑到結束**。設計不變（`watchRun` 一律自己註冊訊號；Go 會把訊號送給每個註冊的 channel，跟 bubbletea 並存沒有問題），但 `watchTUI` 多了一條規則：TUI 回 nil 時 run 還沒結束，就代表是被訊號關掉的，當成中斷（不然 TUI 跟我們的 goroutine 誰先處理訊號是競態）；TUI 回錯誤時照舊等 run 跑完，訊號仍能停下它。
- 2026-11-26（實作）：`runHeadlessProgress` 刪掉，由 `runlive.go` 的 `watchPlain` 取代（進度行格式與 2 秒間隔不變，scenario／distributed 才印，D1）。非 TUI 模式的「Interrupted — stopping the test...」照舊印在 stderr；TUI 模式不印，避免把畫面弄亂，改由 RunE 結束後印「已中斷：…」。
- 2026-11-26（實作，計畫外的測試檔）：`runScenario` 多回傳 `interrupted`，`runscenario_headless_test.go` 的呼叫從 `sum, _, err :=` 改成 `sum, _, _, err :=`，斷言沒有動。
- 2026-11-26（實作）：被中斷時 `--observe` 直接略過並在 stderr 警告（R3）；因為最後一律回中斷錯誤，`--strict-trust` 的守門不會走到，結果一樣是 exit 1。中斷錯誤回傳前設 `cmd.SilenceUsage = true`，不印 cobra 的 Usage（`assertInterrupted` 斷言 stderr 沒有 `Usage:`）。
- 2026-11-26（實作，測試修正）：`TestRunLiveOutputOrder` 第一次跑紅燈的原因在測試本身：標記「測試結果」同時出現在摘要後面的「測試結果說明」段落，被數成 2 次。改成完整的段落標題 `\n測試結果\n`，斷言內容（各 1 次、順序固定）沒有放寬。
- 2026-11-26（實作，比規格多的測試）：`TestWatchRunTUIErrorKeepsRunning`、`TestWatchRunTUIErrorThenSignalStops` 守 R2 的兩個分支。這兩條是在實作之後寫的，寫完就是綠的；另外用變異測試確認它們有效：把 `watchTUI` 改成 TUI 回錯誤也取消 run，第一條就會紅（`Should be false`），之後已還原。`TestRunLiveNonTTYSkipsTUI`、`TestRunLiveNoTUIFlag`、`TestRunDistributedUsesWatchRun` 在實作前就是綠的（它們斷言的是「沒有發生」或「跟以前一樣」），留著當回歸守門。
- 2026-11-26（實作）：產品程式 diff 是 `run.go` +66／−60、`runlive.go` +172（含註解），在規格估的 150–250 行內。
- 2026-11-26（第 5 段第 1 輪，M-2，更正 R2）：bubbletea v1.3.10（`tea.go:600-617`）在 stdin 不是終端時會改開 `/dev/tty` 讀按鍵，所以 `ramplio run … < file` 時 Ctrl-C 仍然是按鍵，不會變成 SIGINT；實作那筆 Decisions 寫的「stdin 不是終端時 TUI 回 `ErrInterrupted`、壓測跑到結束」前提不成立。真正會走到「TUI 回錯誤」分支的是 TUI 無法初始化（例如沒有 /dev/tty）。程式不用改：該分支仍是「照舊等 run 跑完、訊號仍能停下它」。CHANGELOG、R2、`TestWatchRunTUIErrorKeepsRunning` 的註解同步更正。
- 2026-11-26（第 5 段第 1 輪，M-1）：新增 `TestWatchRunSignalReleasedBeforeStop`（TUI 初始化失敗／非 TTY 兩個子測試）：假訊號來源的 stop 會記錄呼叫次數，在 cancel 被呼叫的那一刻斷言 stop 已經被呼叫過（只看結束後的次數不夠，`watchRun` 的 defer 一定會呼叫）。修正前就是綠的（行為本來就對，缺的是守門）；變異測試確認有效：拿掉 `watchSignals` 裡的 `stopSignals()`，兩個子測試都紅（「取消 run 之前就要先釋放訊號」）。它的「TUI 初始化失敗」子測試涵蓋原本的 `TestWatchRunTUIErrorThenSignalStops`（同樣的設定與 `interrupted`、時間斷言，再多一條釋放順序），所以把舊的那條併進來、刪掉。
- 2026-11-26（第 5 段第 1 輪，M-3）：被中斷時 `thresholdMsg` 固定為空字串，不呼叫 `checkThresholds`，`--output` 的 JUnit 不會記成門檻失敗。新增 `TestRunLiveInterruptOutputHasNoThresholdVerdict`（.json／.xml）：修正前 .xml 紅（`<failure message="p50 5ms &gt; 1ms">`），.json 綠（`WriteJSON` 本來就不寫門檻，留著當守門）。第一次寫這條測試時 .xml 也是綠的，原因是測試設定：情境只有一個 30 秒的 stage，引擎從 0 VU 線性爬升，500 毫秒內一個請求都沒有，門檻觸發不了；改成先 100 毫秒爬到 2 VU 再持續 30 秒，才看到預期的紅。
- 2026-11-26（第 5 段第 1 輪，L-1，不改程式）：`runScenario` 的 teardown 進行中（run 已經正常跑完負載、`watchRun` 已回傳之前）按 Ctrl-C 會被標成中斷：不存紀錄、exit 1。teardown 的時間窗很短，而且使用者確實按了 Ctrl-C，保守處理可以接受。
- 2026-11-26（第 5 段第 1 輪，L-2）：情境 3 加強：除了沒有進度行，再斷言開頭行跟改動前一樣（`Running 1 VUs for 2500ms → GET `、`Running rate mode: 5 req/s for 2500ms → GET `，這兩個 `Printf` 這張卡沒動）、有摘要段落標題、沒有 TUI 的框線或進度條字元（`╭ ╰ █ ░`）。
- 2026-11-26（第 5 段第 1 輪，L-3，不改程式）：中斷時 `--output`／`--report` 的內容就是中斷前的部分結果，檔案本身沒有標記「被中斷」；使用者從結束碼與 stderr 得知。要在檔案裡加標記會改到輸出格式，不在本卡範圍。
- 2026-11-26（第 5 段第 1 輪，L-4，不改程式，既有問題）：中斷錯誤由 `main.go` 印一次，cobra 也會印一次（`SilenceErrors` 沒設），跟卡 1 記下的另開卡問題相同，留在另開卡清單。
- 2026-11-26（使用者決定，另開卡）：既有問題：`run --url --vus N -d D` 只建一個 stage（`run.go` `runURL` 的 `Stages: []scenarios.Stage{{Duration: dur, Target: vus}}`，審查時在第 697 行），引擎從 0 線性內插，整段時間從 0 慢慢爬到 N。主對話用真 pty 實測：即時畫面前幾秒顯示 VUs: 0、Total: 0，量到的數字也比實際 N VU 低。本卡不修（M-3 的測試就是被這個行為卡住，才改成先爬升再持續）。
## History

- 2026-11-26：讀 `run.go`（runURL／runRPS／runScenario／runDistributed／runHeadlessProgress）、`reporter/tui.go`、`autohistory.go`，確認 url／rps 沒有訊號處理、TUI 路徑沒攔 SIGTERM、TUI 為 inline 模式不會蓋掉摘要。寫出 2b 規格，3 個待決定附推薦。
- 2026-11-26：使用者確認規格，D1–D3 照推薦定案；進第 2 段。
- 2026-11-26：第 2 段實作完成（TDD：先補 `liveEnv`／`shouldUseTUI` 骨架讓測試能編譯，看到行為上的紅燈——沒開畫面、中斷後跑滿 30 秒、中斷後仍存紀錄、RunE 回 nil——才實作）。commit：a3e0614 CARD 定案、50627d2 實作與測試、4b0edd0 README／CHANGELOG。
- 2026-11-26：第 3 段測試輸出（實際結尾）：
  - 完成檢查 `go test ./cmd/ramplio/... -run 'RunLive|ShouldUseTUI' -count=1`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	13.068s
    ```
  - `go test -race -count=1 ./...`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	92.380s
    ok  	github.com/machiko/ramplio/v3/internal/distributed	13.342s
    ok  	github.com/machiko/ramplio/v3/internal/engine	101.873s
    ok  	github.com/machiko/ramplio/v3/internal/reporter	2.806s
    （其餘 12 個套件同為 ok，共 16 個套件）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - repo 內沒有殘留 `.ramplio/`
  - 端到端驗證（真終端的 TUI、`kill -TERM`、管線輸出跟 main 的 binary 比對）**待確認**：這個對話沒有終端，起背景 mock-server 加 `kill` 的指令被權限擋下，留給主對話做（步驟見「驗證」）。
- 2026-11-26：第 4 段獨立審查「可以進 PR」，無 CRITICAL／HIGH，3 MEDIUM＋4 LOW。主對話用真 pty 實測：TTY 下 `run --url` 會開即時畫面；收到 SIGTERM 後 exit 1、有摘要、「已中斷」、不存紀錄；正常跑完 exit 0（第 3 段「待確認」的端到端驗證以此補上）。
- 2026-11-26：第 5 段第 1 輪修正完成。commit：09c2c8d M-3 中斷不寫門檻判定＋M-1 訊號釋放測試＋L-2 情境 3 加強、本筆（CHANGELOG M-2 更正、CARD 的 M-2／L-1／L-3／L-4／另開卡 Decisions）。
- 2026-11-26：第 5 段第 1 輪測試輸出（實際結尾）：
  - 完成檢查 `go test ./cmd/ramplio/... -run 'RunLive|ShouldUseTUI' -count=1`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	14.121s
    ```
  - `go test -race -count=1 ./...`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	93.994s
    ok  	github.com/machiko/ramplio/v3/internal/distributed	15.125s
    ok  	github.com/machiko/ramplio/v3/internal/engine	99.993s
    ok  	github.com/machiko/ramplio/v3/internal/reporter	3.504s
    （其餘 12 個套件同為 ok，共 16 個套件）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - repo 內沒有殘留 `.ramplio/`

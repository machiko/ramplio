# CARD：自動留存與比對（卡 2a）

- 分支：`feat/auto-history`（worktree：`/Users/reyes/Workspace/ramplio-worktrees/auto-history`）
- 基準：`main` @ 9a61b13
- 規模：一般
- 狀態：第 4 段複審通過，第 6 段 PR 等 CI（2026-11-26）

## 規格

### Context

易用性三張卡的第二張（第一張「單一入口」已合併，PR #1）。根因同上一張：**入口太多、每個入口只做一半**。存檔這一塊目前的情況：

- 三種存檔各要記一個旗標，而且格式互不相通：
  - `run --output x.json` → `reporter.Report`（`internal/reporter/json.go:13`），**沒有目標欄位**，只有 `report --input` 吃它，用來轉 HTML。
  - `run|discover --save-baseline x.json` → `baseline.Baseline`（`internal/baseline/baseline.go:22`），有 `Scenario`（目標識別），只有 `compare` 吃它。discover 只能產 baseline，產不出 Report，因為它沒有 `metrics.Summary`。
  - `run --report x.html` → HTML 成品，沒有程式會再讀它。
- `report` 的 `--input` 是必填（`report.go:61`）；`compare` 一定要給兩個參數（`compare.go:89`）。沒有事先存檔，跑完就什麼都比不了。
- 結果：使用者要先知道「之後想比較」，才會記得加 `--save-baseline`，而且要自己管檔名。

原本的第二張卡有 4 點，評估後拆成兩張（見 Decisions）：
- **本卡 2a**：每次 `run`／`discover` 跑完自動留一筆紀錄 → 結尾自動跟上一次同目標比較 → `compare`／`report` 不帶參數也能跑。
- **卡 2b**（下一張）：`run --url`／`run --rps` 補即時 TUI。

### 規模分級

一般（改到行為；會寫檔，但只寫在使用者的工作目錄，不碰認證授權、CI、migration）。預估產品程式 diff 約 350–450 行（不含測試）。

### 目標

- `run`（`--url`、`--rps`、`--scenario`）與 `discover`（含單一入口 `ramplio <網址|curl>`）每次跑完，自動存一筆紀錄到 `.ramplio/runs/`，不用加任何旗標。
- 紀錄檔是 **baseline 格式的超集**：最上層就是 `baseline.Baseline` 的欄位，另外多 `target_key`（比對用）與 `report`（run 模式的完整 `reporter.Report`）。所以 `ramplio compare .ramplio/runs/a.json .ramplio/runs/b.json` 不用改程式就能讀。
- 結尾自動跟「上一次同一個目標」比較，印一段白話變化（p99、錯誤率、吞吐量；discover 為安全上限／崩潰點）。**這段只是資訊，不影響 exit code。**
- `ramplio compare` 不帶參數：比「最近一筆」與「同目標的前一筆」，沿用既有的退步 → exit 1 契約。
- `ramplio report` 不帶參數：用最近一筆 run 紀錄產生 HTML。
- 存檔失敗（唯讀目錄、磁碟滿）只印一行警告，不影響壓測結果、報告輸出與 exit code。
- 存檔內容的網址一律經 `maskURLForDisplay` 遮罩。
- `--no-history` 可關掉自動留存。
- （範圍擴充，使用者決定）`run --save-baseline` 存檔前也用 `maskURLForDisplay` 遮罩目標網址。

### 非目標

- 不做 `run --url`／`--rps` 的 TUI（卡 2b）。
- 不改 `--output`／`--report`／`--save-baseline` 的格式，舊檔照舊可用（唯一的行為改動是 `run --save-baseline` 的網址改為遮罩後的值，見情境 17）。
- 不自動產 HTML（`ramplio report` 零參數即可產生）。
- dashboard（`run --dashboard`）與分散式（`--worker`）不留存：兩者目前都拿不到結束時的 `metrics.Summary`，要改執行流程，另開卡。
- 不遮罩情境檔內使用者自訂的 step 名稱（那是使用者自己寫的文字）。
- 不新增第三方相依套件。

### 驗收情境（給定／當／則／對應測試）

存檔與內容

1. 給定本機 httptest server、空的紀錄目錄，當執行 `run --url <server> -d 1s`，則 `runs/` 底下多 1 個檔，`baseline.Load` 讀得到（`Metrics` 非 nil），`report` 欄位存在。→ `cmd/ramplio/autohistory_test.go: TestHistoryRunAutoSaves`
2. 給定同上，當執行 `discover --url <server>`（測試用縮短探測時間），則多 1 筆紀錄，`Discover` 非 nil、沒有 `report`。單一入口 `ramplio <server>` 也一樣。→ `TestHistoryDiscoverAutoSaves`
3. 遮罩（負向）：給定網址 `…/?token=SECRET123#frag`，當 run 或 discover 跑完，則紀錄檔原文找不到 `SECRET123` 與 `frag`（含 `scenario`、`target_key`、`report.steps[].name`）。→ `TestHistoryMasksURL`
4. 被擋下的寫入型 discover（POST 沒加 `--allow-write`）不存紀錄。→ `TestHistoryBlockedWriteNotSaved`
5. `--no-history` 時不建立 `.ramplio/`、不寫任何檔。→ `TestHistoryNoHistoryFlag`

失敗不影響結果（負向）

6. 給定紀錄目錄是唯讀，當 `run --url` 跑完，則 stderr 有一行「無法存執行紀錄」的警告，摘要照印，RunE 回 nil（exit 0）。discover 同樣。→ `TestHistoryReadOnlyDirKeepsResult`
7. 給定 `runs/` 裡有 1 個壞掉的 JSON，當這次跑完要找上一次，則略過壞檔並警告一行，本次照存、照比、exit code 不變。→ `TestHistorySkipsCorruptRecord`

自動比對

8. 給定同一目標、同樣負載參數已有 1 筆紀錄，當再跑一次，則結尾印「跟上一次（<時間>）比」段落，含 p99、錯誤率、吞吐量的前後值與持平／改善／退步。→ `TestHistoryAutoDiffSameTarget`
9. 給定沒有同目標紀錄（第一次跑、換網址、`--vus` 不同、header 不同、body 不同），則不比較，只印「已存為這個目標的第一筆紀錄」。header 只差在順序或名稱大小寫時仍算同一目標。run 與 discover 一致。紀錄檔只存 header＋body 的雜湊，原文找不到 header 值與 body（負向）。→ `TestHistoryAutoDiffSkipsDifferentTarget`、`TestHistoryAutoDiffHeaderOrderAndCaseSameTarget`、`TestHistoryDiscoverDifferentHeaderNotCompared`、`TestHistoryRecordOmitsHeaderAndBody`、`internal`：`TestHistoryRequestFingerprint`
10. 退步不改 exit code（負向）：給定上一次 p99 很低、這次 server 刻意變慢，當跑完，則比對段落標「退步」，但 RunE 仍回 nil。→ `TestHistoryAutoDiffRegressionKeepsExitCode`

.gitignore 與保留上限

11. 給定 `.ramplio/` 不存在，當第一次存檔，則同時寫 `.ramplio/.gitignore`（內容 `*`）並印一行說明；第二次不再印。→ `TestHistoryFirstCreateWritesGitignore`
12. 給定已有 N 筆（N＝保留上限），當存第 N+1 筆，則最舊的一筆被刪，總數維持 N；刪檔失敗只警告。→ `internal/history/history_test.go: TestHistorySavePrunesOldest`

零參數指令

13. 給定同目標 2 筆紀錄且第 2 筆退步，當執行 `ramplio compare`（零參數），則印出比較並回錯誤（exit 1）；沒退步則 exit 0。→ `cmd/ramplio/compare_test.go: TestCompareZeroArgUsesHistory`
14. 給定紀錄少於 2 筆（或最近一筆沒有同目標的前一筆），當 `ramplio compare`，則回錯誤並說明「先跑兩次同一個目標」的指令範例，exit ≠ 0。→ `TestCompareZeroArgNotEnoughHistory`
15. 給定最近有 1 筆 run 紀錄，當 `ramplio report`（零參數），則產生 `report.html`，並印出用的是哪一筆（時間＋遮罩後目標）。只有 discover 紀錄時回錯誤並說明 discover 沒有 HTML 報告。→ `cmd/ramplio/report_test.go: TestReportZeroArgUsesLatestRun`、`TestReportZeroArgOnlyDiscover`
16. 相容：`compare a.json b.json`、`report --input x.json`、`--save-baseline`、`--output` 既有測試不改、照樣綠；`compare` 只給 1 個參數回用法錯誤。→ 既有 `compare_test.go`、`baselinesave_test.go` ＋ `TestCompareOneArgRejected`

`--save-baseline` 遮罩（範圍擴充）

17. 負向：給定帶帳密與 token 的網址（`http://user:PASSWORD9@host/?token=SECRET123`），當執行 `run --url … --save-baseline b.json`，則讀回 `b.json` 原文找不到 `PASSWORD9` 與 `SECRET123`，`scenario` 等於該網址經 `maskURLForDisplay` 的結果。→ `cmd/ramplio/baselinesave_test.go: TestRunSaveBaselineMasksURL`

### 實作計畫（檔案）

- 新增 `internal/history/history.go`＋`history_test.go`（純資料與檔案操作，不碰 cmd）：
  - `type Record struct { baseline.Baseline（內嵌，JSON 攤平在最上層）; TargetKey string; Report *reporter.Report }`。
  - `Save(dir string, rec Record, keep int) (path string, created bool, err error)`：`MkdirAll` → 第一次建立時寫 `.gitignore`（`*`）→ 檔名 `<UTC 時間 20261125T153012.123Z>-<run|discover>.json`，用 `O_EXCL` 避免撞名 → 暫存檔＋rename（照 `baseline.Save` 的做法）→ 依檔名排序刪掉超過 `keep` 的舊檔。
  - `List(dir) ([]Entry, []error)`：新到舊，壞檔放進 errors 不中斷。
  - `PreviousSameTarget(entries, key, excludePath)`、`Latest(entries, filter)`。
  - 檔案權限 0o644／目錄 0o755，跟既有 baseline 一致。
- 新增 `cmd/ramplio/autohistory.go`＋`autohistory_test.go`：
  - `historyDir()`：預設 `.ramplio`（目前工作目錄），環境變數 `RAMPLIO_HISTORY_DIR` 可覆寫（測試隔離與 CI 用）。
  - `targetKey(...)`：組出「模式＋方法＋遮罩後網址或情境檔路徑＋負載參數」字串（見 Decisions「同一個目標」）。
  - `recordAndDiff(outW, errW, rec)`：存檔 → 失敗只警告 → 找同目標前一筆 → `baseline.Compare` → `renderAutoDiff` 印精簡段落（重用 `baseline.MetricLabel`／`FormatMetricValue`，不重用 `renderComparison` 是因為它的結論句是守門口吻「建議先查再上」）。
  - 新增 `TestMain`：把 `RAMPLIO_HISTORY_DIR` 設到暫存目錄，讓既有的 cobra 層測試不會在 `cmd/ramplio/` 底下留下 `.ramplio/`。
- 修改 `cmd/ramplio/run.go`：
  - 新增 `--no-history` 旗標。
  - 在 `--save-baseline` 之後、threshold 判定與 `os.Exit(1)` 之前呼叫 `recordAndDiff`（threshold 失敗的那次也要留）。
  - （情境 17）`--save-baseline` 的 `baseline.FromSummary(sum, scenarioName)` 改傳遮罩後的識別：`--url` 模式用 `maskURLForDisplay(url)`，情境模式維持檔名。
  - `runURL` 的 step 名稱 `req.Method + " " + req.URL` 改用 `maskURLForDisplay`，不然原始網址會跟著 `report.steps[].name` 存進檔（終端摘要也會變成遮罩後的網址，屬於刻意的改動）。
- 修改 `cmd/ramplio/discover.go`：`discoverOptions` 加 `NoHistory`；`runDiscover` 存完 `--save-baseline` 後呼叫 `recordAndDiff`（`blockWrite` 路徑不走到這裡，天然不存）；新增 `--no-history` 旗標。單一入口走 `runDiscover`，自動受惠，`smartentry.go` 不用改。
- 修改 `cmd/ramplio/compare.go`：`Args` 改成 0 或 2 個；0 個時從 history 取「最近一筆＋同目標前一筆」，其餘沿用 `baseline.Compare`＋`renderComparison`＋`compareVerdictErr`。更新 `Use`／`Long` 說明。
- 修改 `cmd/ramplio/report.go`：拿掉 `MarkFlagRequired("input")`；沒給 `--input` 時取最近一筆有 `report` 的紀錄，交給既有的 `reporter.WriteHTMLFromReport`。
- 新增 `cmd/ramplio/report_test.go`（目前沒有這個檔）；`cmd/ramplio/compare_test.go` 只新增測試，不改既有斷言。
- 修改 `.gitignore`（本 repo 加 `.ramplio/`）、`README.md`、`docs/getting-started.md`（「比較兩次結果」改寫成零參數用法）、`CHANGELOG.md` 的 `[Unreleased]`（沿用卡 1 的決定：專案沒有 `changelog.d/`）。

可重用：`maskURLForDisplay`（preflight.go:137）、`baseline.FromSummary`／`FromDiscover`／`Compare`／`MetricLabel`／`FormatMetricValue`、`reporter.SummaryToReport`／`WriteHTMLFromReport`、`renderComparison`／`compareVerdictErr`（compare.go）、`currentGitCommit`、`writeBaselineFile` 的「失敗只警告」寫法。

### 完成檢查

`go test ./internal/history/... ./cmd/ramplio/... -run 'History|ZeroArg|OneArg|SaveBaselineMasks' -count=1`（實作前紅：套件不存在／測試失敗；實作後綠），接著 `go test -race -count=1 ./...`、`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`、`go build ./...`。

### 驗證（端到端）

1. `go run ./cmd/ramplio mock-server` 起本機目標。
2. 在暫存目錄跑兩次 `ramplio run --url http://localhost:<port>/ -d 5s` → 第一次看到「第一筆紀錄」與 `.gitignore` 說明，第二次看到「跟上一次比」段落。
3. `ramplio compare` → 看到兩筆的比較；`ramplio report` → 產生 `report.html`。
4. `chmod 500 .ramplio` 後再跑一次 → 只有一行警告，摘要正常，`echo $?` 為 0。

## Decisions

- 2026-11-25：原第二張卡的 4 點拆成兩張。**2a（本卡）**：自動留存＋自動比對＋`compare`／`report` 零參數，這三點共用同一份紀錄格式，分開做會改兩次介面。**2b（下一張）**：`run --url`／`--rps` 補即時 TUI。`runURL`／`runRPS` 要改成 runScenario 那種背景執行＋provider＋非終端改印進度行的結構，跟留存互不相依；4 點一起做會超過一天的量。
- 2026-11-25：紀錄檔用「baseline 超集」一種檔案，不分 baseline／report 兩個檔。理由：`baseline.Load` 用 `encoding/json`，預設會略過不認識的欄位，所以紀錄檔可以直接給既有的 `compare` 吃；一次執行對應一個檔，清理與「同一筆」的認定都比較單純。`Record` 新增的欄位不寫進 `baseline.Baseline`，baseline 的 schema 不用升版。
- 2026-11-25：自動比對只是資訊、不影響 exit code；守門仍由明確下 `compare` 或 thresholds 負責。理由：一般使用者每次跑完都會看到比對，網路雜訊就可能讓 exit code 跳來跳去，在 CI 會變成誤報。
- 2026-11-25（使用者確認）：保留筆數＝所有目標合計最近 50 筆，超過刪最舊。
- 2026-11-25（使用者確認）：紀錄放目前工作目錄的 `.ramplio/`，`RAMPLIO_HISTORY_DIR` 可覆寫。
- 2026-11-25（使用者確認）：「同一個目標」＝模式＋HTTP 方法＋遮罩後網址（情境模式用情境檔路徑）＋負載參數全部相同（2026-11-25 第 5 段第 1 輪再加上 header＋body，見下）；run 的負載參數是 `--vus` 或 `--rps` 加 `-d`，discover 是 `--tolerance`、`--max-rps`、`--probe-duration`。
- 2026-11-25（使用者確認）：第一次建立 `.ramplio/` 時自動寫 `.ramplio/.gitignore`（內容 `*`）並印一行說明。
- 2026-11-25（使用者確認）：`compare` 零參數比「最近一筆」與「同目標的前一筆」。
- 2026-11-25（使用者確認，範圍擴充）：原本記為另開卡的既有問題——`run --url --save-baseline` 的 `Scenario` 存原始網址（`run.go:206` `baseline.FromSummary(sum, scenarioName)`）——併進本卡修，新增驗收情境 17。
- 2026-11-25（實作，偏離規格）：紀錄檔名從規格寫的 `<時間>-<run|discover>.json` 改成 `<時間>-<序號>.json`，kind 只存在內容裡。理由：`-race` 跑 `TestHistoryListNewestFirstSkipsCorrupt` 時抓到真的 bug——同一毫秒存兩筆不同 kind 的紀錄，兩個檔都拿到序號 00（撞名檢查把 kind 算進檔名），排序就退回去比 kind 的字母，新舊顛倒。拿掉 kind 後 `O_EXCL` 才真正保證序號唯一、檔名順序＝存檔順序。測試沒改。
- 2026-11-25（實作）：`history.Save` 的回傳值從規格寫的 `(path, created, err)` 改成 `(SaveResult{Path, Created, PruneErr}, err)`。刪舊檔失敗時存檔本身已經成功，要能單獨回報成警告，不能混在 `err` 裡。
- 2026-11-25（實作，規格外的小補強）：discover 被中斷（ctx 取消）時不存紀錄。只探測一半的結果，下次比對會看起來像大退步。run 的情境模式按 Ctrl-C 中斷時，現有流程分不出是不是跑完了，照樣會存（只影響參考用的自動比對，不影響結束碼），列為已知限制。
- 2026-11-25（實作）：`compare.go` 的 `renderComparison` 和新的 `renderAutoDiff` 共用 `verdictMarkWord`（判定 → 符號＋白話），避免兩份用語分歧。`compare`／`report` 的輸出改走 `cmd.OutOrStdout()`；正式執行時一樣是 stdout，測試才抓得到輸出。
- 2026-11-25（實作）：`run.go` 的自動紀錄訊息走 `cmd.OutOrStdout()`／`cmd.ErrOrStderr()`；正式執行時一樣是 stdout／stderr，測試可以分別斷言。原有的摘要仍然直接寫 `os.Stdout`，這次沒動。
- 2026-11-25（實作）：`TestHistoryBlockedWriteNotSaved`（情境 4）和 `TestCompareOneArgRejected`（情境 16）在實作前就是綠的。前者斷言「沒有發生」，原本就不存檔；後者原本由 cobra 的 `ExactArgs(2)` 擋。兩條都留下當回歸守門：實作後改成 `compareArgs`、`runDiscover` 加了存檔，它們仍然要綠。
- 2026-11-25（實作）：`report_test.go` 比規格多兩條：`TestReportZeroArgNoHistory`（完全沒有紀錄）、`TestReportExplicitInputStillWorks`（情境 16 的 `report --input` 相容性，原本沒有測試）。
- 2026-11-25（實作）：產品程式 diff 是 +526／−34 行（不含測試與文件），比規格估的 350–450 行多。多出來的主要在 `internal/history`（245 行，含註解與錯誤處理）。
- 2026-11-25：觀察到的既有問題（計畫外，本卡不修，另開卡）：`run --url` 的 `--output x.xml`（JUnit）和 `--sink` 收到的 `scenarioName` 仍是原始網址；終端的「Running … → <網址>」那行也沒遮罩。
- 2026-11-25（使用者確認，第 5 段第 1 輪 M1）：「同一個目標」再加入 header＋body。把 header 依名稱排序（名稱轉小寫正規化，值不動）再接上 body，取 sha256 的前 8 碼放進 target key。**只存雜湊、不存明文**。代價：token 輪換後會被當成新目標，從第一筆重新累積。run、discover、單一入口一致；情境檔模式仍只看路徑。
- 2026-11-25（第 5 段第 1 輪）：M1 的雜湊放在 `history.RequestFingerprint`（純函式）。每一段都先寫長度再寫內容，header 值跟 body 的邊界就不會被挪動（例如 `X-A: 1b`＋空 body 跟 `X-A: 1`＋body `b` 不會撞在一起）。target key 一律帶 `req=<8 碼>`，沒有 header 與 body 時也帶。`TestHistoryAutoDiffHeaderOrderAndCaseSameTarget` 與 `TestHistoryRecordOmitsHeaderAndBody` 在修正前就是綠的（原本 key 根本不看 header，紀錄檔也從來不存 header），兩條留著當回歸守門。
- 2026-11-25（第 5 段第 1 輪）：L3 不只是取「現有最大時間戳」跟「現在」比較大的那個，而是用 `max(現在, 最大時間戳＋1ms)`。如果直接沿用最大時間戳，萬一有一個檔名在未來的檔（時鐘大幅倒退、手動放進來的檔），之後每次都會擠在同一毫秒，序號 00–99 用完就再也存不了；每次加 1ms 就不會用完。時鐘改成可注入的 package 變數 `nowFunc`，只有 history 的測試會替換它（該套件沒有平行測試）。
- 2026-11-25（第 5 段第 1 輪）：L5 讓 `history.List` 回傳 `(entries, skipped, err)`，把「整個目錄讀不了」（`err`）跟「個別壞檔」（`skipped`）分開。`recordAndDiff` 遇到 `err` 就印一行警告後結束，不再 Save，也不比較；`compare`／`report` 零參數遇到時直接回錯誤。既有測試的 `List` 呼叫跟著改成三個回傳值，斷言沒有改。
- 2026-11-25（第 5 段第 1 輪）：L1：0 byte 的檔（另一個行程 `O_EXCL` 佔位、還沒 rename 進來）和讀檔前就消失的檔（被並行清理掉）都靜默略過。L2：把刪檔拆成 `removeOldest`，測試可以直接餵一個不存在的檔名；`fs.ErrNotExist` 不算失敗。
- 2026-11-25（第 5 段第 1 輪）：L6：`SaveResult.Created` 改名為 `WroteGitignore`，語意改成「這次有沒有實際寫入 .gitignore」。`.gitignore` 用 `O_EXCL` 建立，使用者自己的 `.gitignore` 絕不覆寫（`TestHistoryGitignoreKeepsUserFile`）。`TestHistorySaveFirstCreateWritesGitignore` 的斷言跟著改名，從 `first.Created` 改成 `first.WroteGitignore`，判斷的內容不變。提示文字從「已建立 X/ 存放執行紀錄」改成「執行紀錄放在 X/（已在裡面自動放 .gitignore…）」，因為目錄可能是使用者自己建的。
- 2026-11-25（第 5 段第 1 輪）：L8：`report` 設 `SilenceUsage`。沒有像 `compare` 一樣設 `SilenceErrors`，所以錯誤訊息仍會印兩次，那是卡 1 記下的另開卡問題，這裡不處理。
- 2026-11-25（第 5 段第 1 輪）：L4、L7 不改程式，寫進 README 與 getting-started。L4：壞檔或較新 schema 的紀錄檔會被略過並警告，但不會被自動刪除；檔名排在最新的壞檔需要手動刪。L7：情境檔模式只看檔案路徑，改了內容但路徑不變，仍會跟上一次比較。
- 2026-11-25（第 5 段第 1 輪）：internal/history 新增的幾條 LOW 測試，紅燈原因是「新 API 不存在」（`removeOldest`、`nowFunc`、`WroteGitignore`、`List` 三個回傳值），沒有先看到行為上的紅。cmd 層的 L5、L6、L8 則是先只改成新簽名、行為不變，實際看到預期的紅（警告 2 行、「已建立」字樣、印出 Usage）之後才修。
- 2026-11-25（第 5 段第 2 輪，M-2 退化修正）：第 1 輪 L6 的「沒有 .gitignore 就寫 `*`」，在 `RAMPLIO_HISTORY_DIR` 指到專案根目錄、`$HOME` 或共用目錄時，會讓整個目錄不被 git 追蹤。現在只在這兩種情況寫：(1) 這次才建立紀錄目錄；(2) 目錄裡除了 `runs/`（和 `.gitignore` 本身）沒有別的東西，使用者自己先 `mkdir` 的空目錄也算。其他情況不寫，`SaveResult.NeedsIgnoreHint` 只在「這次才建立 `runs/`」那一次為 true，cmd 層印一行提示請使用者自己把 `<目錄>/runs` 加進 .gitignore。目錄讀取失敗一律視為「有別的東西」（寧可不寫）。掃描目錄每次只讀 16 筆，碰到第一個不相干的項目就停，不會把 `$HOME` 整個列出來。`TestHistoryGitignoreWrittenWhenOnlyRunsExists` 修正前就是綠的，留著當守門。
- 2026-11-25（第 5 段第 2 輪，M-1，只改文件）：target key 的請求雜湊是沒加鹽的 sha256 前 8 碼，**雜湊不等於加密**，低熵的 header／body（短 token、PIN）可以離線窮舉還原。README 與 getting-started 已註明「不要把 `.ramplio/` 紀錄或 baseline 檔貼到公開地方」。
- 2026-11-25（第 5 段第 2 輪，L-1）：指紋先依「名稱轉小寫」排序，再依原始名稱、值排序，同名不同大小寫的 header 同時出現時也是同一個順序。照字面「先依原始名稱排序」的話，大小寫混用時會打破第 1 輪的「名稱大小寫不影響」（`X-B` 排在 `x-a` 前面），所以先比小寫。寫進雜湊的仍是小寫名稱＋值，兩個都保留，不合併。
- 2026-11-25（第 5 段第 2 輪，L-2）：`nextStamp` 改成掃過所有檔名，取最大的可解析時間戳，排在最後的外來檔（如 `zzz.json`）不會讓時鐘倒退保護失效。
- 2026-11-25（第 5 段第 2 輪，L-3，不改程式）：行程在 O_EXCL 佔位之後、rename 之前被砍掉，會留下 0 byte 的佔位檔或 `*.json.tmp`。0 byte 檔 `List` 會靜默略過，但它算在保留筆數裡，會照輪替被刪；`*.json.tmp` 不符合 `*.json` 不會被讀，也不會被輪替，需要手動清。
- 2026-11-25（第 5 段第 2 輪，L-4，不改程式）：只有 baseline 欄位、沒有 `target_key` 的檔（例如手動把 `--save-baseline` 的檔放進 `runs/`）照樣會被讀進來，但空的 key 永遠不會被當成同目標，不會被自動比較；`compare` 零參數時如果它是最近一筆，會回「沒有同目標的前一筆」。
- 2026-11-26（第 4 段第 3 次審查）：LOW 不處理，寫進 PR 說明：(1) LOW-1：在空的專案根目錄設 `RAMPLIO_HISTORY_DIR=.`，因為目錄裡只有 `runs/`，仍會寫入 `*` 的 `.gitignore`；(2) LOW-2：`runs/` 建立之後如果存檔失敗，下一次不會再印「請自己忽略 runs/」的提示（提示只在建立 `runs/` 那次印）。

## History

- 2026-11-25：讀程式釐清三種存檔格式（JSON 報告、baseline、HTML）各自的結構與消費者，決定拆成 2a／2b，寫出 2a 規格。
- 2026-11-25：使用者確認規格（5 個待決定照推薦定案），併入 `run --save-baseline` 遮罩（情境 17）；進第 2 段。
- 2026-11-25：第 2 段實作完成（TDD，每塊先看到預期原因的紅燈再實作）。commit：7877c1a CARD 定案、3facf62 `internal/history`、fcaf789 `run --save-baseline` 遮罩（情境 17）、3752e20 run／discover 自動留存與比對、5fee8d3 同毫秒排序修正、6209f0c `compare` 零參數、cb98efa `report` 零參數、d6e4518 文件。
  - 注意：3752e20 commit 時，指令用 `| tail` 吃掉了 `go test -race` 的失敗 exit code，所以這筆 commit 當下 `internal/history` 在 `-race` 下是紅的（同毫秒排序 bug）。下一筆 5fee8d3 修正，之後的檢查都用 `set -o pipefail`。
- 2026-11-25：第 3 段測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/history/... ./cmd/ramplio/... -run 'History|ZeroArg|OneArg|SaveBaselineMasks' -count=1`：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/history	0.515s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	15.707s
    ```
  - `go test -race -count=1 ./...`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	71.697s
    ok  	github.com/machiko/ramplio/v3/internal/baseline	3.342s
    ok  	github.com/machiko/ramplio/v3/internal/discover	13.784s
    ok  	github.com/machiko/ramplio/v3/internal/engine	102.526s
    ok  	github.com/machiko/ramplio/v3/internal/history	1.375s
    ok  	github.com/machiko/ramplio/v3/internal/reporter	5.016s
    （其餘 10 個套件同為 ok，共 16 個套件）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`（v2.15.0）：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - 測試跑完後 repo 內沒有殘留 `.ramplio/`（`TestMain` 把 `RAMPLIO_HISTORY_DIR` 指到暫存目錄）。
- 2026-11-25：第 4 段獨立審查結論「可以進 PR」，無 CRITICAL／HIGH，1 MEDIUM＋8 LOW。使用者確認同目標判定加入 header＋body 雜湊（ea1233f 改規格）。
- 2026-11-25：第 5 段第 1 輪修正完成。commit：ea1233f 規格、7fbc8ab M1 header＋body 雜湊、6a73811 L1／L2／L3／L5／L6／L8、062d1dc 文件（M1 說明、L4、L7）。
- 2026-11-25：第 5 段第 1 輪測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/history/... ./cmd/ramplio/... -run 'History|ZeroArg|OneArg|SaveBaselineMasks' -count=1`（篩選字串不用改，新測試名稱都含 `History` 或 `ZeroArg`；`-v` 數到 69 個 PASS，含子測試）：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/history	0.366s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	25.968s
    ```
  - `go test -race -count=1 ./...`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	77.840s
    ok  	github.com/machiko/ramplio/v3/internal/discover	12.791s
    ok  	github.com/machiko/ramplio/v3/internal/engine	103.196s
    ok  	github.com/machiko/ramplio/v3/internal/history	5.839s
    ok  	github.com/machiko/ramplio/v3/internal/scenarios	3.463s
    （其餘 11 個套件同為 ok，共 16 個套件）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - repo 內沒有殘留 `.ramplio/`
- 2026-11-25：第 5 段第 2 輪（最後一輪）修正完成。commit：9369fe9 M-2 .gitignore 退化修正＋L-1 指紋固定＋L-2 時間戳掃全部檔名、cc8efc1 文件（M-1 雜湊不等於加密、非空目錄不寫 .gitignore）、本筆（CARD：M-1、L-3、L-4 等 Decisions）。
- 2026-11-25：第 5 段第 2 輪測試輸出（實際結尾）：
  - 完成檢查 `go test ./internal/history/... ./cmd/ramplio/... -run 'History|ZeroArg|OneArg|SaveBaselineMasks' -count=1`（篩選字串不變；`-v` 數到 74 個 PASS，含子測試）：
    ```
    ok  	github.com/machiko/ramplio/v3/internal/history	0.319s
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	26.536s
    ```
  - `go test -race -count=1 ./...`（exit 0）：
    ```
    ok  	github.com/machiko/ramplio/v3/cmd/ramplio	78.353s
    ok  	github.com/machiko/ramplio/v3/internal/engine	101.277s
    ok  	github.com/machiko/ramplio/v3/internal/history	4.331s
    ok  	github.com/machiko/ramplio/v3/internal/scenarios	1.937s
    （其餘 12 個套件同為 ok，共 16 個套件）
    ```
  - `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest run`：`0 issues.`，exit 0
  - `go build ./...`：exit 0，無輸出
  - repo 內沒有殘留 `.ramplio/`
- 2026-11-26（第 4 段）：第 3 次獨立審查結論「可以進 PR」，無 CRITICAL／HIGH／MEDIUM，LOW 2 條記入 Decisions，不處理。主對話另做端到端驗證：兩次 run 自動比較、`compare`／`report` 零參數、紀錄檔沒有 token 原文，都通過。進第 6 段。

# CARD：探測結果一步存成帶門檻的情境檔（卡 3a）

- 分支：`feat/save-scenario`（worktree：`/Users/reyes/Workspace/ramplio-worktrees/save-scenario`）
- 基準：`main` @ e6072a9
- 規模：一般
- 狀態：第 4 段複審通過，待使用者同意 push 與開 PR（2026-11-26）

## 規格

### Context

易用性路線卡 3（卡 1 單一入口、2a 自動留存、2b run 即時畫面都已合併）。現況：`discover` 探出安全上限後只印報告、可存 baseline，但要「以後照這個負載重跑、退步就 fail」得自己手寫 scenario.yaml（stages＋thresholds＋header）。使用者會探測，卻不會把結果變成可重複的守門測試。

卡 3 原本還包含「收斂 CLI `init` 與 dashboard guided 兩套精靈」。讀程式後發現兩者不是同一種東西：`init` 產生 YAML，guided（`internal/dashboard/guided.go`）是給 PM 輸入商業參數直接開跑並給判定，不產生 YAML。收斂需要先討論定位，拆成 3b，本卡不做。

### 規模分級

一般（改到行為）。

### 目標

1. `ramplio discover …`（`--url` 或 `--curl`）新增 `--save-scenario <路徑>`：探測完成後，把結果寫成可直接 `ramplio run --scenario <路徑>` 的情境檔：
   - stages：30s 內爬升到 `target_rps = SafeLimit`，再維持 2m（用情境檔既有的 `target_rps` 欄位）。
   - steps：一個 step，method／URL／header／body 來自這次探測的請求。
   - thresholds：`p99_ms` = 這次的 `--tolerance`（毫秒），`error_rate_pct` = 1（與探測判定 PASS 的標準一致）。
2. 憑證不落地：敏感 header（判定沿用專案既有的敏感關鍵字，至少含 Authorization、Cookie、含 token/key/secret 字樣者）的值寫成 `{{env "RAMPLIO_H_<名稱大寫、- 換 _>"}}`；URL 的敏感 query 值寫成 `{{env "RAMPLIO_Q_<KEY 大寫>"}}`。寫檔後印出需要 export 的變數名稱（不印值）。body 照原文寫入，非空時印一行提醒「body 以明文存進檔案」。
3. 不寫檔（印原因、exit 非 0）的情況：SafeLimit 為 0；探測被中斷；寫入型方法沒加 `--allow-write`（原本就只送預檢）；目標檔已存在（不覆蓋）；URL 含 userinfo（`user:pass@`，提示改用 Authorization header）。
4. 沒加 `--save-scenario` 而探測成功時，報告最後多一行提示可以加 `--save-scenario scenario.yaml` 存成情境檔（提示內不出現 token 原文）。
5. 刪除 `cmd/ramplio/init.go` 中叫人執行不存在的 `./scripts/generate_sessions.sh` 那段說明，改成一句說明 sessions.csv 需要每列一個已登入的 session cookie 值（不推薦不存在的工具）。

### 非目標

- 不收斂兩套精靈（3b）。
- 單一入口 `ramplio <網址>` 本身不接受 `--save-scenario`（只在提示裡導向 `discover`）。
- 不改 discover 探測演算法、不改情境檔 schema、不新增相依套件。
- 不自動覆蓋、不做 `--force`。

### 驗收情境（給定／當／則／對應測試）

1. 給定本機 httptest server，當 `discover --url <srv> --save-scenario out.yaml`，則 out.yaml 存在、`scenarios` 解析與驗證通過，stages 末段 `target_rps` = SafeLimit，thresholds `p99_ms` = tolerance 毫秒、`error_rate_pct` = 1。→ `TestDiscoverSaveScenarioWritesRunnableFile`
2. 給定帶 `Authorization: Bearer secret123` 的 `--curl`，當存檔，則檔案內容不含 `secret123`，含 `{{env "RAMPLIO_H_AUTHORIZATION"}}`；輸出列出 `RAMPLIO_H_AUTHORIZATION` 且不含 `secret123`。→ `TestSaveScenarioMasksSensitiveHeaders`
3. 承 2，給定設好該環境變數，當用產生的檔案 `run --scenario` 對 httptest 跑（短時間），則 server 收到的 Authorization 等於環境變數的值。→ `TestSavedScenarioRunsWithEnvCredentials`（證明產物真的能跑，不只是字串像）
4. 給定 URL 含 `?token=abc`，則檔案不含 `abc`，改為 `{{env "RAMPLIO_Q_TOKEN"}}`。→ `TestSaveScenarioMasksSensitiveQuery`
5. 負向：SafeLimit 為 0（server 一律 500）→ 不產生檔案、exit 非 0、訊息說明原因。→ `TestSaveScenarioRefusesZeroSafeLimit`
6. 負向：目標檔已存在 → 原檔內容不變、exit 非 0。→ `TestSaveScenarioRefusesOverwrite`
7. 負向：POST 沒加 `--allow-write` → 只送 1 發預檢、不產生檔案。→ `TestSaveScenarioWriteMethodBlocked`
8. 負向：URL 含 userinfo → 不產生檔案、提示改用 header。→ `TestSaveScenarioRefusesUserinfo`
9. 沒加 `--save-scenario` 的成功探測 → 報告結尾有 `--save-scenario` 提示，且不含 header 值原文。→ `TestDiscoverReportHintsSaveScenario`
10. `init` 輸出不再出現 `generate_sessions.sh`。→ `init_test.go` 新增斷言

### 實作計畫（檔案）

- 新增 `cmd/ramplio/savescenario.go`＋`savescenario_test.go`：由 `discover.DiscoverResult`＋請求＋tolerance 產生 `scenarios.Scenario`（或 YAML 字串）、敏感值換成 env 引用、寫檔（`O_EXCL` 防覆蓋）。敏感判定重用 `preflight.go`／`discover.go` 既有的遮罩規則，不另寫一份關鍵字表。
- 修改 `cmd/ramplio/discover.go`：新增 `--save-scenario` 旗標，`discoverOptions` 加欄位，`runDiscover` 成功後呼叫；未指定時印提示。
- 修改 `cmd/ramplio/init.go`：移除 generate_sessions.sh 段落。
- 修改 `cmd/ramplio/init_test.go`、`discover_test.go`（如需要）。
- 修改 `README.md`、`docs/getting-started.md`、`CHANGELOG.md`（Unreleased 新增一條）。

先讀：`internal/scenarios/scenario.go`（Stage.TargetRPS、Thresholds）、`internal/scenarios/template.go`（`{{env}}` 套用在哪些欄位，若 URL／header 不套用 template 要先回報，不要擅改 schema）、`cmd/ramplio/preflight.go` 的 `maskURLForDisplay`、`discover.go` 的 `maskedHeaders`。

### 完成檢查

`go test ./cmd/ramplio/... -run 'SaveScenario|SavedScenario|HintsSaveScenario|Init'`（實作前紅、實作後綠），接著 `go test -race ./...`、`golangci-lint run`、`go build ./...`。

### 驗證（端到端，第 3 段在主對話做）

1. `ramplio mock-server --port 8080 --latency 20ms`
2. `ramplio discover --url http://localhost:8080/ --probe-duration 3s --save-scenario /tmp/s.yaml` → 看檔案內容
3. `ramplio run --scenario /tmp/s.yaml` → 跑得起來、門檻有判定

## Decisions

- 2026-11-26 卡 3 拆 3a／3b：guided 不產生 YAML，與 init 定位不同，收斂需先討論。
- 2026-11-26 負載持續在 SafeLimit（不打折）：SafeLimit 是實測 PASS 的最高點，守門要能抓到「退步到撐不住原本容量」。
- 2026-11-26 門檻沿用探測 PASS 標準（p99 ≤ tolerance、錯誤率 < 1%），讓「存下來的情境」與「探測說安全」語意一致。
- 2026-11-26 敏感值改 `{{env}}` 引用：情境檔常被 commit，不能存 token；body 不遮（無法可靠判斷），改為提醒。
- 2026-11-26 不覆蓋既有檔、不做 `--force`：使用者自己改過的情境檔被蓋掉無法復原。
- 2026-11-26 已查證 `{{env}}` 會套用在 URL、header、body（`internal/engine/step.go` `renderRequest`），不需改 schema。
- 2026-11-26 敏感 header 判定：專案沒有既有的 header 關鍵字表（`maskedHeaders` 一律遮全部），改重用 `preflight.go` 的 `sensitiveQueryKeyParts`（auth、token、key、secret、session…），只另加 `cookie`；不另寫一份完整清單。
- 2026-11-26 env 變數名：除 `-` 外，所有非英數字元都換成 `_`（例 `access.token` → `RAMPLIO_Q_ACCESS_TOKEN`），確保是合法的 shell 變數名。header 整個值換成引用，環境變數要設成完整值（例 `Bearer xxx`）。
- 2026-11-26 新增一種不寫檔的情況（規格目標 3 沒列）：兩個不同的敏感值對到同一個變數名（如 `?token=a&token=b`、`X-Token` 與 `X_Token`）時拒絕，避免靜默共用同一個值。
- 2026-11-26 userinfo 與「目標檔已存在」在探測前就擋（不用等整輪探測才說不行），寫檔時仍以 `O_EXCL` 防競態；URL fragment 不會送到伺服器，存檔時拿掉。
- 2026-11-26 檔案權限 0600（body 可能含明文帳密）；YAML 開頭加註解列出要 export 的變數；寫檔前先用 `scenarios.Parse` 驗證產物，壞檔不落地。
- 2026-11-26 存檔提示只在「未中斷且 SafeLimit > 0」時印，提示內不帶網址與 header；單一入口也走 `runDiscover`，因此同樣會看到導向 `discover --save-scenario` 的提示（符合非目標：單一入口本身不接受旗標）。
- 2026-11-26 ~~已知限制：原請求含 `{{` 時 run 會失敗~~ → 第 5 段 MEDIUM-2 改為拒絕存檔（見下）。
- 2026-11-26 第 5 段 MEDIUM-2：網址、非敏感 header、body 原本就含 `{{` 時拒絕存檔，訊息只指出位置（網址／header 名稱／body），不印值；敏感值換成 env 引用後才檢查，不受影響。所有不依賴探測結果的檢查（`{{`、env 撞名、tolerance）在探測前用替代負載先建一次情境來判斷。另發現：含 `{{` 的請求連 discover 探測本身都會失敗（探測也走引擎的模板渲染，未知 token 直接報錯，SafeLimit 為 0），屬既有問題，不在本卡範圍，另開卡。
- 2026-11-26 第 5 段 MEDIUM-1／MEDIUM-3：存檔輸出與 YAML 註解註明「這些環境變數沒設時會送出空值」；網址路徑非空且不是 `/` 時，輸出提醒「網址路徑照原文保存，若路徑含秘密請手動改寫」。
- 2026-11-26 第 5 段 LOW-1：探測前檢查上層目錄存在且是目錄。
- 2026-11-26 第 5 段 LOW-3：tolerance 不到 1ms 時**拒絕**存檔（不進位成 1）：進位會悄悄放寬使用者指定的標準，拒絕讓使用者自己決定。1ms 以上仍以整數毫秒截斷（如 1500µs → 1ms），沒有改成小數。
- 2026-11-26 第 5 段 LOW-2 不處理（刻意取捨，負載卡在安全上限）：README 補一句偶發波動可能讓門檻失敗、需要餘裕可自行調低 `target_rps`。
- 2026-11-26 複審 LOW-A 接受：寫入型方法沒加 `--allow-write` 且 body 含 `{{` 時，現在先報模板錯誤、不送那 1 發預檢，與驗收情境 7「只送 1 發預檢」字面不同；少送一發只會更保守，不影響安全，接受現況。
- 2026-11-26 複審 LOW-B 不處理：只要出現 `{{` 就拒絕、不要求成對 `}}`，屬保守方向，誤擋時使用者可手動寫情境檔。

## History

- 2026-11-26 開卡，規格經使用者確認。
- 2026-11-26 第 2 段：先寫測試，放最小骨架（欄位、旗標、空的 `scenarioEnvName`）後跑完成檢查為紅，失敗原因皆符合預期（沒產檔／沒擋／沒提示／init 仍印 generate_sessions.sh；情境 7 與「SafeLimit 0 不提示」原本就成立故為綠）。實作後完成檢查 15 個測試全綠（16.7s）。
- 2026-11-26 第 3 段：`go test -race ./...` 16 個套件全 ok；`go build ./...` ok；`go vet ./...` ok；`gofmt -l` 無輸出；`golangci-lint` 本機未安裝，待 CI 確認。端到端：mock-server（20ms）＋`discover --max-rps 20 --save-scenario /tmp/s.yaml` 產出檔案無憑證原文、權限 0600；`validate` 通過；`run --scenario` 跑滿 2m30s、2,696 次 0 失敗、exit 0；縮短版把 `p99_ms` 改 1 後 exit 1（門檻有判定）。
- 2026-11-26 commit：3926d11（feat：--save-scenario）、03befb4（fix：init 說明）、e9938e0（docs：README／getting-started／CHANGELOG）。
- 2026-11-26 第 4 段獨立審查：無 CRITICAL／HIGH；MEDIUM-1～3、LOW-1／3／4 進第 5 段，LOW-2 不處理。
- 2026-11-26 第 5 段第 1 輪：先加測試，跑出預期的紅（`{{` 四種情況都只得到「安全上限為 0」、輸出沒有空值／路徑提醒、上層目錄不存在時 server 收到請求、tolerance 500µs 沒報錯）。LOW-4 測試（PASS 一級後才中斷）因既有中斷判斷已涵蓋而一開始就綠；暫時拿掉中斷判斷後確認轉紅（`An error is expected but got nil`），再還原。修正後完成檢查全綠（21.5s）；`go test -race ./...` 16 個套件全 ok（只有 cmd/ramplio 有改動，其餘為 cache）；`go build ./...`、`go vet ./...` ok；`gofmt -l` 無輸出。
- 2026-11-26 第 5 段 commit：21cb258（fix：六項修正與測試）、f7289c3（docs：README）。
- 2026-11-26 第 4 段複審：無 CRITICAL／HIGH，第 5 段第 1 輪各項確認修好；LOW-A 接受、LOW-B 不處理（見 Decisions）。待使用者同意 push 與開 PR。

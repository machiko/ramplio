# Ramplio

**輸入網址，回答你的服務撐得住多少人。** Ramplio 自動探測 HTTP 服務的容量上限、輸出白話容量報告，而且數字你能用內建 `mock-server` 注入已知延遲自行驗證——不必先學會 VU/RPS，也不必賭工具有沒有測準。也支援 YAML 多階段情境、登入流程、即時儀表板與分散式壓測，全部都在單一自包含的執行檔中。

```bash
ramplio https://example.com      # 給網址，回答：你的服務撐得住多少人
pbpaste | ramplio -              # 或在瀏覽器 DevTools 對請求按「Copy as cURL」，帶著 header／body 探測
```

## 快速導航

- 🚀 **[容量探測](#capacity-discovery)** — 給網址，得到「撐多少人」的白話答案
- 📖 **[核心概念](#核心概念)** — 理解 VU、RPS 和關鍵指標
- 🔬 **[量測公信力](#量測公信力)** — 數字憑什麼可信、如何自己驗證
- 🎯 **[基本用法](#基本用法)** — 30 秒啟動第一次測試
- ⚙️ **[進階功能](#進階功能)** — 分散式測試、自動探測、條件邏輯
- 🔐 **[認證與數據](#認證與數據)** — 登入測試、數據注入
- 📊 **[監控與集成](#監控與集成)** — Prometheus、CI/CD、Dashboard
- 📚 **[參考](#參考)** — CLI、架構、開發指南

---

## 功能特色

- **Capacity Discovery** — 一行指令自動探測網站最大吞吐量：從 5 RPS 遞增探測、偵測臨界點、輸出白話容量報告，適合 PM 與非技術人員直接使用
- **HAR Import** — 從 Chrome/Firefox DevTools 錄製直接產生 scenario.yaml，自動偵測登入步驟並注入 bearer auth
- **Per-step Metrics** — TUI 與 Dashboard 即時顯示每個步驟的 p50/p99/錯誤率，精確定位瓶頸步驟
- **單行指令或 YAML 驅動** — 直接指定 URL，或描述複雜的多階段測試情境
- **精確的百分位數** — 使用 HDR 直方圖計算 p50/p90/p95/p99，無近似誤差
- **可驗證的公信力** — `mock-server` 注入已知延遲分佈做 ground-truth 自我驗證；量測準不準是數學問題，不靠「跟 k6／JMeter 比一比」背書（見 [量測公信力](#量測公信力)）
- **Coordinated Omission 修正** — rate 模式並陳「服務延遲」與「壓力下實際延遲」，不會像 closed-loop 工具低報慢請求
- **開跑前 pre-flight 預檢** — 正式施壓前先單發探測目標可達性，連得上就印出連線分解（DNS／連線／TLS／首位元組），連不上直接給白話原因與建議
- **量測可信度判語** — 報告末尾自評產生器健康度（丟棄樣本比例、GC 暫停、尖峰 goroutine），直接告訴你這次數字能不能信
- **即時 TUI** — 終端機內的即時儀表板，顯示階段進度、RPS、延遲與各步驟指標。`run --url`、`run --rps`、`run --scenario`、分散式都有；輸出不是終端（CI、管線、重新導向）時自動關閉，也可用 `--no-tui` 關掉。跑到一半按 Ctrl-C（或收到 SIGTERM）會停止壓測、照樣印摘要與 `--output`／`--report` 等輸出，但不存自動執行紀錄、以 exit 1 結束（結果不完整，不做門檻判定）
- **即時網頁儀表板** — 由執行檔本身提供的 Vue 3 + Chart.js SPA，零額外設定
- **Prometheus 整合** — 將指標匯出至 Grafana 或任何相容 Prometheus 的監控系統
- **CI 友善** — 閾值超標時以 exit code `1` 退出
- **DNS 快取** — 選擇性快取 DNS 查詢，讓延遲量測只反映應用程式本身的開銷
- **單一執行檔** — 無執行時期相依套件，無需額外的儀表板程序

---

## 安裝

**Homebrew(macOS,推薦):**

```bash
brew install machiko/tap/ramplio
```

**直接下載:** 從 [GitHub Releases](https://github.com/machiko/ramplio/releases) 下載對應平台的 binary(macOS arm64/amd64、Linux amd64/arm64、Windows amd64),解壓即用,無任何相依套件。

**Go 開發者:**

```bash
go install github.com/machiko/ramplio/v3/cmd/ramplio@latest
```

**從原始碼建置:**

```bash
git clone https://github.com/machiko/ramplio.git
cd ramplio
make install         # 編譯並安裝至 ~/.local/bin/ramplio(版本號自動帶入 git tag)
```

---

## 核心概念

本節幫助你理解 Ramplio 的兩種負載模式、關鍵指標與負載設定。

### 理解 VU 與 RPS：選對測試方式

Ramplio 支援兩種負載模式。選對才能測出有意義的結果。

#### **VU（虛擬使用者）模式**

模擬**真實用戶的獨立行為**：每個 VU 在自己的 goroutine 中反覆執行 scenario loop，包括思考時間、重試、條件跳轉等。

```
3 個 VU，每個 scenario 耗時 10 秒
→ 實際 RPS = 3 × (requests_per_loop / 10s)
```

**適用場景：**
- Web 應用、移動 App（用戶邊看邊點）
- 需要模擬真實用戶行為
- 想看「100 人同時在線會怎樣」

#### **RPS（Requests/Sec）模式**

發送**固定吞吐率**的請求流，依固定節奏排定每個請求的應送時刻（產生器落後時立刻補送、排程不滑動），不受思考時間影響。產生器短暫停頓（如 GC）後會一次補送積欠的請求，瞬間速率可能短暫高於設定，平均維持在設定值；若實際送出低於排程的 95%，報告的量測信心會標示並列出「排程 N 筆、實際 M 筆」。

```
100 RPS 無論 VU 數量或思考時間，永遠是固定 100 req/sec
```

**適用場景：**
- API 後端、Webhook 推送
- 下游系統在乎吞吐量，不在乎邏輯用戶數
- 已知真實吞吐量，直接測試該速率

#### **不會失真嗎？**

**不會。VU 更真實。** 例子：

| 場景 | VU 設定 | 實際 RPS | 為何不用 RPS 直測 |
|------|:------:|:--------:|------------------|
| 100 人同時瀏覽，每 30 秒點一次 | 100 VU | ~16 RPS | 直接 100 RPS 會讓所有人狂點，不現實 |
| API 網關已知高峰 5000 RPS | - | 5000 RPS | VU 模式測不出「實際吞吐」的穩定性 |

### 關鍵指標解讀

| 指標 | 含義 | 實戰解讀 |
|------|------|---------|
| **P95/P99 Latency** | 95%/99% 的請求在此時間內完成 | P99 > 1s 代表 1% 的用戶感覺「卡」；P95 與 P99 差距大 = 有長尾延遲（GC/IO/swap 瓶頸） |
| **Error Rate** | 失敗請求的百分比 | 1% 在 10,000 RPS = 100 req/sec 失敗；需對標業務容忍度（銀行轉帳 < 0.1%，內容推薦可接受 5%） |
| **Throughput (RPS)** | 實際每秒請求數 | 實際 RPS 遠低於目標 = 到達瓶頸；RPS 開始下降 = 系統飽和 |
| **Active VU** | 當前運行中的虛擬用戶 | 看趨勢：穩定增長 = 正常 ramping；突然斷崖 = circuit breaker 觸發 |

### 實戰：如何設定合理負載？

### **第一步：判斷你要測什麼**

```yaml
# 方案 1：基於用戶數（Web 應用）
stages:
  - duration: 1m
    target: 50    # 50 人同時在線

steps:
  - name: Browse
    url: /api/products
    pause: "2000ms"  # 平均思考時間 2 秒
    
# 預期 RPS = 50 × (requests_per_step / 2000ms)
```

```yaml
# 方案 2：基於吞吐量（API 後端）
stages:
  - duration: 1m
    target_rps: 5000   # 固定 5000 req/sec，依固定節奏均勻送出
```

### **第二步：驗證設定**

```bash
ramplio run --scenario my.yaml --dashboard
# 看 Active VU 和 RPS 是否符合預期

# 例：50 VU 應該產生 ~16 RPS（50 × 5 req / 30 sec）
# 如果實際只有 10 RPS，代表有超時或慢查詢拉長了迴圈時間
```

### **第三步：對標真實數據**

查你的生產環境指標：
```
高峰期同時在線用戶：100
平均 RPS：1500

→ 平均每個用戶 15 RPS
→ 平均迴圈耗時 ~100ms
→ 設 VU=100，pause 或 request 總耗時控制在 100ms
```

---

## 量測公信力

壓測工具的價值建立在一個前提上：**它報出來的數字是可信的**。Ramplio 不要你「相信我們」，而是讓量測準確度變成可驗證的數學問題。完整說明見 **[docs/accuracy.md](docs/accuracy.md)**。

### 三根支柱

| 支柱 | 本質 |
|------|------|
| **方法論正確性** | 量測在「該送的時間」起算，消除 Coordinated Omission |
| **Ground-truth 自我驗證** | 對已知延遲分佈施壓，量到的百分位必須吻合 |
| **量測透明度** | 看得見量了什麼、工具自身有沒有當瓶頸 |

> 為什麼不靠「跟 k6／JMeter 比一比」？對照知名工具只是最弱的佐證——你的公信力會寄生在別人工具的正確性上，若兩者用同樣方式量錯（例如都有 Coordinated Omission），對照起來「一致」卻一致地錯。

### Ground-truth 自我驗證

量到的百分位只可能 ≥ 注入值（多了本機往返），絕不可能低於——若低於就代表量測有 bug。這是純粹的數學，不靠跟其他工具比對。

**最簡單：一行自證。** `ramplio verify` 自動起一個注入已知延遲的內建目標、施壓、比對、給白話判語：

```bash
ramplio verify
```

```
  量測自證 — 對已知延遲分佈施壓，反推 Ramplio 量得準不準
  注入分佈：固定 50ms    施壓：10 VU × 3s    容差：±20ms

  量測結果（注入值 → 量到值）
    p50            50ms → 51ms    ✓
    p99            50ms → 54ms    ✓

  ✓ 量測準確：所有百分位都落在注入值 +0~20ms 內。
```

失準時以 exit code `1` 退出，可放進 CI 當作「這版 Ramplio 沒把量測改壞」的回歸閘門。

**想自訂分佈？** 用內建 `mock-server` 注入確定性延遲，再手動施壓比對：

```bash
# 固定延遲：量到的所有百分位都應 ≈ 50ms
ramplio mock-server --latency 50ms &
ramplio run --url http://localhost:8080 --rps 200 --duration 20s

# 雙峰分佈：10% 慢（200ms）、其餘快（10ms）
# 量到的 p50 應 ≈ 10ms，p95/p99 應 ≈ 200ms（證明 HDR 能分離尾端延遲）
ramplio mock-server --latency-fast 10ms --latency-slow 200ms --slow-pct 10 &
ramplio run --url http://localhost:8080 --rps 200 --duration 30s
```

### Coordinated Omission 修正（rate 模式）

closed-loop 工具在系統變慢時會「自動」放慢送出速度，於是最該被記錄的慢請求根本沒送出，量到的延遲遠低於使用者實際經歷。Ramplio 的 rate（`--rps`）模式以 dispatcher 按目標速率排定每個請求的「應送時間」，並分兩條並陳：

| 數字 | 從何時起算 | 意義 |
|------|-----------|------|
| **服務延遲** | worker 實際送出 | 伺服器處理一個請求要多久 |
| **壓力下延遲** | 該請求「排定要送」的時間 | 使用者從點擊到看到回應實際要等多久 |

報告的**整體結論採用壓力下 p99**，因為那才是使用者真正的體驗。產生器有餘裕時兩者相等，不會無中生有地灌水；VU 模式沒有「排定時間」概念，因此不套用修正也不顯示，誠實標明適用範圍。

```bash
ramplio mock-server --latency 50ms &
# 目標 500 RPS，但伺服器每次 50ms：觀察壓力下 p99 ≫ 服務 p99
ramplio run --url http://localhost:8080 --rps 500 --duration 30s
```

### 量測透明度

- **連線分解（pre-flight）**：開跑前單發預檢若連得上，用 `httptrace` 拆出 DNS／TCP 連線／TLS 握手／首位元組（TTFB）。此 trace 只加在診斷請求上，壓測 hot path 不付出任何成本，因此不干擾正式量測。
- **量測可信度判語**：報告末尾自評產生器健康度——丟棄樣本比例（channel 滿載）、GC 暫停佔比、尖峰 goroutine 數——判語分 高／中等／偏低 三級，偏低時建議降載或分散到多節點重測。

---

## 基本用法

第一次使用？直接執行 `ramplio`（不帶任何參數）會顯示引導前門，列出三條最常用的路徑，不用先查文件：

```bash
ramplio                    # 顯示引導：開視覺面板 / 建立情境 / 快速測一次
```

### 單行指令

```bash
ramplio run --url https://httpbin.org/get --vus 20 --duration 30s
```

```
Running 20 VUs for 30s → GET https://httpbin.org/get

測試結果
────────
  總請求數：           2840
  測試時長：           30.00s
  每秒請求：           94.6

延遲分佈
────────
  最短：               85ms
  平均：               210ms
  p50：                190ms
  p90：                340ms
  p95：                410ms
  p99：                520ms
  最長：               980ms

回應狀態
────────
  成功 (2xx)：         2840 (100.0%)
  失敗：               0 (0.0%)

────────────────────────────────────────

測試結果說明
──────────
  整體結論：✓ 網站很健康，可以放心上線

  反應速度  ✓ 普通
      99% 的人點下去後，520 毫秒內就看到回應。
      （使用者開始能感覺到一點點等待）

  穩定度　  ✓ 完美
      這次共試了 2,840 次，沒有任何一次失敗。

  承受能力  每秒約 95 個請求
      錯誤率 0，代表這個壓力下軟體還有餘裕。

  一句話總結：整體來說，網站又快又穩，可以放心。
```

> 同一份白話解讀（整體結論／反應速度／穩定度／承受能力）由單一來源產生，
> 終端機、JSON、HTML 報告與網頁儀表板的用語完全一致。

### 情境檔案

```yaml
# smoke.yaml
name: API smoke

stages:
  - duration: 30s
    target: 50      # 漸升至 50 VU
  - duration: 60s
    target: 50      # 持載
  - duration: 30s
    target: 0       # 漸降

steps:
  - name: GET health
    method: GET
    url: https://api.example.com/health
    assertions:
      status: 200

  - name: POST order
    method: POST
    url: https://api.example.com/orders
    headers:
      Content-Type: application/json
    body: '{"item":"widget","qty":1}'
    assertions:
      status: 201

thresholds:
  error_rate_pct: 1.0
  p99_ms: 800
```

```bash
ramplio run --scenario smoke.yaml
```

執行多步驟情境時，TUI 會在整體指標下方顯示 per-step 表格：

```
  Step                                Total       p50       p99   Err%
  ──────────────────────────────────────────────────────────────────────
  GET health                           3240      12ms      48ms    0.0%
  POST order                           3240      85ms     340ms    0.2%
```

---

## 進階功能

### 分散式測試（突破單進程限制）

**使用情境：** 超過 10,000 VU 或需要避免單一機器的 TCP 連線數限制時，可以在多個進程間分散負載。

**一、啟動 Worker（每個終端一條指令）**

```bash
# 終端 1：Worker A 監聽 :7700
ramplio worker --addr :7700

# 終端 2：Worker B 監聽 :7701
ramplio worker --addr :7701

# 終端 3：Worker C 監聽 :7702
ramplio worker --addr :7702
```

**二、執行分散式測試**

```bash
# 用 --worker 旗標指定 worker 位址（可重複）
ramplio run --scenario smoke.yaml \
  --worker localhost:7700 \
  --worker localhost:7701 \
  --worker localhost:7702
```

Coordinator 自動做到：
- ✓ 檢查所有 Worker 健康狀態
- ✓ 運行 Setup 步驟（執行一次，集中於 Coordinator）
- ✓ 逐 stage 按整數餘數法切分 VU 與 RPS，每個 stage 的叢集總和恰等於情境宣告值（e.g. 100 VU → 3 Worker 各 34、33、33；`target_rps: 40` → 2 Worker 各 20）
- ✓ 開跑前確認每個 Worker 支援逐 stage 份額；版本過舊的 Worker 會被拒絕（否則它會獨自跑完整負載）
- ✓ 廣播情境給所有 Worker，每個 Worker 執行自己的一份
- ✓ 每秒輪詢所有 Worker 的即時指標，合并顯示在 TUI / Dashboard
- ✓ 執行 Teardown（同樣在 Coordinator）
- ✓ 合并所有 Worker 結果（加總、加權百分位數）

**重要：** 分散模式下，`setup` 和 `teardown` 只在 Coordinator 執行一次；實際負載由 Worker 並行承擔。

**注意：** `circuit_breaker` 在每個 Worker 各自計算——門檻以單一 Worker 承擔的流量判定，不是全叢集合計。N 個 Worker 時每個只看到約 1/N 的失敗（且任一成功請求就清空窗口），熔斷可能比單機晚、甚至永遠不觸發；觸發時也只停下該 Worker，其他 Worker 照常送負載。分散式下請依 Worker 數調降 `consecutive_failures`（約 ÷ N）。

### Capacity Discovery

不需要了解 VU 或 RPS 是什麼——直接告訴 Ramplio 網址，它自動幫你找出網站的最大承載量：

```bash
ramplio https://example.com                    # 單一入口：等同 discover --url
ramplio 'curl https://api.example.com/me -H "Authorization: Bearer …"'
pbpaste | ramplio -                            # 貼上 DevTools「Copy as cURL」
```

單一入口會自動判斷你給的是網址、curl、還是檔案（`.yaml` 會提示用 `run --scenario`、`.har` 會提示用 `import`），打錯字的子指令會給建議而不會開跑。

**寫入型方法的安全閥：** POST／PUT／PATCH／DELETE（包含 Chrome 複製出來、只有 `--data-raw` 沒寫 `-X` 的隱式 POST）預設**只送一發預檢**、印出結果就停下；確定要壓測請改用 `pbpaste | ramplio discover --curl - --allow-write`。終端輸出只顯示 header 名稱、值一律遮成 `***`，建議指令也不會把你的 token 印回畫面。

需要細部控制時用 `discover` 子指令：

```bash
ramplio discover --url https://example.com
ramplio discover --url https://api.example.com/me -H "Authorization: Bearer $TOKEN"
ramplio discover --url https://api.example.com/orders --method POST --body '{"sku":1}' \
  -H "Content-Type: application/json" --allow-write
pbpaste | ramplio discover --curl - -H "Authorization: Bearer $NEW_TOKEN"   # 明確旗標覆蓋 curl 的值
```

curl 支援 DevTools 產出的寫法與常見旗標：`-X`、`-H`、`-d/--data/--data-raw/--data-binary`、`--data-urlencode`、`--json`、`-G`、`-I`、`-b`、`-u/--user`（轉 Basic 認證）、`--oauth2-bearer`、`-A`、`-e`、`--url`，以及 `-sXPOST` 這類短旗標叢集；`--compressed`、`-s`、`-S`、`-k`、`-L`、`-i`、`-v`、`-f`、`-4`、`-6`、`--http1.1`、`--http2` 略過，`-o`、`-m`、`-w` 等只影響 curl 本身輸出的旗標會印警告後略過。**無法完整重現的寫法**（不認識的旗標、`-F` 表單、`-T` 上傳、`-d @檔案`、`-x` proxy、`X-HTTP-Method-Override` 這類方法覆寫 header 等）一律視同寫入：沒加 `--allow-write` 就只送 1 發預檢。警告只列旗標名稱，網址裡的密碼、`#` 後面的 fragment，以及名稱含 `token`、`secret`、`key`、`sig`、`auth`、`pass`、`session`、`code` 等字樣的參數，在畫面上與 `--save-baseline` 存檔裡都會遮成 `***`。

也可以從網頁儀表板的「⚡ 探測上限」分頁直接操作，包含即時探測進度表格與容量報告卡片，PM 不需要技術背景也能讀懂結果。

**常用選項：**

```bash
# 使用更嚴格的回應時間標準
ramplio discover --url https://api.example.com --tolerance 500ms

# 探測到更高的 RPS
ramplio discover --url https://example.com --max-rps 1000

# 縮短每個探測點的時間（較快但精準度略低）
ramplio discover --url https://example.com --probe-duration 10s
```

### HAR Import

從瀏覽器錄製直接產生 scenario.yaml，無需手寫：

**錄製步驟（Chrome/Edge）：**
1. DevTools → Network 分頁
2. 執行要壓測的完整操作流程（登入、查詢、下單…）
3. Network 面板空白處右鍵 → **Save all as HAR with content**

```bash
# 輸出到 stdout（預覽）
ramplio import recording.har

# 儲存到檔案
ramplio import recording.har -o scenario.yaml

# 不過濾靜態資源
ramplio import recording.har --no-filter

# 自訂測試時長
ramplio import recording.har -o scenario.yaml -d 5m
```

Import 會自動：
- **過濾靜態資源**（.js/.css/圖片/字型），只保留 API 呼叫
- **偵測登入步驟**（評分系統），自動加入 `capture: token` 提取 JWT
- **注入 bearer auth**，後續步驟的原始 token 替換為 `{{capture.token}}`

產生的 scenario.yaml 可直接執行：

```bash
ramplio validate --scenario scenario.yaml
ramplio run --scenario scenario.yaml
```

### 條件邏輯：if 欄位與 AND/OR 運算

`if` 欄位用來控制步驟是否執行，支援複雜的布林邏輯（AND、OR、括號優先級）。典型場景是根據前一步驟的回應結果，決定是否執行後續步驟。

**基本語法：**

```yaml
steps:
  - name: Step A
    method: GET
    url: https://api.example.com/status
    capture:
      status: $.data.status
      version: $.data.version

  # 簡單比較：只有當 status 等於 "ok" 時執行
  - name: Step B (only if status ok)
    method: GET
    url: https://api.example.com/data
    if: 'capture.status == "ok"'
    assertions:
      status: 200
```

**支援的比較運算子：**

| 運算子 | 說明 | 範例 |
|--------|------|------|
| `==` | 等於 | `capture.code == "200"` |
| `!=` | 不等於 | `capture.error != ""` |
| `<` | 小於（數值） | `capture.latency < 1000` |
| `<=` | 小於等於 | `capture.count <= 100` |
| `>` | 大於 | `capture.price > 0` |
| `>=` | 大於等於 | `capture.retry_count >= 3` |

**AND/OR 與括號優先級：**

```yaml
  # AND：所有條件都為真
  - name: Call authenticated endpoint
    method: GET
    url: https://api.example.com/protected
    if: 'capture.token != "" AND capture.token_type == "Bearer"'

  # OR：至少一個條件為真
  - name: Fallback auth path
    method: POST
    url: https://api.example.com/refresh
    if: 'capture.token == "" OR capture.token_expired == "true"'

  # 混合 AND/OR（使用括號）
  - name: Add to cart
    method: POST
    url: https://api.example.com/cart/add
    if: 'capture.stock != "0" AND (capture.price < "100" OR capture.on_sale == "true")'
```

**完整範例：** 見 `testdata/simple-if-example.yaml`、`conditional-flow.yaml`、`complex-conditions.yaml`

---

## 認證與數據

### 需要登入的網站

對有登入保護的網站做壓力測試。

**情境 A：測試環境可停用 CAPTCHA（最簡單）**

```yaml
steps:
  - name: POST /auth/login
    method: POST
    url: https://staging.example.com/auth/login
    headers:
      Content-Type: application/json
    body: '{"email":"loadtest@example.com","password":"testpass"}'
    assertions:
      status: 200
    capture:
      jwt: "$.access_token"

  - name: GET /dashboard
    method: GET
    url: https://staging.example.com/dashboard
    auth:
      bearer: "{{capture.jwt}}"
    assertions:
      status: 200
```

**情境 B：生產環境有真實 CAPTCHA（Session Pool 方法）**

預先產生 `sessions.csv`，在測試中注入：

```yaml
name: 登入後功能壓測

vars_from:
  file: sessions.csv
  mode: sequential

steps:
  - name: GET /dashboard
    method: GET
    url: "https://example.com/dashboard"
    headers:
      Cookie: "{{data.session_cookie}}"
    assertions:
      status: 200
```

```bash
ramplio run --scenario testdata/post-login-load.yaml
```

### `capture` 欄位與模板

從 response 中提取值，供後續步驟以 `{{capture.key}}` 引用：

| 表達式 | 說明 | 範例 |
|--------|------|------|
| `$.path.to.value` | JSONPath（從 response body 提取） | `$.data.token` |
| `header:Name` | 從 response header 提取 | `header:X-Request-Id` |
| `cookie:name` | 從 `Set-Cookie` 提取特定 cookie | `cookie:session` |
| `regex:(pattern)` | 正規表達式（第一個 capture group） | `regex:token=([a-z0-9]+)` |

```yaml
steps:
  - name: POST /auth/login
    method: POST
    url: "https://api.example.com/auth/login"
    body: '{"username":"user1","password":"pass123"}'
    capture:
      auth_token: $.token
      token_type: $.type
    assertions:
      status: 200

  - name: GET /user/profile
    method: GET
    url: "https://api.example.com/user"
    headers:
      Authorization: "Bearer {{capture.auth_token}}"
    assertions:
      status: 200
```

---

## 監控與集成

### 即時網頁儀表板

```bash
# 啟動儀表板（預設 port 9999）
ramplio run --scenario smoke.yaml --dashboard

# 指定 port
ramplio run --scenario smoke.yaml --dashboard --dashboard-port 8080
```

開啟後從首頁「你想做什麼？」選擇入口，整個操作以頂部 **① 設定 → ② 執行中 → ③ 結果** 進度列指引，隨時知道走到哪一步：

| 入口 | 說明 |
|------|------|
| 🧙 帶我設定（推薦） | PM 精靈：回答幾個業務問題，自動轉換成測試配置 |
| 🚀 快速測試 | 直接填入 URL，自訂並發數與測試時間 |
| 📂 上傳場景 | 上傳 HAR 或 YAML 情境檔，支援多步驟與登入流程 |
| ⚡ 探測上限 | 自動探測網站最大吞吐量 |

執行中與結果畫面的關鍵指標（p99、錯誤率）都附帶白話說明，且用語與終端機輸出完全一致。

結果頁還有兩張進階卡片（v3.1）：

- **容量回歸比較** — 在設定表單上傳 `--save-baseline` 產生的 JSON 快照，run 結束自動比較並顯示每個指標的持平/改善/退步（判定與 `ramplio compare` 同一套雙門檻容差，量測可信度警告強制顯示）
- **目標系統觀測** — 伺服器以 `ramplio run --dashboard --observe "jaeger://…?service=X"` 啟動時，rate 模式 run 結束後自動做 trace 瓶頸關聯，白話指出是哪個環節先垮（誠實邊界與 CLI 相同：樣本不足不猜測、截斷一律可見）

### Prometheus 整合

```bash
ramplio run --scenario smoke.yaml --prometheus :9100
```

公開的指標：`ramplio_requests_total`、`ramplio_errors_total`、`ramplio_error_rate_pct`、`ramplio_rps`、`ramplio_latency_p50/p90/p99_ms`、`ramplio_active_vus` 等。

### OpenTelemetry 整合

```bash
# 測試結束時把最終彙總指標推送到 OTel collector(OTLP/HTTP)
ramplio run --url https://example.com --rps 200 -d 30s --sink otel://localhost:4318

# 讓每個壓測請求帶 W3C traceparent,APM 就能標記出哪些流量來自壓測
ramplio run --url https://example.com --rps 200 -d 30s --trace-context
```

匯出的指標:`ramplio.requests.count`、`ramplio.error_rate_pct`、`ramplio.throughput_rps`、`ramplio.latency.p50/p90/p95/p99_ms`、`ramplio.latency.corrected_p99_ms`(rate 模式)等,並附 `scenario` 標籤。

> `--trace-context` 預設關閉:逐請求注入在產生器極限吞吐下有約 5% 成本,只在需要與 APM 做流量關聯時開啟。

### 瓶頸關聯(--observe)

不只告訴你撐不住,還告訴你**是哪裡先垮**。壓測結束後自動拉取目標系統的 trace,比較「低負載基準窗 vs 滿載臨界窗」的 per-operation 延遲,白話指出退化最嚴重的環節:

```bash
# Jaeger 後端
ramplio run --url https://example.com --rps 200 -d 60s \
  --observe "jaeger://localhost:16686?service=checkout"

# Grafana Tempo 後端(tempos:// 走 https)
ramplio run --url https://example.com --rps 200 -d 60s \
  --observe "tempo://localhost:3200?service=checkout"

# CI 場景:觀測不可信(拉取失敗/樣本截斷/關聯不足)時視同失敗
ramplio run --url https://example.com --rps 200 -d 60s \
  --observe "jaeger://localhost:16686?service=checkout" --strict-trust
```

```
目標系統觀測(trace 關聯)
──────────────────────────
  結論:瓶頸指向 SELECT orders——p95 從 10ms 惡化到 90ms(9.0 倍)。
  其次:GET /users 12ms → 13ms(1.1 倍)
  註:基準窗取自爬升前段,若目標系統有明顯冷啟動效應,退化倍率可能被低估。
```

**誠實邊界(刻意設計):**
- 僅 rate 模式(`--rps`)支援——比較窗口出自負載輪廓(爬升前半=基準、持平段=臨界)
- trace 樣本不足時回報「關聯不足」,**不猜測瓶頸**;被排除的 operation 一律列名
- 全部 operation 等幅變慢時回報「疑似資源飽和」,不硬指單點
- 已知限制:基準窗含冷啟動效應時倍率可能被低估;慢路徑「佔比」上升但峰值不變的退化不反映於 p95
- 歸因準確度可自證:對已知瓶頸分佈做關聯,結論必須指向注入的瓶頸(見整合測試)

### CI 整合

閾值讓 Ramplio 成為 CI 流程的效能門禁：

```yaml
# .github/workflows/perf.yml
- name: 壓力測試
  run: ramplio run --scenario testdata/smoke.yaml
```

Exit code `0` → 閾值通過；Exit code `1` → 閾值超標。

---

## 參考

### CLI 參數說明

```
ramplio run [flags]

Flags:
  -u, --url string            目標 URL（與 --scenario 互斥）
  -s, --scenario string       YAML 情境檔案路徑
      --vus int               虛擬使用者數量（預設 1，與 --rps 互斥）
      --rps int               目標每秒請求數——rate 模式（與 --vus 互斥）
  -d, --duration string       測試時長（預設 "30s"）
      --method string         HTTP 方法（預設 "GET"）
  -H, --header stringArray    HTTP 標頭，可重複使用
      --body string           請求 body
  -o, --output string         結果檔案（JSON 或 JUnit XML）
      --timeout string        單次請求逾時
      --no-preflight          略過開跑前的可達性預檢
      --dns-cache             快取 DNS 查詢
      --dashboard             開啟即時網頁儀表板
      --dashboard-port int    儀表板 port（預設 9999）
      --prometheus string     Prometheus 指標端點
      --worker stringArray    分散式測試：Worker 位址
```

```
ramplio <網址 | 'curl …' | ->

不寫子指令直接給目標：網址或 curl 走容量探測；寫入型方法只送一發預檢。
```

```
ramplio discover [flags]

Flags:
  -u, --url string             目標 URL（與 --curl 二擇一）
      --curl string            DevTools「Copy as cURL」整段；給 - 從 stdin 讀
  -H, --header stringArray     額外 header，可重複（覆蓋 curl 解析出的同名 header）
      --method string          HTTP 方法（預設 GET；有 --body 時預設 POST）
      --body string            請求內容
      --allow-write            允許對 POST/PUT/PATCH/DELETE 壓測（否則只送一發預檢）
      --tolerance string       可接受的 p99 時間（預設 "2s"）
      --max-rps int            最高探測速率（預設 500）
      --probe-duration string  每個探測點的時長（預設 "15s"）
```

```
ramplio import <recording.har> [flags]

Flags:
  -o, --output string     輸出檔案（預設 stdout）
      --no-filter         不過濾靜態資源
  -d, --duration string   測試時長（例如 "5m"）
```

```
ramplio mock-server [flags]

注入確定性延遲的本機測試標的，用於 ground-truth 量測驗證（見 量測公信力）。

Flags:
      --port int             監聽 port（預設 8080）
      --latency string       固定的每請求模擬延遲（例如 5ms、10ms）
      --latency-fast string  雙峰：快帶延遲（多數請求）
      --latency-slow string  雙峰：慢帶延遲（尾端）
      --slow-pct int         雙峰：多少 % 的請求走慢帶（0–100）
```

```
ramplio verify [flags]

一鍵自證：對注入已知延遲的內建目標施壓，比對量到的百分位與注入值，
給白話判語。準確 exit 0、失準 exit 1（見 量測公信力）。

Flags:
      --latency string       固定注入延遲（預設 50ms；與雙峰互斥）
      --latency-fast string  雙峰：快帶延遲（多數請求）
      --latency-slow string  雙峰：慢帶延遲（尾端）
      --slow-pct int         雙峰：多少 % 的請求走慢帶（預設 10）
      --tolerance string     可接受的量測誤差（預設 "20ms"）
      --duration string      施壓時長（預設 "3s"）
      --vus int              並發虛擬使用者數（預設 10）
```

### 儲存與重新載入結果

每次 `run`／`discover` 跑完都會自動存一筆執行紀錄到目前目錄的 `.ramplio/runs/`（保留最近 50 筆；裡面自動放 `.gitignore`，不會被 commit），結尾也會自動跟「上一次同目標、同設定」比較 p99、錯誤率、吞吐量。這段比較只是參考，不影響結束碼。

```bash
ramplio run --url https://api.example.com/health   # 跑兩次同樣的指令
ramplio compare                                    # 比較最近一筆與同目標的前一筆（退步時 exit 1）
ramplio report                                     # 用最近一筆 run 紀錄產生 report.html
```

- 同目標的認定：模式、HTTP 方法、網址、header＋body、負載參數（`--vus`／`--rps`、`-d`；discover 是 `--tolerance`、`--max-rps`、`--probe-duration`）都相同。header＋body 只存 sha256 前 8 碼，不存原文；header 順序與名稱大小寫不影響，但 token 換了就會當成新目標，從第一筆重新累積。
- 情境檔模式（`--scenario`）只看檔案路徑：改了情境檔內容但路徑不變，仍會跟上一次比較。
- 壞掉或由較新版本 ramplio 寫的紀錄檔會被略過（印警告），但不會被自動刪除；檔名排在最新的壞檔要自己刪。
- **雜湊不等於加密**：header＋body 的雜湊沒有加鹽，只取 8 碼，短 token、PIN 這類內容可以被離線窮舉還原。不要把 `.ramplio/` 紀錄或 baseline 檔貼到公開的地方。
- `.gitignore` 只在紀錄目錄是 ramplio 自己建的（或裡面只有 `runs/`）時自動放。如果 `RAMPLIO_HISTORY_DIR` 指到已經有其他檔案的目錄（例如專案根目錄），不會寫 `.gitignore`，只在第一次存檔時提示你自己把 `runs/` 加進去。
- 加 `--no-history` 不留存；設 `RAMPLIO_HISTORY_DIR` 可以改存放位置。存檔失敗只印警告，不影響結果與結束碼。
- 紀錄檔裡的網址會遮罩帳密與 token。紀錄檔是 baseline 格式的超集，也可以直接 `ramplio compare .ramplio/runs/a.json .ramplio/runs/b.json`。

想自己管檔名時，舊的旗標照樣能用：

```bash
ramplio run --scenario smoke.yaml --output results.json
ramplio report --input results.json
```

### 開發與構建

```bash
make build           # 編譯 → ./bin/ramplio
make install         # 安裝至 ~/.local/bin/ramplio
make test            # 執行所有測試
make race            # 以 -race 執行測試
make cover           # 覆蓋率報告
make lint            # golangci-lint
make dashboard       # 啟動儀表板（port 9999）
```

執行單一測試：

```bash
go test ./internal/engine/... -run TestRampUp -v
```

### 架構總覽

```
ramplio/
├── cmd/ramplio/       # cobra CLI 指令
├── internal/
│   ├── engine/          # VU 池與多階段調度
│   ├── protocols/       # HTTP executor、DNS 快取
│   ├── metrics/         # HDR 直方圖、per-step 分桶
│   ├── scenarios/       # YAML 解析、驗證
│   ├── reporter/        # 終端、JSON、JUnit、TUI、Prometheus
│   ├── importer/        # HAR 解析
│   ├── distributed/     # Coordinator、Worker
│   └── dashboard/       # WebSocket + Vue 3 SPA
└── testdata/            # 測試用 YAML 與 HAR
```

### 文件與資源

| 文件 | 說明 |
|------|------|
| [docs/scenario-schema.md](docs/scenario-schema.md) | 完整 YAML schema |
| [docs/accuracy.md](docs/accuracy.md) | 量測準確度與公信力：三根支柱、ground-truth 驗證、Coordinated Omission 修正 |
| [docs/roadmap.md](docs/roadmap.md) | Milestone 計畫 |
| [CHANGELOG.md](CHANGELOG.md) | 版本異動記錄 |

---

## 授權條款

MIT

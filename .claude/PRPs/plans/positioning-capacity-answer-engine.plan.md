# Plan: 定位收斂為「容量回答機」

## Summary
把 Ramplio 從「樣樣有、樣樣不夠尖」收斂成單一尖銳定位：**給網址 → 回答你的服務能撐多少人，而且數字你能自己驗證**。本計畫只調整產品「主推 vs 淡化」的表層（CLI 前門、`discover` 體驗與繁中化、dashboard 前門、README hero），**不刪除任何既有功能、不改演算法**。

## User Story
作為一個要做容量規劃或上線前壓測、但不想變成壓測專家的後端工程師／PM／小團隊，
我想要輸入一個網址就得到「能撐多少人、何時飽和」的白話答案，並能親手驗證這數字可信，
這樣我不必先學會 VU/RPS，也不必在「為什麼不用 k6」的問題上糾結。

## Problem → Solution
**現狀**：`ramplio` 前門把 `discover` 排在三條路徑之外；dashboard 把「探測上限」降級成底部小連結；`discover` 容量報告與探測列**全是英文**（與繁中白話定位矛盾）；README 以平鋪的功能列表開場，沒有單一故事。
**目標**：`discover`（容量探測）成為 CLI 前門、dashboard 前門、README hero 的**第一主推**；容量報告全面繁中白話化並掛上 ground-truth 信任背書；其餘指令（run / import / 分散式 / DSL）保留但退居次要。

## Metadata
- **Complexity**: Medium
- **Source PRD**: N/A（源自 `/ecc:prp-plan` 對話，定位選項＝「容量回答機」）
- **PRD Phase**: N/A（對應評估建議的 P0）
- **Estimated Files**: 6 改 + 1 新測試 ≈ 7

---

## UX Design

### Before（CLI 前門 `ramplio` 無參數）
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Ramplio — 開發者優先的 HTTP 壓力測試工具
  對網站或 API 施加可調負載，即時量測效能並產生白話報告
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  三條最常用的路徑：

  ① 開視覺面板（最推薦，全程點選操作）
    ramplio run --dashboard
  ② 引導式建立測試情境（問答產生 YAML）
    ramplio init
  ③ 快速測一個網址一次
    ramplio run --url https://example.com
```

### After（CLI 前門 `ramplio` 無參數）
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  Ramplio — 你的服務撐得住多少人？
  給網址，自動探測容量上限並給白話答案；數字你能自己驗證
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  從這裡開始：

  ① 探測容量上限（最推薦，一行回答撐多少人）
    ramplio discover --url https://example.com
  ② 開視覺面板（全程點選操作）
    ramplio run --dashboard
  ③ 直接壓測一個網址
    ramplio run --url https://example.com

  進階：YAML 多階段情境、登入流程、分散式 → ramplio --help
```

### Before（`discover` 容量報告）
```
  ┌──────────────────────────────────────────────┐
  │  Capacity Report                             │
  ├──────────────────────────────────────────────┤
  │  Safe limit:     ~200 req/sec                │
  │  Breaking point: ~300 req/sec                │
  ├──────────────────────────────────────────────┤
  │  What this means:                            │
  │  Your site handles about 200 requests per    │
  │  second comfortably. Above that, response    │
  │  times climb beyond 2s.                      │
  └──────────────────────────────────────────────┘
```

### After（`discover` 容量報告）
```
  ┌──────────────────────────────────────────────┐
  │  容量報告                                     │
  ├──────────────────────────────────────────────┤
  │  安全上限：    每秒約 200 個請求              │
  │  臨界點：      每秒約 300 個請求              │
  ├──────────────────────────────────────────────┤
  │  這代表什麼：                                 │
  │  你的服務每秒約能穩定處理 200 個請求。        │
  │  超過後回應時間會拉長到 2s 以上。             │
  │                                              │
  │  這個數字怎麼信？工具本身的量測準確度可用     │
  │  ramplio mock-server 注入已知延遲自行驗證。   │
  └──────────────────────────────────────────────┘
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| `ramplio`（無參數） | 三路徑首推 `run --dashboard` | 首推 `discover`，tagline 改容量問句 | `guidance.go` |
| `ramplio --help` 一行說明 | "Developer-first HTTP stress testing tool" | 容量定位（中英並存或繁中） | `main.go` rootCmd |
| `discover` 報告 | 全英文 | 全繁中白話 + ground-truth 信任背書一行 | `discover.go` |
| `discover` 探測列 | `5 rps ✓ p99=42ms errors=0.1%` | `每秒 5 個 ✓ p99=42ms 錯誤=0.1%` | `discover.go` |
| Dashboard 前門 | 探測上限＝底部小連結 | 探測上限＝主推卡片（推薦 badge） | `dashboard.html` |
| README 開場 | 功能特色平鋪列表 | 容量回答機 hero 故事 | `README.md` |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `cmd/ramplio/guidance.go` | 全 (1-47) | 前門 `printWelcome` / `printNextSteps`，要翻轉首推 |
| P0 | `cmd/ramplio/discover.go` | 91-159 | `printDiscoverProbe` / `printDiscoverReport`，英文→繁中白話的核心 |
| P0 | `cmd/ramplio/main.go` | 10-21 | rootCmd Short/Long 定位文案 |
| P1 | `internal/discover/prober.go` | 41-47, 174-186 | `DiscoverResult` 欄位（SafeLimit/BreakingPoint/Exhausted）與 classify 門檻，報告措辭須與之一致 |
| P1 | `internal/reporter/interpret.go` | 全 | 白話用語的單一來源；新報告措辭語氣須對齊（避免兩套口吻） |
| P1 | `internal/dashboard/templates/dashboard.html` | 1637-1662 | 前門卡片與 tagline；探測上限要升為 primary |
| P2 | `cmd/ramplio/guidance_test.go` | 全 | 前門測試模式（`bytes.Buffer` + `strings.Contains`），須同步更新斷言 |
| P2 | `README.md` | 1-46 | hero 與功能特色開場 |

## External Documentation
無需外部研究——全部使用既有內部模式（cobra command 文案、`io.Writer` 純函式輸出、現有測試風格）。

---

## Patterns to Mirror

### NAMING_CONVENTION（CLI 純函式輸出，吃 io.Writer 便於測試）
```go
// SOURCE: cmd/ramplio/guidance.go:22-27
func printNextSteps(w io.Writer, heading string, steps ...nextStep) {
	fmt.Fprintf(w, "%s\n", heading)
	for _, s := range steps {
		fmt.Fprintf(w, "\n  %s\n    %s\n", s.label, s.cmd)
	}
}
```

### REPORT_RENDER（容量報告現有框線繪製，保留結構只換字串）
```go
// SOURCE: cmd/ramplio/discover.go:114-130
func printDiscoverReport(result discover.DiscoverResult, tolerance time.Duration) {
	top := "  ┌" + strings.Repeat("─", reportWidth) + "┐"
	row := func(s string) { fmt.Printf("  │  %-*s│\n", reportWidth-2, s) }
	fmt.Println(top)
	row("Capacity Report")   // ← 改 "容量報告"
	...
	row(fmt.Sprintf("Safe limit:     ~%d req/sec", result.SafeLimit)) // ← 繁中化
}
```
> GOTCHA：`row()` 用 `%-*s` 對齊欄寬 `reportWidth-2`，但**中文字元寬度 ≠ ASCII**，`fmt` 以 rune 數對齊會讓含中文的列右框線錯位。見 Task 3 的對齊處理。

### PLAIN_LANGUAGE_TONE（白話用語單一來源的語氣基準）
```go
// SOURCE: internal/reporter/interpret.go（Interpret 產生的句子風格）
// 例："99% 的人點下去後，520 毫秒內就看到回應。"
// 容量報告新措辭須同此口吻：講「人 / 請求」，不講術語。
```

### TEST_STRUCTURE（前門／輸出測試）
```go
// SOURCE: cmd/ramplio/guidance_test.go:38-55
func TestPrintWelcome(t *testing.T) {
	var buf bytes.Buffer
	printWelcome(&buf)
	out := buf.String()
	for _, want := range []string{"Ramplio", "ramplio run --dashboard", ...} {
		if !strings.Contains(out, want) {
			t.Errorf("printWelcome output missing %q\n--- got ---\n%s", want, out)
		}
	}
}
```

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `cmd/ramplio/guidance.go` | UPDATE | 前門 tagline 改容量問句；首推 `discover` |
| `cmd/ramplio/main.go` | UPDATE | rootCmd Short/Long 改容量定位文案 |
| `cmd/ramplio/discover.go` | UPDATE | 報告與探測列繁中白話化 + ground-truth 信任背書；中文欄寬對齊 |
| `cmd/ramplio/guidance_test.go` | UPDATE | 斷言同步新前門文案 |
| `cmd/ramplio/discover_test.go` | CREATE | 報告繁中化的字串斷言（目前無此測試檔） |
| `internal/dashboard/templates/dashboard.html` | UPDATE | 探測上限升為 primary 卡片、tagline 對齊 |
| `README.md` | UPDATE | hero 改容量回答機故事，功能列表退居次要 |

## NOT Building
- **不刪除任何指令或功能**：run / init / import / validate / worker / 分散式 / DSL（if/loop/group）/ TLS 全部保留，只調整「主推 vs 淡化」。
- **不改 `discover` 探測演算法**（`prober.go` 的 ProbeSequence / classify / worker 數）——本計畫純定位與文案層。
- **不新增 `ramplio verify` 子指令**——那是 P1（降低信任門檻）的範疇，另案處理。
- **不動 `interpret.go` 的判語邏輯**——只借用其語氣，不改其門檻或輸出。
- **不做英文 i18n 框架**——專案既有輸出即繁中為主，本計畫只把殘留英文補齊，不引入語系切換。

---

## Step-by-Step Tasks

### Task 1: CLI 前門翻轉首推（guidance.go）
- **ACTION**: 改 `printWelcome` 的 tagline 與三條路徑順序。
- **IMPLEMENT**:
  - 第 35-36 行兩句改為：`"  Ramplio — 你的服務撐得住多少人？"` 與 `"  給網址，自動探測容量上限並給白話答案；數字你能自己驗證"`。
  - `printNextSteps` 標題改 `"\n  從這裡開始："`，三個 `nextStep` 改為：
    `{"① 探測容量上限（最推薦，一行回答撐多少人）", "ramplio discover --url https://example.com"}`、
    `{"② 開視覺面板（全程點選操作）", "ramplio run --dashboard"}`、
    `{"③ 直接壓測一個網址", "ramplio run --url https://example.com"}`。
  - 結尾「看完整指令清單」行改為點出進階能力：`"  進階：YAML 多階段情境、登入流程、分散式 → ramplio --help"`。
- **MIRROR**: NAMING_CONVENTION（printNextSteps 既有簽名，不改結構）。
- **IMPORTS**: 無新增。
- **GOTCHA**: `discover` 需要 `--url`，前門範例必須帶 URL，否則使用者複製後直接報 required flag 錯誤。
- **VALIDATE**: `go test ./cmd/ramplio/ -run TestPrintWelcome`（更新後）通過。

### Task 2: rootCmd 定位文案（main.go）
- **ACTION**: 改 `rootCmd` 的 `Short` 與 `Long`。
- **IMPLEMENT**:
  - `Short: "回答你的服務撐得住多少人 — 容量探測與壓力測試工具"`
  - `Long: "Ramplio 給網址就能自動探測 HTTP 服務的容量上限，輸出白話容量報告；量測準確度可用內建 mock-server 注入已知延遲自行驗證。也支援 YAML 多階段情境、登入流程與分散式壓測。"`
- **MIRROR**: 既有 cobra 欄位風格（main.go:10-21）。
- **IMPORTS**: 無。
- **GOTCHA**: `Version: "1.0.0"` 不要動。
- **VALIDATE**: `go run ./cmd/ramplio --help` 顯示新 Short/Long；`go build ./...` 無誤。

### Task 3: discover 報告與探測列繁中白話化（discover.go）
- **ACTION**: `printDiscoverProbe`（91-110）與 `printDiscoverReport`（114-159）全部字串繁中化，並加一行 ground-truth 信任背書；處理中文欄寬對齊。
- **IMPLEMENT**:
  - 探測列（108-109）：`"  每秒 %5d 個  %s  p99=%-8s  錯誤=%.1f%%\n"`。
  - 報告標題：`row("Capacity Report")` → `row("容量報告")`。
  - `Safe limit` → `安全上限：    每秒約 %d 個請求`；`< 5 req/sec` → `安全上限：    每秒不到 5 個請求`。
  - `Breaking point` → `臨界點：      每秒約 %d 個請求`；`not reached` → `臨界點：      測試範圍內未觸及`；`test cancelled` → `臨界點：      測試已中斷`。
  - `What this means:` → `這代表什麼：`。
  - 三個 switch 分支（145-156）的句子改繁中，口吻對齊 interpret.go（講「請求 / 服務」）：
    - SafeLimit==0：`你的服務在很低的流量下就吃力了。`／`建議先檢查伺服器健康狀態，再做壓測。`
    - Exhausted：`你的服務通過了全部 %d 個測試等級。`／`最大安全吞吐量超過每秒 %d 個請求。`／`想探更高可加 --max-rps 再跑一次。`
    - default：`你的服務每秒約能穩定處理 %d 個請求。`／`超過後回應時間會拉長到 %s 以上。`
  - 報告末尾在 `bot` 之前加入信任背書兩列：`row("")`、`row("這個數字怎麼信？工具量測準確度可用")`、`row("ramplio mock-server 注入已知延遲自行驗證。")`。
  - **中文對齊**：現有 `row()` 用 `fmt.Sprintf("  │  %-*s│\n", reportWidth-2, s)` 以 rune 數補空白，中文字顯示寬度為 2 會導致右框線錯位。**最小修正**：新增 `displayWidth(s string) int`（ASCII=1、其餘 rune=2，用 `unicode/utf8` 走訪 rune 判斷 `r < 128`），`row` 改為依 `displayWidth` 計算補空白數 `pad := reportWidth-2-displayWidth(s); if pad<0 {pad=0}`，再 `fmt.Printf("  │  %s%s│\n", s, strings.Repeat(" ", pad))`。
- **MIRROR**: REPORT_RENDER（保留 top/sep/bot 框線繪製）、PLAIN_LANGUAGE_TONE。
- **IMPORTS**: 新增 `unicode/utf8`（若用 `utf8.RuneCountInString` 輔助）；`strings` 已匯入。
- **GOTCHA**: `reportWidth=46` 為框內字元寬，中文混排時務必用顯示寬度而非 `len()`（byte 數）或 rune 數，否則框線參差。
- **VALIDATE**: `go test ./cmd/ramplio/ -run TestPrintDiscoverReport`（Task 5 新增）通過；手動 `go run ./cmd/ramplio discover --url http://localhost:8080`（先開 mock-server）目視框線對齊。

### Task 4: 前門測試同步（guidance_test.go）
- **ACTION**: 更新 `TestPrintWelcome` 的期望字串，反映新首推與 tagline。
- **IMPLEMENT**: `want` 清單改為含 `"撐得住多少人"`、`"ramplio discover --url https://example.com"`、`"ramplio run --dashboard"`、`"ramplio --help"`；移除已不存在的 `"ramplio init"` 首推斷言（init 指令仍在，只是不再是前門首推）。
- **MIRROR**: TEST_STRUCTURE。
- **IMPORTS**: 無。
- **GOTCHA**: `TestPrintNextSteps` 用的是自訂 step，與前門文案無關，不要改。
- **VALIDATE**: `go test ./cmd/ramplio/ -run TestPrintWelcome` 通過。

### Task 5: discover 報告測試（新增 discover_test.go）
- **ACTION**: 新增 `cmd/ramplio/discover_test.go`，斷言報告繁中化與信任背書出現、且不再含英文殘留。
- **IMPLEMENT**:
  - 由於 `printDiscoverReport` 目前直接 `fmt.Printf` 到 stdout，**先做最小可測重構**：抽出 `func writeDiscoverReport(w io.Writer, result discover.DiscoverResult, tolerance time.Duration)`，`printDiscoverReport` 改為呼叫它並傳 `os.Stdout`（鏡像 guidance.go 的 `io.Writer` 模式）。探測列同理可選擇抽 `writeDiscoverProbe(w, pr)`。
  - 測試：建構三種 `discover.DiscoverResult`（default 有 SafeLimit、Exhausted、SafeLimit==0），各自 `writeDiscoverReport(&buf, ...)`，斷言含 `"容量報告"`、`"安全上限"`、`"這代表什麼"`、`"mock-server"`；並斷言 **不含** `"Capacity Report"`、`"Safe limit"`、`"What this means"`。
- **MIRROR**: TEST_STRUCTURE（`bytes.Buffer` + `strings.Contains`）。
- **IMPORTS**: `bytes`、`strings`、`testing`、`time`、`github.com/machiko/ramplio/internal/discover`。
- **GOTCHA**: 抽 `io.Writer` 版本時，`row` 閉包要改吃 `w`，別留一份還寫 stdout。
- **VALIDATE**: `go test ./cmd/ramplio/...` 全通過。

### Task 6: Dashboard 前門升 discover 為主推（dashboard.html）
- **ACTION**: 把「探測上限」從底部小連結（1659-1661 `home-discover-link`）提升為第一張 `home-card--primary`，原「帶我設定」降為次要卡（移除其 `--primary` 與 `推薦` badge，或改放第二）；tagline（1638）對齊容量定位。
- **IMPLEMENT**:
  - tagline 1638：`三步測出網站撐不撐得住` → `給網址，回答你的服務能撐多少人`。
  - 卡片區（1641-1658）：新增／移動一張 primary 卡 `@click="pickMode('discover')"`，標題「探測容量上限」、desc「給網址，自動找出能撐多少人。不需要懂技術。」、badge「推薦」。原 guided 卡移除 `home-card--primary` 與 badge。
  - 底部 `home-discover-link`（1659-1661）：可移除（已升為主卡）或改成指向「進階：上傳 YAML / HAR 情境」。
- **MIRROR**: 既有 `home-card` / `home-card--primary` / `home-card-badge` class 結構（1642-1657）。
- **IMPORTS**: 無（純 HTML/Vue template）。
- **GOTCHA**: `pickMode('discover')` 已存在於既有 handler（底部連結用的就是它），改卡片只是換觸發點，不需動 JS 邏輯。確認 `pickMode` 對 `'discover'` 的分支仍有效。
- **VALIDATE**: `go build ./...`（embed 重新打包）；`go run ./cmd/ramplio run --dashboard` 開瀏覽器目視前門，探測上限為主推卡且可點進 Discover 分頁。

### Task 7: README hero 改容量回答機故事
- **ACTION**: 改開場（1-3 行）與功能特色的排序／開場句，讓容量回答機成為第一印象；既有功能列表保留但退居「也支援」。
- **IMPLEMENT**:
  - 第 3 行 intro 改為以容量問句開場，例：「**輸入網址，回答你的服務撐得住多少人。** Ramplio 自動探測 HTTP 服務的容量上限、輸出白話容量報告，而且數字你能用內建 mock-server 自行驗證。也支援 YAML 多階段情境、登入流程、即時儀表板與分散式壓測。」
  - 「功能特色」清單把 **Capacity Discovery** 移到第一條並強化措辭；其餘維持。
  - 「快速導航」可在最前面加一條：`- 🚀 **[30 秒回答容量](#capacity-discovery)** — 給網址，得到撐多少人的答案`（或指向既有 Capacity Discovery 段落的錨點）。
- **MIRROR**: README 既有繁中條列風格。
- **IMPORTS**: N/A。
- **GOTCHA**: 不要刪既有段落（VU/RPS、認證、分散式…），定位收斂是「重新排序與開場」，不是砍內容。
- **VALIDATE**: 人工檢視 README 開場是否一眼傳達容量回答機；錨點連結有效。

---

## Testing Strategy

### Unit Tests
| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| `TestPrintWelcome`（更新） | `printWelcome(&buf)` | 含「撐得住多少人」「ramplio discover --url …」「ramplio run --dashboard」 | 否 |
| `TestWriteDiscoverReport_Default` | SafeLimit=200, BreakingPoint=300 | 含「容量報告」「安全上限」「每秒約 200」「mock-server」；不含「Capacity Report」 | 否 |
| `TestWriteDiscoverReport_Exhausted` | Exhausted=true, SafeLimit=2000 | 含「通過了全部」「--max-rps」 | 邊界（探到頂） |
| `TestWriteDiscoverReport_Struggling` | SafeLimit=0 | 含「很低的流量下就吃力」「檢查伺服器健康」 | 邊界（首探即敗） |
| `TestWriteDiscoverReport_Alignment` | 含中文的列 | 每行 `displayWidth` ≤ reportWidth-2，右框線對齊 | 中文寬度 |

### Edge Cases Checklist
- [x] SafeLimit==0（首探即失敗）
- [x] Exhausted（全部通過、未觸臨界）
- [x] BreakingPoint==0 且非 Exhausted（中途取消）→ 「測試已中斷」
- [x] 中文混排欄寬對齊
- [ ] 不適用：空輸入（discover 強制 `--url`，cobra 先擋）
- [ ] 不適用：並發／網路失敗（本計畫不碰探測執行路徑）

---

## Validation Commands

### Static Analysis
```bash
gofmt -l cmd/ramplio/ internal/discover/
go vet ./cmd/ramplio/...
```
EXPECT: `gofmt -l` 無輸出；vet 無告警。

### Unit Tests
```bash
go test ./cmd/ramplio/... -v
```
EXPECT: 全通過（含更新後的 TestPrintWelcome 與新 discover_test.go）。

### Full Test Suite
```bash
go test -race ./...
```
EXPECT: 無回歸（本計畫不碰共享狀態，race 應維持綠燈）。

### Lint
```bash
golangci-lint run
```
EXPECT: 無新增問題。

### Browser Validation
```bash
go build ./...
go run ./cmd/ramplio mock-server --latency 20ms &
go run ./cmd/ramplio run --dashboard
# 瀏覽器確認前門「探測容量上限」為主推卡，點進可跑 Discover
```
EXPECT: 探測上限為第一主推、可運作。

### Manual Validation
- [ ] `go run ./cmd/ramplio`（無參數）→ 首推 discover、tagline 為容量問句
- [ ] `go run ./cmd/ramplio --help` → Short/Long 為容量定位
- [ ] `go run ./cmd/ramplio mock-server --latency 20ms &` 後 `discover --url http://localhost:8080` → 報告全繁中、框線對齊、末尾有 mock-server 信任背書
- [ ] README 開場一眼是容量回答機

---

## Acceptance Criteria
- [ ] CLI 前門首推 `discover`，tagline 為容量問句
- [ ] `discover` 報告與探測列無英文殘留、框線對齊
- [ ] 報告末尾含 ground-truth（mock-server）信任背書
- [ ] rootCmd Short/Long 為容量定位
- [ ] Dashboard 前門「探測上限」為主推卡
- [ ] README hero 為容量回答機故事，且未刪既有段落
- [ ] 所有驗證指令通過，`go test -race ./...` 無回歸

## Completion Checklist
- [ ] 遵循既有 `io.Writer` 純函式輸出模式（discover 報告已可測）
- [ ] 白話措辭口吻對齊 `interpret.go`（講人／請求，不堆術語）
- [ ] 測試遵循 `bytes.Buffer` + `strings.Contains` 模式
- [ ] 無硬編魔術數字（沿用既有 `reportWidth`）
- [ ] 無刪除既有功能（只調整主推 vs 淡化）
- [ ] commit 遵守 isocialwork-conventions（`type: 動詞 敘述`、繁中、無 Co-Authored-By）

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 中文欄寬對齊在不同終端字型下仍微錯位 | 中 | 低 | 用 displayWidth（ASCII=1/其餘=2）涵蓋主要情況；接受 emoji 等寬字例外 |
| 既有使用者習慣 `run --dashboard` 為首推，改動造成困惑 | 低 | 低 | 三路徑全保留，只是換順序；dashboard 仍在第二位 |
| dashboard.html embed 改動未重新 build 導致前門沒更新 | 中 | 中 | 驗證步驟強制 `go build ./...` 後再開 dashboard |
| 過度繁中化把指令名 / flag 也翻譯 | 低 | 中 | 只翻譯說明文字，指令與 flag（discover/--url/--max-rps）一律保留原文 |

## Notes
- 此計畫對應評估建議的 **P0（定位收斂）**，刻意**不含 P1（`ramplio verify` 一鍵自證）與 P2（assertion retry / rate 模式記憶體硬傷）**，避免範疇蔓延。
- 定位選擇理由（來自 `/ecc:prp-plan` 對話）：「容量回答機」是最尖、最差異化的定位，且避開「為什麼不用 k6」這個必輸問題；ground-truth 自證在此定位中**降格為信任背書**而非主賣點。
- 後續若採用此定位，建議接著規劃 P1 的 `ramplio verify` 子指令，把報告末尾的「自行驗證」從一句話變成一鍵可重現的體驗。

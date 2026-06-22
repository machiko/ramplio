# 量測準確度與公信力

> 壓測工具的價值建立在一個前提上：**它報出來的數字是可信的**。本文說明 Ramplio 如何證明自己的量測準確，而不是要求你「相信我們」。

---

## 為什麼不靠「跟 k6 / JMeter 比一比」

把數字拿去跟知名工具對照，是一種佐證，但它是**最弱的根基**——你的公信力會寄生在別人工具的正確性上。如果兩個工具都用同樣的方式量錯（例如都有 Coordinated Omission），對照起來「一致」，卻一致地錯。

Ramplio 採取更硬的做法：**對一個延遲分佈已知的標的施壓，反推量測準不準**。伺服器端注入多少延遲是我們自己決定的事實（ground truth），量測結果該不該等於它，是純粹的數學問題，不需要任何第三方背書。

---

## 三根公信力支柱

| 支柱 | 本質 | 驗證方式 |
|------|------|----------|
| **方法論正確性** | 量測在「該送的時間」起算，消除 Coordinated Omission | RPS 模式並陳 service / 壓力下兩種延遲（見下節） |
| **Ground-truth 自我驗證** | 對已知延遲分佈施壓，量測應吻合 | `internal/engine/groundtruth_test.go` |
| **量測透明度** | 看得見量了什麼、工具自身有沒有當瓶頸 | latency 分解 + 產生器自我健康度 |

---

## Ground-truth 自我驗證

### 原理

伺服器端注入一個**已知的延遲分佈**，Ramplio 對它施壓，量到的百分位必須落在容差內。量測值只可能 **≥** 注入值（多了本機網路往返與處理負擔），不可能低於它——若低於，代表量測有 bug。

### 自動化驗證

`internal/engine/groundtruth_test.go` 內含兩個基準測試：

| 測試 | 注入分佈 | 斷言 |
|------|----------|------|
| `TestGroundTruth_FixedLatency` | 固定 30ms | p50、p99 ∈ [30ms, 50ms] |
| `TestGroundTruth_BimodalSeparatesTail` | 90% 10ms ＋ 10% 120ms | p50 落在快帶、p99 落在慢帶 |

第二個測試證明 HDR 直方圖能正確**分離尾端延遲**——這正是平均值會抹平、而百分位不該抹平的東西。

```bash
go test ./internal/engine/ -run TestGroundTruth -v
```

### 自己動手驗證

`mock-server` 可注入確定性延遲，讓你重現同樣的驗證：

```bash
# 固定延遲：量到的所有百分位都應 ≈ 50ms
ramplio mock-server --latency 50ms &
ramplio run --url http://localhost:8080 --rps 200 --duration 20s

# 雙峰分佈：10% 的請求慢（200ms），其餘快（10ms）
# 量到的 p50 應 ≈ 10ms，p95/p99 應 ≈ 200ms
ramplio mock-server --latency-fast 10ms --latency-slow 200ms --slow-pct 10 &
ramplio run --url http://localhost:8080 --rps 200 --duration 30s
```

| 旗標 | 說明 |
|------|------|
| `--latency` | 固定延遲，每個請求都等這麼久 |
| `--latency-fast` | 雙峰：快帶延遲（多數請求） |
| `--latency-slow` | 雙峰：慢帶延遲（尾端） |
| `--slow-pct` | 雙峰：多少 % 的請求走慢帶（0–100） |

注入分佈是已知事實，量測結果該等於它——這就是公信力的數學基礎。

---

## 設計上既有的準確度保障

這些選型決策（詳見 `docs/tech-decisions.md`）本身就是準確度的前提：

- **Go goroutine**：產生器自身在高並發下不排隊，量到的延遲不含工具等待時間。
- **HDR Histogram**：固定記憶體、可設定精度（0.1%），不用排序整個 slice，long soak 不 OOM。
- **Buffered channel + 單一 aggregator**：VU hot path 記錄樣本零鎖等待，量測不含 mutex 競爭時間；channel 滿載時 dropped samples 會被計數並警告，不靜默吞掉。

---

## Coordinated Omission 修正（rate 模式）

### 問題

closed-loop 壓測工具（送出一個請求 → 等回應 → 再送下一個）有個著名的系統性偏差：當系統變慢，工具「自動」放慢送出速度，於是**最該被記錄的慢請求根本沒被送出**，量到的延遲遠低於使用者實際經歷。Gil Tene（HdrHistogram 作者）稱之為 Coordinated Omission。

### 修正方式

Ramplio 的 rate（open）模式以單一 **dispatcher** 按目標速率排定每個請求的「應送時間」，與 worker 是否有空無關。worker 真正送出時，量測分兩條並陳：

| 數字 | 從何時起算 | 意義 |
|------|-----------|------|
| **服務延遲（service）** | worker 實際送出 | 伺服器處理一個請求要多久 |
| **壓力下延遲（corrected）** | 該請求「排定要送」的時間 | 使用者實際從點擊到看到回應要等多久 |

當系統跟不上請求速率，請求在 dispatcher 的佇列中排隊，`corrected = 完成時間 − 應送時間` 會把這段排隊等待算進去——這正是 closed-loop 工具會漏掉的。報告的**整體結論採用 corrected p99**，因為那才是使用者真正的體驗。

> VU（closed-loop）模式沒有「排定時間」這個概念，因此不套用修正，報告也不顯示 corrected 數字——誠實標明適用範圍。
>
> 注意：當產生器有充足餘裕（worker 池遠大於同時在途請求數），corrected ≈ service，不會無中生有地灌水。修正只在系統逼近或超過可承受速率時才顯現差距。

### 驗證

- `internal/metrics/coordinated_omission_test.go`：直接驗證修正數學（排隊 180ms → corrected p99 反映完整等待、service p99 只反映處理時間），並驗證分散式合併保留修正。
- `internal/engine/coordinated_omission_test.go`：驗證 rate 模式串接 ScheduledAt；有餘裕時 corrected 不灌水；VU 模式不產生 corrected。

```bash
ramplio mock-server --latency 50ms &
# 目標 500 RPS，但伺服器每次 50ms：觀察 corrected p99 ≫ service p99
ramplio run --url http://localhost:8080 --rps 500 --duration 30s
```

## 量測透明度

公信力不只在「數字準」，也在「看得見量了什麼、工具有沒有當瓶頸」。

### 連線分解（pre-flight）

開跑前的單發預檢若連得上目標，會印出這一發請求的時間花在哪：

```
✓ 預檢通過：https://example.com/（HTTP 200，總共 142ms）
  連線分解（這一發請求的時間花在哪）：
    DNS 解析：12ms    連線：28ms    TLS：61ms    首位元組：138ms
```

用 `httptrace` 拆 DNS / TCP 連線 / TLS 握手 / 首位元組（TTFB）。沿用既有 keep-alive 連線時會標明（省去 DNS／連線／TLS）。這段只在**單發診斷請求**上量測（`ExecuteTraced`），壓測 hot path 的 `Execute` 完全不付出 trace 成本，因此不會干擾正式量測。

### 量測可信度（產生器自我健康度）

壓測工具若自己丟樣本或為了 GC 停頓，就是在干擾它要量的東西。報告末尾的「量測可信度」直接講清楚這次數字能不能信：

| 訊號 | 來源 | 影響 |
|------|------|------|
| 丟棄樣本比例 | collector channel 滿載 | ≥1% → 可信度偏低（漏掉的常是表現最差的請求）|
| 產生器 GC 暫停 | `runtime.MemStats` 跑前/跑後差值 | 佔測試時長 ≥5% → 偏低、≥2% → 中等 |
| 尖峰 goroutine 數 | aggregator 週期取樣 | 透明呈現產生器並發規模 |

判語分 高／中等／偏低 三級；偏低時建議降載或分散到多節點重測。GC 干擾過高也會在「診斷發現」列出白話歸因。

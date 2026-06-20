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

## 後續支柱

- **Coordinated Omission 修正**（Phase 2）：RPS 模式下，量測從排定發送時間起算，系統被壓垮時不再低報延遲。
- **量測透明度**（Phase 3）：latency 拆解為 DNS / 連線 / TLS / TTFB / total；產生器自我健康度判語，工具自身可能成為瓶頸時主動警告。

# 技術選型決策記錄

> 記錄 Ramplio 各項技術選型的原因與取捨，供日後審視或調整參考。

---

## 語言：Go

壓力測試工具本身就是「被測系統的競爭對手」——必須產生大量並發請求，同時自身不能成為瓶頸。

| 面向 | Go | Node.js | Python |
|------|-----|---------|--------|
| 並發模型 | goroutine（M:N threading，每個約 2KB） | event loop（單執行緒，I/O 密集尚可） | GIL 限制真正並行 |
| 極高並發下 | 線性擴展 | event loop 開始排隊 | 多進程才能突破 GIL |
| 部署 | 單一靜態 binary | 需要 Node runtime | 需要 Python + 套件 |
| 業界驗證 | k6、Vegeta、hey | Artillery | Locust |

**關鍵問題**：若用 Node.js，event loop 在極高並發下會產生排隊延遲，量測到的 latency 有一部分是測試工具本身的等待時間，**量測誤差直接影響測試可信度**。Go goroutine 的輕量設計消除了這個誤差來源。

---

## Latency 量測：HDR Histogram

這是測試工程師最容易踩的坑，選型的正確性直接影響報告可信度。

### 為什麼不用平均值

平均值掩蓋尾端延遲（tail latency）：

```
99% 請求 → 10ms
 1% 請求 → 10,000ms（10s）
平均值   → ~110ms  ← 完全不反映真實問題
```

### 為什麼不用簡單 slice 排序取 percentile

- 100 萬筆請求 → 8MB slice，GC 壓力影響測試本身
- 記憶體用量隨請求數線性成長，長時間 soak test 會 OOM

### HDR Histogram 的優勢

- **固定記憶體上限**，不論請求數量
- **精度可設定**（如 0.1% 誤差），適合 latency 量測
- **業界標準**：Cassandra、Disruptor、k6 皆採用

---

## Metrics 傳遞：Buffered Channel + 單一 Aggregator

```
[VU goroutine 1] ─┐
[VU goroutine 2] ─┼──► chan metrics.Sample ──► [aggregator goroutine]
[VU goroutine N] ─┘
```

### 為什麼不用 Mutex 直接寫共享結構

1. **量測失真**：VU 在收到 HTTP response 後必須立刻記錄樣本。若此時競爭 mutex，量測到的 latency 包含鎖等待時間，數據失真。
2. **競爭複雜度**：N 個 goroutine 共寫一個結構，race condition 難以窮舉測試。

### Buffered Channel 的優勢

- **VU hot path 零等待**：寫入 channel 是非阻塞操作（buffer 未滿時）
- **aggregator 是唯一讀寫者**：天然消除 race condition，無需額外 mutex
- **背壓可見**：channel 接近滿載代表 aggregator 跟不上，本身就是系統壓力信號

---

## Scenario 格式：YAML DSL

### 為什麼不用程式碼（如 k6 的 JavaScript）

k6 選 JS 是因為它要支援複雜業務邏輯腳本（登入、取 token、串接多步驟請求）。

Ramplio 定位在 **API / 網站層的壓力測試**，80% 場景需求是：
- 指定端點與請求結構
- 設定 ramp-up / hold / ramp-down 階段
- 定義 pass/fail threshold

YAML 的優勢：

| 考量 | YAML | 程式碼 DSL |
|------|------|-----------|
| 上手門檻 | 任何工程師可讀 | 需學習腳本語言 |
| Git diff | 乾淨、可審查 | 較難 review |
| CI/CD 整合 | 可程式化生成 | 需額外抽象層 |
| 複雜邏輯 | 有限（設計如此） | 彈性高 |

**未來擴展路徑**：若需要動態邏輯（如 OAuth token 刷新），在 YAML 中增加 `script` 欄位引用外部腳本，而非一開始強迫所有使用者寫程式。

---

## CLI 框架：Cobra + Viper

Go 生態的事實標準，Kubernetes、Hugo、GitHub CLI 皆採用。

- **Cobra**：subcommand 結構（`run`、`validate`、`report`）
- **Viper**：設定優先序 = flag > 環境變數 > config file，適合 CI/CD 環境覆寫

無獨特理由選其他方案，選最多人維護的工具降低未來維護成本。

---

## OpenTelemetry 整合:手刻 OTLP/JSON + opt-in trace context

v3 Phase 2 的兩個反直覺選擇,皆由實測數據決定:

- **不引入 otel SDK,手刻 OTLP/HTTP JSON**:sink 契約是「測後單發匯出 ~10 個 gauge」,不需要 SDK 的 MeterProvider/periodic reader 整棵依賴樹。實測 binary 增重 +18KB(SDK 路線估 +數 MB),與既有 Influx(line protocol)/Loki(JSON)手刻 sink 風格一致。代價是 OTLP/JSON 編碼細節自扛(camelCase、fixed64 以字串表示),已由審查對照規範驗證。
- **traceparent 注入預設關閉(`--trace-context` opt-in)**:交錯 A/B benchmark 實測逐請求注入在產生器極限吞吐下造成約 5% 退化(生成已優化至 63ns/1 alloc,成本在 map insert/配置/GC 的複利),與「hot path 零額外成本」原則衝突。需要 APM 關聯時明確開啟,成本記載於旗標說明。

---

## 瓶頸關聯:Jaeger 首發 + 三態誠實歸因

v3 Phase 3 的設計取捨:

- **Jaeger query API 首發,TraceSource 介面留擴充位**:Jaeger 的 `/api/traces` JSON API 穩定且易 stub 測試;介面最小化(FetchSpans),Tempo 等後端之後以相同介面加入。Jaeger 預設 limit=20 對統計分析嚴重欠採樣,顯式設 1000 且「截斷」以 FetchResult.Truncated 對下游可見。
- **三態歸因(ok/insufficient/no_culprit)+ 排除可見化**:錯誤歸因比不歸因更傷公信力。樣本不足回報「關聯不足」絕不硬給答案;等幅變慢回報「疑似資源飽和」而非硬指最慢者;因樣本不足被排除的 operation 一律列名(ExcludedOps)——沒有這個,no_culprit 會變成「已窮盡搜尋」的假斷言。
- **比較窗口出自 rate 負載輪廓**:爬升前半(0→50% 負載)當基準、持平段當臨界,與 runRPS 共用同一份窗口數學(rateProfile)。已知偏誤如實揭露:冷啟動落在基準窗會讓倍率被低估;p95 偵測不到「慢路徑佔比上升」型退化。
- **歸因自證**:與 `ramplio verify` 同一哲學——對已知瓶頸分佈做關聯,結論必須指向注入的瓶頸,固化為整合測試(TestObserveGroundTruthE2E)。

---

## 選型核心原則

> 每個選型都回到同一個問題：**「這個選擇會不會讓測試數據失真？」**

這是壓力測試工程師與一般應用工程師選型時最根本的差異——工具本身的行為不能干擾被量測的對象。

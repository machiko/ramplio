# Plan: P2 — 鞏固設計 + 量測透明度

## Summary
把 analysis 標為「技術債」的兩項，重新定位成**鞏固可辯護的設計 + 提升透明度**，而非照字面修（照字面修會掩蓋失敗、破壞 CO 修正）：
- **A. assertion 不 retry**：維持現況（retry 會掩蓋真實缺陷、傷準確度），補回歸測試 + 文件確立為刻意行為。
- **B. rate 模式 worker pool**：把 eager `maxRPS×5` 預生改成 **grow-on-demand（只增不減、到 cap 為止）**，省下低延遲目標的閒置 goroutine（減少排程抖動，利於量測可靠度），且**完整保留 Coordinated Omission 語義**（達 cap 才阻塞=真正的產生器極限）；達 cap 時於「量測可信度」判語誠實標示「產生器自身可能是瓶頸」。

## User Story
作為一個會細看壓測工具內部行為的資深使用者，
我想要工具的 retry / worker pool 行為是「有意為之且講得清楚」的，而不是看起來像沒處理好的邊角，
這樣我才信得過它在高壓下的數字，也知道何時是工具自己到極限、何時是目標撐不住。

## Problem → Solution
**現狀**：
- assertion 失敗只記為 error、不 retry——正確，但無測試/文件背書，看起來像漏洞。
- `runRate` eager 預生 `clamp(maxRPS×5, 10, 5000)` 個 worker：低延遲目標下大量閒置（如 200 RPS/50ms 只需 ~10 個卻生 1000 個）；高 RPS×高延遲時 cap 5000 又可能 under-provision，造成**假性 overload**（產生器自己不夠 worker，被誤判成目標撐不住），而使用者看不到這件事。

**目標**：
- A：assertion 不 retry 變成「有測試守、有文件說」的刻意設計。
- B：worker 數隨真實需求成長（省記憶體/排程），CO 背壓只在達 cap 時出現，且達 cap 這件事被誠實揭露在可信度判語。

## Metadata
- **Complexity**: Medium（動到 `runRate` 並發熱路徑，需 -race 嚴格把關）
- **Source PRD**: N/A（評估建議 P2；方向經 AskUserQuestion 確認＝「鞏固設計+透明度」）
- **Estimated Files**: 5 改 + 1~2 測試

---

## UX Design
內部變更為主。唯一使用者可見差異：當 rate 模式達到 worker 上限時，報告末尾「量測可信度」會多一句歸因——

### Before（達 worker 上限、目標看似很慢時）
```
量測可信度
─────────
  ⚠ 中等：…（只看丟樣本/GC）
```
（使用者無從得知「壓力下延遲」可能來自產生器自己不夠 worker）

### After
```
量測可信度
─────────
  ⚠ 中等：…
      產生器達到 worker 上限（5000），壓力下延遲可能部分來自產生器自身而非目標；
      考慮分散到多節點重測以釐清。
```

### Interaction Changes
| Touchpoint | Before | After | Notes |
|---|---|---|---|
| rate 模式 worker 數 | eager `maxRPS×5` | grow-on-demand 到 cap | 純內部 |
| 達 worker 上限 | 不可見 | 可信度判語標示產生器可能是瓶頸 | 透明度 |
| assertion 失敗 | 記 error、不 retry（無測試） | 同左，但有測試+文件確立 | 行為不變 |

---

## Mandatory Reading
| Priority | File | Lines | Why |
|---|---|---|---|
| P0 | `internal/engine/ramp.go` | 370-460（runRate）、462-508（dispatch）、522-602（runRateWorker） | 要改的核心；CO 戳記 `scheduledAt=應送時間`、`jobs<-` 阻塞=背壓 |
| P0 | `internal/engine/coordinated_omission_test.go` | 全 | **不可破壞**的 CO 回歸守；改完必須全綠 |
| P1 | `internal/metrics/summary.go` | 5-44 | 加 `GeneratorWorkerCapHit`；既有 `Generator*` 欄位風格 |
| P1 | `internal/metrics/export.go` | `Export`/`MergeExports` | 新 bool 欄位跨節點 OR 合併 |
| P1 | `internal/reporter/confidence.go` | 全 | `MeasurementConfidence(sum)` 只吃 Summary，加 cap-hit 分支 |
| P1 | `internal/reporter/confidence_test.go` | 全 | 測試模式 |
| P2 | `internal/protocols/retry.go` | 全 | 確認 retry 不涉 assertion（shouldRetry 只看 Error/StatusCode） |

## External Documentation
無需——全為內部並發與量測邏輯。

---

## Patterns to Mirror

### CO_STAMPING（不可動的 CO 語義）
```go
// SOURCE: internal/engine/ramp.go:501-503
scheduledAt := now.Add(delay)   // 應送時間
select {
case jobs <- scheduledAt:       // 阻塞 = 背壓 = 要量的排隊延遲
case <-ctx.Done(): return
}
// worker: corrected = At - ScheduledAt（At = 完成時間）
```

### SINGLE_SPAWNER_WG（避免 wg.Add 與 wg.Wait 並發）
```go
// SOURCE: internal/engine/ramp.go:412-455
// dispatcher 是唯一 spawner；runRate 先 <-dispDone（dispatcher 全停）才 wg.Wait()
dispCancel(); <-dispDone; workerCancel(); wg.Wait()
```

### GENERATOR_HEALTH_FIELD（Summary 自我健康度欄位）
```go
// SOURCE: internal/metrics/summary.go:42-44
GeneratorPeakGoroutines int
GeneratorGCPause        time.Duration
// 新增：GeneratorWorkerCapHit bool —— rate 模式是否觸及 worker 上限
```

### CONFIDENCE_BRANCH（可信度判語只吃 Summary）
```go
// SOURCE: internal/reporter/confidence.go:38-54
switch { case ...: return ConfidenceReading{Level, Icon, Note} }
```

### TEST_STRUCTURE（engine 計時測試 + -short 跳過）
```go
// SOURCE: internal/engine/groundtruth_test.go:38-41
if testing.Short() { t.Skip("timing test skipped in -short mode") }
```

---

## Files to Change
| File | Action | Justification |
|---|---|---|
| `internal/engine/ramp.go` | UPDATE | `ratePool`（idle/total/capHit）；runRate 起 min 池；dispatch 送前 `maybeGrow`；worker 計 idle |
| `internal/metrics/summary.go` | UPDATE | 新增 `GeneratorWorkerCapHit bool` |
| `internal/metrics/export.go` | UPDATE | 合併時 OR `GeneratorWorkerCapHit` |
| `internal/reporter/confidence.go` | UPDATE | cap-hit → 至多 medium + 歸因 note |
| `internal/engine/ramp_pool_test.go` | CREATE | grow-on-demand：低延遲峰值 worker ≪ cap；CO 測試保持綠 |
| `internal/engine/assertion_retry_test.go` | CREATE | assertion 失敗記 error 且**不** retry（即使設了 Retry） |
| `internal/reporter/confidence_test.go` | UPDATE | cap-hit 判語測試 |

## NOT Building
- **不改 CO 戳記/修正數學**——只改 worker 供給方式，背壓/`corrected` 計算不動。
- **不讓 assertion 觸發 retry**（除非未來另開 opt-in；本次不做）。
- **不做 worker 縮減（shrink）**——grow-only 到 cap，run 結束統一收。避免抖動與額外並發風險。
- **不改 VU 模式**——本計畫只碰 rate（open）模式。
- **不改 cap 公式上限**（仍 `maxRPS×5`、硬上限 5000）——只把它從「eager 預生量」變成「成長上限」。

---

## Step-by-Step Tasks

### Task 1: ratePool 與 grow-on-demand（ramp.go）
- **ACTION**: 新增 `ratePool` 型別與成長邏輯；runRate 改為起 `rateMinWorkers` 個、其餘按需成長。
- **IMPLEMENT**:
  ```go
  const ( rateMinWorkers = 10; rateHardCap = 5000; rateWorkerHeadroom = 5 )

  type ratePool struct {
      jobs   chan time.Time
      idle   atomic.Int32
      total  atomic.Int32
      max    int
      capHit atomic.Bool
      spawn  func()
  }
  // maybeGrow 在 dispatcher 送 job 前呼叫：沒有閒置 worker 且未達上限就生一個；
  // 達上限則標記 capHit 並讓後續 send 阻塞（= 真正的背壓，CO 照常捕捉）。
  func (p *ratePool) maybeGrow() {
      if p.idle.Load() > 0 { return }
      if int(p.total.Load()) >= p.max { p.capHit.Store(true); return }
      p.spawn()
  }
  ```
  - runRate：`maxWorkers := clamp(maxRPS*rateWorkerHeadroom, rateMinWorkers, rateHardCap)`；`jobs := make(chan time.Time, maxWorkers)`（buffer 維持 = cap，背壓 backlog 深度不變）。
  - `pool := &ratePool{jobs: jobs, max: maxWorkers}`；`pool.spawn = func(){ pool.total.Add(1); wg.Add(1); go func(){ defer wg.Done(); e.runRateWorker(workerCtx, jobs, &pool.idle) }() }`。
  - 初始：`for i:=0;i<rateMinWorkers;i++ { pool.spawn() }`。
  - 收尾後：`sum.GeneratorWorkerCapHit = pool.capHit.Load()`。
- **MIRROR**: SINGLE_SPAWNER_WG（spawn 只由 dispatcher + 初始迴圈呼叫，皆在 `<-dispDone` 之前）。
- **IMPORTS**: `sync/atomic`（ramp.go 已用 atomic？確認；engine 已用 `e.activeVUs.Add`，atomic 已匯入）。
- **GOTCHA**:
  - **wg.Add 不可與 wg.Wait 並發**：spawn 僅由初始迴圈（dispatcher 啟動前）與 dispatcher（`<-dispDone` 後不再呼叫）觸發。順序 `dispCancel()→<-dispDone→workerCancel()→wg.Wait()` 必須保留。
  - `idle`/`total` 是兩個獨立 atomic、非一致快照——可接受：最壞多生 1 個（受 cap 限）或瞬間少生（send 進 buffer 等新 worker，老化 ~µs，遠低於 `coOmissionGapFloor`，不誤判）。
- **VALIDATE**: `go test -race ./internal/engine/...` 全綠（特別是 coordinated_omission_test）。

### Task 2: dispatch 送前成長 + worker idle 計數（ramp.go）
- **ACTION**: `dispatch` 簽名改吃 `*ratePool`，送 job 前 `pool.maybeGrow()`；`runRateWorker` 加 `idle *atomic.Int32` 計數。
- **IMPLEMENT**:
  - `func (e *RampEngine) dispatch(ctx context.Context, lim *rate.Limiter, pool *ratePool)`：原 `jobs chan<- time.Time` 改 `pool`；送處改：
    ```go
    scheduledAt := now.Add(delay)
    pool.maybeGrow()
    select {
    case pool.jobs <- scheduledAt:
    case <-ctx.Done(): return
    }
    ```
  - `func (e *RampEngine) runRateWorker(ctx context.Context, jobs <-chan time.Time, idle *atomic.Int32)`：
    ```go
    for {
        var scheduledAt time.Time
        idle.Add(1)
        select {
        case <-ctx.Done(): idle.Add(-1); return
        case scheduledAt = <-jobs:
        }
        idle.Add(-1)
        ... 其餘原樣 ...
    }
    ```
- **MIRROR**: CO_STAMPING（maybeGrow 加在 stamping 之後、send 之前，不改 scheduledAt）。
- **GOTCHA**: `idle.Add(-1)` 必須在「收到 job」與「ctx.Done 離開」兩條路徑都正確執行——用上面的位置（收到後立刻 -1；Done 分支內 -1）。勿漏。
- **VALIDATE**: 同 Task 1；新增 ramp_pool_test 驗證峰值 worker。

### Task 3: Summary 欄位 + 分散式合併（summary.go, export.go）
- **ACTION**: 加 `GeneratorWorkerCapHit bool`；合併時 OR。
- **IMPLEMENT**:
  - summary.go：在 `GeneratorGCCount` 後加 `GeneratorWorkerCapHit bool` // rate 模式是否觸及 worker 上限（產生器可能成為瓶頸）。
  - export.go：`MergeExports`/`Export` 聚合處，`merged.GeneratorWorkerCapHit = a || b`（任一節點觸及即為真）。
- **MIRROR**: GENERATOR_HEALTH_FIELD。
- **GOTCHA**: 確認 `HistogramExport` 是否也需帶此欄位跨節點（若 worker 回傳的是 export 而非 summary）。比照既有 `DroppedSamples`/`Generator*` 的跨節點處理方式一致對待。
- **VALIDATE**: `go test ./internal/metrics/...` 綠。

### Task 4: 可信度判語納入 cap-hit（confidence.go）
- **ACTION**: `MeasurementConfidence` 在 cap-hit 時，等級至多 medium 並附歸因。
- **IMPLEMENT**: 在現有 switch 前先處理：若 `sum.GeneratorWorkerCapHit`，且原本會落在 high，則降為 medium，note 改/附：
  「中等：產生器達到 worker 上限，壓力下延遲可能部分來自產生器自身而非目標；考慮分散到多節點重測以釐清。」
  若原本就是 low（丟樣本/GC 嚴重），維持 low 但 note 併入 cap-hit 句。實作上可在回傳前組合 note，或用 helper `appendCapHitNote(reading, sum)`。
- **MIRROR**: CONFIDENCE_BRANCH。
- **GOTCHA**: 不要無條件把 cap-hit 打成 low——達 cap 不必然是壞事（可能合法用滿）；它是「歸因要小心」的訊號，medium + note 最誠實。
- **VALIDATE**: confidence_test 新案通過。

### Task 5: assertion 不 retry — 測試 + 文件（assertion_retry_test.go, ramp.go 註解）
- **ACTION**: 新增測試確立「assertion 失敗記為 error 且不被 retry」；在兩處 assertion 評估點補一行意圖註解。
- **IMPLEMENT**:
  - 測試：起 `httptest.Server` 永遠回 200 但 body 不符 assertion；step 設 `Retry{Count:3}` 且 assertion 要求 body 含某字串。跑極短 VU 場景，斷言：(a) 該 server 收到的請求數 == 完成的 sample 數（無額外 retry 嘗試）；(b) 結果計為 error（assertion 失敗）。用原子計數器記 server 命中次數。
  - 註解（ramp.go:566 與 667 附近）：
    `// Assertions are evaluated after the executor (incl. retry) returns: an assertion`
    `// failure means the server replied but the content is wrong — a real defect signal`
    `// we record as an error, NOT something to retry (retrying would mask it).`
- **MIRROR**: TEST_STRUCTURE；retry.go 的 shouldRetry 不涉 assertion（佐證設計）。
- **GOTCHA**: 用 VU 模式（closed-loop）測較單純；server 命中數 = retry 次數的證據——若 assertion 觸發 retry，命中數會 > sample 數。
- **VALIDATE**: `go test -race ./internal/engine/ -run TestAssertion` 綠。

### Task 6: grow-on-demand 行為測試（ramp_pool_test.go）
- **ACTION**: 驗證低延遲目標下峰值 worker 遠小於 cap，且總請求量正常。
- **IMPLEMENT**:
  - 對一個快速回應（~1ms）的 httptest server 跑 rate 模式（如 50 RPS、2s）。
  - 由於 `total` 是內部狀態，最乾淨的觀測是新增一個測試可讀的鉤子：在 `ratePool` 加 `total` 的讀取已有（atomic）。測試可透過 `engine` 暴露一個 `peakRateWorkers`（測試用）或斷言 `sum.GeneratorWorkerCapHit == false` 且請求量合理。**最小可測**：斷言低延遲下 `sum.GeneratorWorkerCapHit==false`（未觸 cap）＋ `sum.Total` 接近 RPS×duration。若要直接驗峰值，於 `RampEngine` 加一個未匯出 `ratePeakWorkers int32`（atomic，spawn 時更新 max），測試經由套件內 helper 讀取。
  - 另一案（-short skip 的計時案）：高 RPS 超過 cap 時 `GeneratorWorkerCapHit==true`。
- **MIRROR**: TEST_STRUCTURE。
- **GOTCHA**: 計時相關案以 `testing.Short()` 跳過，避免 CI 抖動。
- **VALIDATE**: `go test -race ./internal/engine/...` 綠。

### Task 7: analysis.md 更新技術債狀態
- **ACTION**: 把兩項從「已知問題」移到「已解決/已釐清」，記錄理由。
- **IMPLEMENT**: 在 analysis.md「已知問題與技術債」表移除/標註：assertion-retry（釐清為刻意設計+測試守）、runRate worker（改 grow-on-demand+透明度）；於「已解決」段補一行帶 commit 脈絡。
- **VALIDATE**: 人工檢視。

---

## Testing Strategy
### Unit / Integration Tests
| Test | Input | Expected | Edge? |
|---|---|---|---|
| `TestAssertionFailure_NotRetried` | 200 但 body 不符 + Retry{3} | server 命中數==sample 數、計為 error | 是（核心設計守） |
| `TestRatePool_LowLatencyStaysSmall` | 50 RPS / 1ms server | `GeneratorWorkerCapHit==false`、Total≈100 | 否 |
| `TestRatePool_CapHitFlag`（-short skip） | RPS 遠超 cap | `GeneratorWorkerCapHit==true` | 邊界（cap） |
| `TestMeasurementConfidence_CapHit` | sum.GeneratorWorkerCapHit=true、無丟樣本 | level=medium、note 含「worker 上限」 | 否 |
| CO 既有測試 | （不變） | **全綠**（語義未破壞） | 回歸守 |

### Edge Cases Checklist
- [x] 低延遲：峰值 worker ≪ cap（省記憶體）
- [x] 達 cap：背壓出現、CO 照常修正、capHit=true
- [x] assertion 失敗不 retry
- [x] ctx 取消時 worker 正確 idle-- 並退出
- [ ] 不適用：VU 模式（未改）

---

## Validation Commands
```bash
gofmt -l internal/engine/ internal/metrics/ internal/reporter/
go vet ./...
go test -race ./internal/engine/... -v        # 重點：CO 測試 + 新池測試
go test -race ./...                            # 全回歸
go test -short ./...                           # 計時案跳過仍綠
```
EXPECT: 全綠；CO 測試不退步；race 乾淨。

### Manual Validation
- [ ] `ramplio mock-server --latency 50ms &` 後 `ramplio run --url ... --rps 500 --duration 20s`：若超出產生器供給，「量測可信度」出現 worker 上限歸因
- [ ] 低 RPS 正常跑，結論不變

---

## Acceptance Criteria
- [ ] CO 既有測試全綠（語義未破壞）
- [ ] 低延遲 rate 測試峰值 worker ≪ cap
- [ ] 達 cap 時 `GeneratorWorkerCapHit` 為真並反映在可信度判語
- [ ] assertion 失敗有測試證明不 retry、計為 error
- [ ] `go test -race ./...` 無回歸
- [ ] analysis.md 技術債狀態更新

## Risks
| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| grow-on-demand 破壞 CO 背壓語義 | 中 | 高 | maybeGrow 只在未達 cap 時生 worker；達 cap 才阻塞；CO 測試當回歸守，紅就回退設計 |
| wg.Add 與 wg.Wait 並發 panic | 中 | 高 | spawn 僅初始迴圈 + dispatcher；`<-dispDone` 後才 wg.Wait()，杜絕並發 Add |
| idle/total 非一致快照導致誤判 | 低 | 低 | 影響僅 ±1 worker（受 cap 限），send 進 buffer 老化 ~µs 遠低於 CO 門檻 |
| cap-hit 誤把合法用滿打成低可信 | 低 | 中 | 僅降到 medium + 歸因 note，不打 low |

## Notes
- 方向經 AskUserQuestion 確認＝「鞏固設計+透明度」。核心立場：analysis 標的兩項是**可辯護設計**，照字面修反而傷準確度/CO。
- 與前兩階段一線相承：P0 立定位、P1 給一鍵自證、P2 讓「高壓下到底是誰的瓶頸」變透明——都是公信力主線。
- 若 Task 1/2 的 grow-on-demand 在 -race 下出現難解競態，**退路**：保留 eager 池，只做 Task 3/4 的「cap-hit 透明度」+ Task 5 的 assertion 釘樁（低風險、仍交付透明度價值），並在此註記取捨。

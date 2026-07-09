// Package baseline 提供壓測與容量探測結果的持久化快照,
// 作為跨執行比較(容量回歸守門)的資料基礎。
// 儲存格式為穩定排序的縮排 JSON,可直接 commit 進 repo 做趨勢追蹤。
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/machiko/ramplio/v3/internal/discover"
	"github.com/machiko/ramplio/v3/internal/metrics"
)

// SchemaVersion 是目前的儲存格式版本;讀到更新的版本時拒絕解讀,
// 避免舊版 binary 默默誤讀新格式。
// 演進策略:新增可選欄位不 bump;欄位語意變更必須 bump 並提供遷移。
const SchemaVersion = 1

// Baseline 是單次執行結果的自包含快照。Metrics 與 Discover 至少其一非 nil
// (Save/Load 皆強制此不變量)。
type Baseline struct {
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	// GitCommit 由呼叫端(cmd 層)填充;本套件是純資料轉換,不感知版控狀態。
	GitCommit string `json:"git_commit,omitempty"`
	// Scenario 是執行對象的識別字串:場景模式為檔名,discover 模式為目標 URL。
	Scenario string `json:"scenario,omitempty"`

	Metrics  *MetricsSnapshot  `json:"metrics,omitempty"`
	Discover *DiscoverSnapshot `json:"discover,omitempty"`
}

// MetricsSnapshot 摘錄 metrics.Summary 中適合跨執行比較的欄位。
// 延遲一律以毫秒浮點數表示,避免 time.Duration 的 JSON 序列化歧義。
type MetricsSnapshot struct {
	Total         int64   `json:"total"`
	Errors        int64   `json:"errors"`
	ErrorRatePct  float64 `json:"error_rate_pct"`
	ThroughputRPS float64 `json:"throughput_rps"`

	P50Ms float64 `json:"p50_ms"`
	P90Ms float64 `json:"p90_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`

	// rate 模式的 coordinated-omission 修正值;VU 模式下 HasCorrected 為 false。
	// Phase 1 守門只比 P99,故僅收 CorrectedP99;要守其他分位數時再擴充(新增欄位不需 bump schema)。
	CorrectedP99Ms float64 `json:"corrected_p99_ms,omitempty"`
	HasCorrected   bool    `json:"has_corrected,omitempty"`

	// 量測可信度欄位:比較引擎據此判斷這份快照的數字本身可不可信,
	// 不可信的 baseline 拿來比較會把「量測劣化」誤判成「目標系統退化」。
	DroppedSamples        int64 `json:"dropped_samples,omitempty"`
	GeneratorWorkerCapHit bool  `json:"generator_worker_cap_hit,omitempty"`
}

// DiscoverSnapshot 摘錄容量探測的守門關鍵數字。
type DiscoverSnapshot struct {
	SafeLimitRPS     int  `json:"safe_limit_rps"`
	BreakingPointRPS int  `json:"breaking_point_rps"`
	Exhausted        bool `json:"exhausted,omitempty"`
}

// FromSummary 由一次壓測結果建立快照。scenario 為場景識別(檔名或 URL)。
func FromSummary(s metrics.Summary, scenario string) Baseline {
	ms := float64(time.Millisecond)
	return Baseline{
		SchemaVersion: SchemaVersion,
		CreatedAt:     time.Now().UTC(),
		Scenario:      scenario,
		Metrics: &MetricsSnapshot{
			Total:          s.Total,
			Errors:         s.Errors,
			ErrorRatePct:   s.ErrorRate(),
			ThroughputRPS:  s.RPS(),
			P50Ms:          float64(s.P50) / ms,
			P90Ms:          float64(s.P90) / ms,
			P95Ms:          float64(s.P95) / ms,
			P99Ms:          float64(s.P99) / ms,
			CorrectedP99Ms: float64(s.CorrectedP99) / ms,
			HasCorrected:   s.HasCorrected,

			DroppedSamples:        s.DroppedSamples,
			GeneratorWorkerCapHit: s.GeneratorWorkerCapHit,
		},
	}
}

// FromDiscover 由一次容量探測結果建立快照。target 為探測目標 URL。
func FromDiscover(d discover.DiscoverResult, target string) Baseline {
	return Baseline{
		SchemaVersion: SchemaVersion,
		CreatedAt:     time.Now().UTC(),
		Scenario:      target,
		Discover: &DiscoverSnapshot{
			SafeLimitRPS:     d.SafeLimit,
			BreakingPointRPS: d.BreakingPoint,
			Exhausted:        d.Exhausted,
		},
	}
}

// validate 強制 Baseline 的核心不變量;守門資料壞了必須大聲失敗。
func validate(b Baseline, path string) error {
	if b.Metrics == nil && b.Discover == nil {
		return fmt.Errorf("baseline %s 無效: Metrics 與 Discover 不可同時為 nil", path)
	}
	return nil
}

// Save 以縮排 JSON 寫入 path,結尾補換行(git 友善)。
// 先寫暫存檔再 rename 原子替換,中斷不會留下半截的 baseline。
func Save(path string, b Baseline) error {
	if err := validate(b, path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 baseline 失敗: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("寫入暫存檔 %s 失敗: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("寫入 baseline %s 失敗: %w", path, err)
	}
	return nil
}

// Load 讀取並驗證 baseline 檔案。
func Load(path string) (Baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Baseline{}, fmt.Errorf("讀取 baseline %s 失敗: %w", path, err)
	}
	return parse(raw, path)
}

// Parse 解析並驗證 baseline 位元組(上傳情境:dashboard 收 bytes 沒有路徑)。
// 與 Load 共用同一套驗證——壞資料必須大聲失敗,不可默默收下。
func Parse(raw []byte) (Baseline, error) {
	return parse(raw, "(上傳內容)")
}

// parse 是 Load/Parse 的共用主體;source 只用於錯誤訊息定位。
func parse(raw []byte, source string) (Baseline, error) {
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return Baseline{}, fmt.Errorf("解析 baseline %s 失敗(非有效 JSON): %w", source, err)
	}
	if b.SchemaVersion > SchemaVersion {
		return Baseline{}, fmt.Errorf(
			"baseline %s 的 schema 版本 %d 比本工具支援的 %d 新,請升級 ramplio",
			source, b.SchemaVersion, SchemaVersion)
	}
	if err := validate(b, source); err != nil {
		return Baseline{}, err
	}
	return b, nil
}

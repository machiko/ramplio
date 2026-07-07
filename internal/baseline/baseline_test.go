package baseline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machiko/ramplio/v2/internal/discover"
	"github.com/machiko/ramplio/v2/internal/metrics"
)

func sampleSummary() metrics.Summary {
	return metrics.Summary{
		Total:        1000,
		Errors:       10,
		BytesIn:      2048,
		WallTime:     10 * time.Second,
		P50:          50 * time.Millisecond,
		P90:          90 * time.Millisecond,
		P95:          95 * time.Millisecond,
		P99:          120 * time.Millisecond,
		CorrectedP99: 200 * time.Millisecond,
		HasCorrected: true,

		DroppedSamples:        7,
		GeneratorWorkerCapHit: true,
	}
}

func TestFromSummaryMapsFields(t *testing.T) {
	b := FromSummary(sampleSummary(), "testdata/example.yaml")

	if b.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", b.SchemaVersion, SchemaVersion)
	}
	if b.Scenario != "testdata/example.yaml" {
		t.Fatalf("Scenario = %q", b.Scenario)
	}
	m := b.Metrics
	if m == nil {
		t.Fatal("Metrics 不可為 nil")
	}
	if m.Total != 1000 || m.Errors != 10 {
		t.Fatalf("Total/Errors = %d/%d", m.Total, m.Errors)
	}
	if m.ErrorRatePct != 1.0 {
		t.Fatalf("ErrorRatePct = %v, want 1.0", m.ErrorRatePct)
	}
	if m.P99Ms != 120 {
		t.Fatalf("P99Ms = %v, want 120", m.P99Ms)
	}
	if !m.HasCorrected || m.CorrectedP99Ms != 200 {
		t.Fatalf("CorrectedP99Ms = %v (has=%v), want 200 (true)", m.CorrectedP99Ms, m.HasCorrected)
	}
	if m.ThroughputRPS != 100 { // 1000 req / 10s
		t.Fatalf("ThroughputRPS = %v, want 100", m.ThroughputRPS)
	}
	// 量測可信度欄位必須跟著快照走:比較引擎要能判斷
	// 「這份 baseline 的數字本身可不可信」。
	if m.DroppedSamples != 7 || !m.GeneratorWorkerCapHit {
		t.Fatalf("可信度欄位未收錄: dropped=%d capHit=%v", m.DroppedSamples, m.GeneratorWorkerCapHit)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := Save(path, FromSummary(sampleSummary(), "x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("Save 完成後不應殘留 .tmp 暫存檔(原子替換)")
	}
}

func TestSaveRejectsEmptyBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	err := Save(path, Baseline{SchemaVersion: SchemaVersion})
	if err == nil {
		t.Fatal("Metrics 與 Discover 皆 nil 的 baseline 應拒絕寫入")
	}
}

func TestLoadRejectsEmptyBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Metrics 與 Discover 皆 nil 的 baseline 應拒絕讀入,守門不可默默通過")
	}
}

func TestFromDiscoverMapsFields(t *testing.T) {
	d := discover.DiscoverResult{SafeLimit: 950, BreakingPoint: 1000, Exhausted: false}
	b := FromDiscover(d, "https://example.com")

	if b.Discover == nil {
		t.Fatal("Discover 不可為 nil")
	}
	if b.Discover.SafeLimitRPS != 950 || b.Discover.BreakingPointRPS != 1000 {
		t.Fatalf("SafeLimit/Breaking = %d/%d", b.Discover.SafeLimitRPS, b.Discover.BreakingPointRPS)
	}
	if b.Scenario != "https://example.com" {
		t.Fatalf("Scenario = %q", b.Scenario)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	want := FromSummary(sampleSummary(), "roundtrip")

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Scenario != want.Scenario || got.SchemaVersion != want.SchemaVersion {
		t.Fatalf("round-trip 中繼資料不一致: %+v vs %+v", got, want)
	}
	if *got.Metrics != *want.Metrics {
		t.Fatalf("round-trip Metrics 不一致:\n got=%+v\nwant=%+v", *got.Metrics, *want.Metrics)
	}
}

func TestSaveIsGitFriendly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := Save(path, FromSummary(sampleSummary(), "x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "\n  ") {
		t.Fatal("輸出應為縮排 JSON(git diff 友善)")
	}
	if !strings.HasSuffix(content, "\n") {
		t.Fatal("檔案應以換行結尾(POSIX / git 慣例)")
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("讀不存在的檔案應回傳錯誤")
	}
}

func TestLoadCorruptJSONReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("損壞的 JSON 應回傳錯誤")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("錯誤訊息應含檔案路徑供除錯: %v", err)
	}
}

func TestLoadNewerSchemaReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("讀取未來 schema 版本應回傳錯誤,而非默默誤讀")
	}
}

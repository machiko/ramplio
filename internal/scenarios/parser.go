package scenarios

import (
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Parse decodes a scenario from r and validates it. Returns a ready-to-use Scenario
// with all Stage.Duration fields populated.
func Parse(r io.Reader) (*Scenario, error) {
	var sc Scenario
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&sc); err != nil {
		return nil, fmt.Errorf("decoding scenario: %w", err)
	}
	// Protocol 在解析期正規化為小寫:下游(engine stepExecutor)一律嚴格
	// 比對,`protocol: WebSocket` 若不正規化會被靜默當 HTTP 執行。
	for _, steps := range [][]Step{sc.Steps, sc.Setup, sc.Teardown} {
		for i := range steps {
			steps[i].Protocol = strings.ToLower(steps[i].Protocol)
		}
	}
	if err := validate(&sc); err != nil {
		return nil, err
	}
	for i := range sc.Stages {
		d, err := time.ParseDuration(sc.Stages[i].DurationRaw)
		if err != nil {
			return nil, fmt.Errorf("stage %d: invalid duration %q: %w", i, sc.Stages[i].DurationRaw, err)
		}
		sc.Stages[i].Duration = d
	}
	for _, sec := range stepSections(&sc) {
		for i := range sec.steps {
			if sec.steps[i].PauseRaw == "" {
				continue
			}
			d, err := time.ParseDuration(sec.steps[i].PauseRaw)
			if err != nil {
				return nil, fmt.Errorf("%s %d (%q): invalid pause %q: %w", sec.name, i, sec.steps[i].Name, sec.steps[i].PauseRaw, err)
			}
			sec.steps[i].Pause = d
		}
	}
	return &sc, nil
}

type stepSection struct {
	name  string // prefix used in error messages
	steps []Step
}

// stepSections lists every step list in a scenario, in a fixed order so the
// same broken scenario always reports the same first error. All of them get
// the same validation and pause parsing.
func stepSections(sc *Scenario) []stepSection {
	return []stepSection{{"step", sc.Steps}, {"setup step", sc.Setup}, {"teardown step", sc.Teardown}}
}

// ParseFile reads and parses a scenario YAML file at path.
func ParseFile(path string) (*Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening scenario file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

func validate(sc *Scenario) error {
	if len(sc.Stages) == 0 {
		return fmt.Errorf("scenario must have at least one stage")
	}
	if len(sc.Steps) == 0 {
		return fmt.Errorf("scenario must have at least one step")
	}
	hasVU, hasRPS := false, false
	for i, stage := range sc.Stages {
		if stage.DurationRaw == "" {
			return fmt.Errorf("stage %d: duration is required", i)
		}
		if stage.Target < 0 {
			return fmt.Errorf("stage %d: target must be >= 0, got %d", i, stage.Target)
		}
		if stage.TargetRPS < 0 {
			return fmt.Errorf("stage %d: target_rps must be >= 0, got %d", i, stage.TargetRPS)
		}
		if stage.Target > 0 && stage.TargetRPS > 0 {
			return fmt.Errorf("stage %d: target and target_rps are mutually exclusive", i)
		}
		if stage.Target > 0 {
			hasVU = true
		}
		if stage.TargetRPS > 0 {
			hasRPS = true
		}
	}
	if hasVU && hasRPS {
		return fmt.Errorf("cannot mix target (VU mode) and target_rps (rate mode) stages in the same scenario")
	}
	// setup/teardown 與 steps 適用同一套檢查:設定錯誤在開跑前擋下,
	// 不是等 setup 執行時被靜默略過。
	for _, sec := range stepSections(sc) {
		for i, step := range sec.steps {
			if err := validateStep(sec.name, i, step); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStep(section string, i int, step Step) error {
	if step.URL == "" {
		return fmt.Errorf("%s %d (%q): url is required", section, i, step.Name)
	}
	if step.Method == "" {
		return fmt.Errorf("%s %d (%q): method is required", section, i, step.Name)
	}
	// ws_mode 是設定錯誤,必須在解析期擋下,不浪費一輪壓測。
	switch step.WSMode {
	case "", "per_request", "persistent":
	default:
		return fmt.Errorf("%s %d (%q): ws_mode 必須是 per_request 或 persistent,得到 %q", section, i, step.Name, step.WSMode)
	}
	if step.WSMode != "" && !strings.EqualFold(step.Protocol, "websocket") {
		return fmt.Errorf("%s %d (%q): ws_mode 僅適用於 protocol: websocket 的步驟", section, i, step.Name)
	}
	// stream 同屬設定錯誤,解析期擋下(比照 ws_mode 慣例)。
	switch step.Stream {
	case "", "sse":
	default:
		return fmt.Errorf("%s %d (%q): stream 目前僅支援 sse,得到 %q", section, i, step.Name, step.Stream)
	}
	if step.Stream != "" && strings.EqualFold(step.Protocol, "websocket") {
		return fmt.Errorf("%s %d (%q): stream 僅適用於 HTTP 步驟", section, i, step.Name)
	}
	// 無效的 regex capture 開跑後每個請求都會編譯失敗、永遠不填值,
	// 卻沒有任何錯誤——驗證在解析期。
	if step.Capture != nil {
		// 固定順序:同一步驟有多個壞 capture 時,每次指名同一個。
		for _, key := range slices.Sorted(maps.Keys(step.Capture.Values)) {
			expr := step.Capture.Values[key]
			if pattern, ok := strings.CutPrefix(expr, "regex:"); ok {
				re, err := regexp.Compile(pattern)
				if err != nil {
					return fmt.Errorf("%s %d (%q): capture %q has an invalid regex: %w", section, i, step.Name, key, err)
				}
				// applyCaptures stores the first group; without one the capture
				// could never fill a value.
				if re.NumSubexp() == 0 {
					return fmt.Errorf("%s %d (%q): capture %q regex needs a capture group, e.g. id=(\\d+)", section, i, step.Name, key)
				}
			}
		}
	}
	// body_matches 也是執行期才編譯:無效 pattern 會讓每個請求斷言失敗。
	if step.Assertions != nil && step.Assertions.BodyMatches != nil {
		if _, err := regexp.Compile(*step.Assertions.BodyMatches); err != nil {
			return fmt.Errorf("%s %d (%q): invalid body_matches pattern: %w", section, i, step.Name, err)
		}
	}
	return nil
}

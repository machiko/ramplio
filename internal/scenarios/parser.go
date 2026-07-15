package scenarios

import (
	"fmt"
	"io"
	"os"
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
	for i := range sc.Steps {
		if sc.Steps[i].PauseRaw == "" {
			continue
		}
		d, err := time.ParseDuration(sc.Steps[i].PauseRaw)
		if err != nil {
			return nil, fmt.Errorf("step %d (%q): invalid pause %q: %w", i, sc.Steps[i].Name, sc.Steps[i].PauseRaw, err)
		}
		sc.Steps[i].Pause = d
	}
	return &sc, nil
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
	for i, step := range sc.Steps {
		if step.URL == "" {
			return fmt.Errorf("step %d (%q): url is required", i, step.Name)
		}
		if step.Method == "" {
			return fmt.Errorf("step %d (%q): method is required", i, step.Name)
		}
		// ws_mode 是設定錯誤,必須在解析期擋下,不浪費一輪壓測。
		switch step.WSMode {
		case "", "per_request", "persistent":
		default:
			return fmt.Errorf("step %d (%q): ws_mode 必須是 per_request 或 persistent,得到 %q", i, step.Name, step.WSMode)
		}
		if step.WSMode != "" && !strings.EqualFold(step.Protocol, "websocket") {
			return fmt.Errorf("step %d (%q): ws_mode 僅適用於 protocol: websocket 的步驟", i, step.Name)
		}
		// stream 同屬設定錯誤,解析期擋下(比照 ws_mode 慣例)。
		switch step.Stream {
		case "", "sse":
		default:
			return fmt.Errorf("step %d (%q): stream 目前僅支援 sse,得到 %q", i, step.Name, step.Stream)
		}
		if step.Stream != "" && strings.EqualFold(step.Protocol, "websocket") {
			return fmt.Errorf("step %d (%q): stream 僅適用於 HTTP 步驟", i, step.Name)
		}
	}
	return nil
}

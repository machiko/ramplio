package scenarios

import (
	"fmt"
	"io"
	"os"
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
	return &sc, nil
}

// ParseFile reads and parses a scenario YAML file at path.
func ParseFile(path string) (*Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening scenario file: %w", err)
	}
	defer f.Close()
	return Parse(f)
}

func validate(sc *Scenario) error {
	if len(sc.Stages) == 0 {
		return fmt.Errorf("scenario must have at least one stage")
	}
	if len(sc.Steps) == 0 {
		return fmt.Errorf("scenario must have at least one step")
	}
	for i, stage := range sc.Stages {
		if stage.DurationRaw == "" {
			return fmt.Errorf("stage %d: duration is required", i)
		}
		if stage.Target < 0 {
			return fmt.Errorf("stage %d: target must be >= 0, got %d", i, stage.Target)
		}
	}
	for i, step := range sc.Steps {
		if step.URL == "" {
			return fmt.Errorf("step %d (%q): url is required", i, step.Name)
		}
		if step.Method == "" {
			return fmt.Errorf("step %d (%q): method is required", i, step.Name)
		}
	}
	return nil
}

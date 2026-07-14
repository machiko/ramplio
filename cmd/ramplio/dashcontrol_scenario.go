package main

// dashController 的場景載入面:GUI 上傳/CLI 預載 YAML 場景的解析與暫存。
// 步驟轉換一律走 scenarioStepsToRamp(與 CLI 單一來源)——這裡曾有一份
// 漂移的內嵌實作,WS 欄位經 GUI 上傳會靜默遺失。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/machiko/ramplio/v3/internal/dashboard"
	"github.com/machiko/ramplio/v3/internal/engine"
	"github.com/machiko/ramplio/v3/internal/scenarios"
)

// setScenario loads a YAML scenario into the controller so the browser can display
// its metadata and start it by sending POST /api/run with an empty body.
func (c *dashController) setScenario(
	meta *dashboard.ScenarioMeta,
	steps, setupSteps, teardownSteps []engine.RampStep,
	stages []scenarios.Stage,
	vars map[string]string,
	dataRows []map[string]string,
	dataMode string,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scenarioMeta = meta
	c.pendingSteps = steps
	c.pendingSetupSteps = setupSteps
	c.pendingTeardownSteps = teardownSteps
	c.pendingStages = stages
	c.pendingVars = vars
	c.pendingDataRows = dataRows
	c.pendingDataMode = dataMode
}

func (c *dashController) ScenarioInfo() *dashboard.ScenarioMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scenarioMeta
}

// LoadScenario parses raw YAML and replaces the active scenario. Rejected while
// a test is running so the browser always sees a consistent state.
// scenarioDir is used to resolve relative paths in vars_from; pass "" to use cwd.
func (c *dashController) LoadScenario(yaml []byte, scenarioDir string) error {
	c.mu.RLock()
	running := c.state == dashboard.StateRunning
	c.mu.RUnlock()
	if running {
		return fmt.Errorf("cannot load scenario while a test is running; stop it first")
	}

	sc, err := scenarios.Parse(bytes.NewReader(yaml))
	if err != nil {
		return fmt.Errorf("invalid scenario YAML: %w", err)
	}

	steps := scenarioStepsToRamp(sc.Steps)
	setupSteps := scenarioStepsToRamp(sc.Setup)
	teardownSteps := scenarioStepsToRamp(sc.Teardown)
	stepNames := make([]string, len(steps))
	for i := range steps {
		stepNames[i] = steps[i].Name
	}

	var dataRows []map[string]string
	var dataMode string
	if sc.VarsFrom != nil && sc.VarsFrom.File != "" {
		dataFile := sc.VarsFrom.File
		if !filepath.IsAbs(dataFile) {
			base := scenarioDir
			if base == "" {
				base, _ = os.Getwd()
			}
			dataFile = filepath.Join(base, dataFile)
		}
		rows, err := scenarios.LoadDataFile(dataFile)
		if err != nil {
			return fmt.Errorf("loading data file %q: %w", sc.VarsFrom.File, err)
		}
		dataRows = rows
		dataMode = sc.VarsFrom.Mode
	}

	maxVUs := maxTarget(sc.Stages)
	var totalSec float64
	for _, stg := range sc.Stages {
		totalSec += stg.Duration.Seconds()
	}
	meta := &dashboard.ScenarioMeta{
		Name:          sc.Name,
		StepNames:     stepNames,
		MaxVUs:        maxVUs,
		TotalSec:      totalSec,
		StageCount:    len(sc.Stages),
		SetupCount:    len(sc.Setup),
		TeardownCount: len(sc.Teardown),
	}
	c.setScenario(meta, steps, setupSteps, teardownSteps, sc.Stages, sc.Vars, dataRows, dataMode)
	return nil
}

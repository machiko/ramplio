package discover

import (
	"context"
	"time"

	"github.com/ramplio/ramplio/internal/engine"
	"github.com/ramplio/ramplio/internal/metrics"
	"github.com/ramplio/ramplio/internal/protocols"
	"github.com/ramplio/ramplio/internal/scenarios"
)

// Config controls the capacity discovery run.
type Config struct {
	URL           string
	Method        string        // default "GET"
	Tolerance     time.Duration // p99 threshold for a passing probe; default 2s
	MaxRPS        int           // stop probing above this rate; default 500
	ProbeDuration time.Duration // how long each probe runs; default 15s
	HTTPConfig    protocols.HTTPConfig
}

// ProbeStatus classifies a single probe result.
type ProbeStatus int

const (
	ProbePass ProbeStatus = iota
	ProbeWarn
	ProbeFail
)

// ProbeResult is the outcome of a single RPS-level probe.
type ProbeResult struct {
	RPS       int
	P99       time.Duration
	ErrorRate float64
	Total     int64
	Status    ProbeStatus
}

// DiscoverResult is the final capacity report.
type DiscoverResult struct {
	Probes        []ProbeResult
	SafeLimit     int  // highest PASS RPS (0 = even the first probe failed)
	BreakingPoint int  // first FAIL RPS (0 = never failed within MaxRPS)
	Exhausted     bool // true when all probes passed without hitting MaxRPS limit's fail
}

// Prober runs capacity discovery probes against a single HTTP endpoint.
type Prober struct {
	cfg      Config
	executor *protocols.HTTPExecutor // shared across probes to reuse TCP/TLS connections
}

// New creates a Prober with sane defaults applied.
func New(cfg Config) *Prober {
	if cfg.Method == "" {
		cfg.Method = "GET"
	}
	if cfg.Tolerance == 0 {
		cfg.Tolerance = 2 * time.Second
	}
	if cfg.MaxRPS == 0 {
		cfg.MaxRPS = 500
	}
	if cfg.ProbeDuration == 0 {
		cfg.ProbeDuration = 15 * time.Second
	}
	return &Prober{
		cfg:      cfg,
		executor: protocols.NewHTTPExecutor(cfg.HTTPConfig),
	}
}

// baseSequence is the default RPS probe ladder.
// Chosen to give useful resolution at low, medium, and high rates.
var baseSequence = []int{5, 10, 20, 40, 75, 125, 200, 300, 500, 750, 1000, 1500, 2000}

// ProbeSequence returns the RPS levels that will be tested, capped at maxRPS.
// If maxRPS falls between two base values, it is appended at the end.
func ProbeSequence(maxRPS int) []int {
	var seq []int
	for _, v := range baseSequence {
		if v > maxRPS {
			break
		}
		seq = append(seq, v)
	}
	if len(seq) == 0 || seq[len(seq)-1] != maxRPS {
		seq = append(seq, maxRPS)
	}
	return seq
}

// Run executes probes in ascending RPS order.
// onProbeStart is called before each probe begins (may be nil).
// onProbe is called after each probe completes.
// Stops early on the first ProbeFail.
func (p *Prober) Run(ctx context.Context, onProbeStart func(rps int), onProbe func(ProbeResult)) DiscoverResult {
	seq := ProbeSequence(p.cfg.MaxRPS)
	result := DiscoverResult{}

	for _, rps := range seq {
		if ctx.Err() != nil {
			break
		}
		if onProbeStart != nil {
			onProbeStart(rps)
		}
		pr := p.probe(ctx, rps)
		result.Probes = append(result.Probes, pr)
		if onProbe != nil {
			onProbe(pr)
		}
		switch pr.Status {
		case ProbePass:
			result.SafeLimit = rps
		case ProbeFail:
			result.BreakingPoint = rps
			return result
		}
		// ProbeWarn: keep going but don't update SafeLimit
	}

	// All probes completed without a failure.
	if result.BreakingPoint == 0 && len(result.Probes) > 0 {
		result.Exhausted = true
	}
	return result
}

func (p *Prober) probe(ctx context.Context, targetRPS int) ProbeResult {
	req := protocols.Request{
		Method: p.cfg.Method,
		URL:    p.cfg.URL,
	}

	workerCount := targetRPS * 5
	if workerCount < 10 {
		workerCount = 10
	}
	if workerCount > 5000 {
		workerCount = 5000
	}

	col := metrics.NewCollector(workerCount)
	eng := engine.NewRamp(engine.RampConfig{
		// SetupSteps run once before the timed probe to warm up the TCP/TLS
		// connection. The result is NOT counted in the probe metrics.
		SetupSteps: []engine.RampStep{{Request: req}},
		Stages:     []scenarios.Stage{{Duration: p.cfg.ProbeDuration, TargetRPS: targetRPS}},
		Steps:      []engine.RampStep{{Request: req}},
		Executor:   p.executor,
	}, col)

	sum := eng.Run(ctx)

	if sum.Total == 0 {
		return ProbeResult{RPS: targetRPS, Status: ProbeFail}
	}

	p99 := sum.P99
	errRate := sum.ErrorRate()

	return ProbeResult{
		RPS:       targetRPS,
		P99:       p99,
		ErrorRate: errRate,
		Total:     sum.Total,
		Status:    classify(p99, errRate, p.cfg.Tolerance),
	}
}

// classify determines probe status.
// FAIL: p99 exceeds 1.5× tolerance OR error rate ≥ 3%.
// WARN: p99 exceeds tolerance OR error rate ≥ 1%.
// PASS: everything within tolerance.
func classify(p99 time.Duration, errorRate float64, tolerance time.Duration) ProbeStatus {
	if p99 > tolerance*3/2 || errorRate >= 3.0 {
		return ProbeFail
	}
	if p99 > tolerance || errorRate >= 1.0 {
		return ProbeWarn
	}
	return ProbePass
}

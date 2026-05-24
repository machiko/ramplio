# Changelog

All notable changes to Ramplio are documented here.
Versions follow [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

---

## [v0.6.0] — Hardening

### Added
- **Prometheus metrics endpoint** (`--prometheus :9100`): expose live metrics in
  Prometheus text format during a run. Metrics include `ramplio_requests_total`,
  `ramplio_errors_total`, `ramplio_error_rate_pct`, `ramplio_rps`,
  `ramplio_latency_p50/p90/p99_ms`, `ramplio_mean_latency_ms`,
  `ramplio_active_vus`, and `ramplio_elapsed_seconds`.
- **DNS cache** (`--dns-cache`): TTL-based DNS lookup cache (default 60 s TTL)
  prevents repeated DNS resolution from inflating per-request latency.
- **Per-request timeout** (`--timeout`): override the scenario's `request_timeout_ms`
  from the CLI without editing the YAML file.
- **HTTP connection pool tuning via YAML** (`http.max_idle_conns`,
  `http.max_idle_conns_per_host`, `http.request_timeout_ms`): tune the transport
  per-scenario for high-VU workloads.
- **`HTTPExecutor.CloseIdleConnections()`**: explicit cleanup of keep-alive
  connections after a run, used by the memory stability tests.
- **Memory stability tests** (`TestMemoryStability`, `TestRampMemoryStability`):
  run 50 VUs for 5 s and verify no goroutine leaks after the engine stops.
- **Documentation**: `docs/getting-started.md` and `docs/scenario-schema.md`.

### Fixed
- `prometheus.go`: replaced `fmt.Fprintf` with `fmt.Fprint` for non-constant
  format strings (go vet warning).

---

## [v0.5.0] — Web Dashboard

### Added
- **Live web dashboard** (`--dashboard`, `--dashboard-port`): Vue 3 + Chart.js
  SPA served by the Go binary via `embed.FS`. Shows RPS, latency percentiles,
  error rate, and active VU count as live time-series charts.
- **WebSocket endpoint** (`/ws/metrics`): pushes `LiveSnapshot` JSON every 500 ms;
  auto-reconnects on disconnect.
- Dashboard shuts down cleanly when the engine context is cancelled.

---

## [v0.4.0] — Live Terminal Dashboard

### Added
- **TUI live dashboard** (bubbletea + lipgloss): real-time terminal view
  displaying stage progress bar, RPS, p99, error rate, and active VU count;
  refreshes every second.
- Graceful Ctrl+C handling: cancels the engine and prints the full summary.
- `reporter.LiveProvider` interface and `LiveSnapshot` struct shared between the
  TUI and web dashboard.

---

## [v0.3.0] — Rich Metrics & Reporting

### Added
- **HDR histogram** (`hdrhistogram-go`): replaces simple min/max with accurate
  p50/p90/p95/p99 percentiles.
- **JSON output** (`--output results.json`): serialize `metrics.Summary` to disk.
- **Threshold exit codes**: exit `1` when `error_rate_pct` or `p99_ms` thresholds
  are exceeded (CI-friendly).
- `metrics.Collector.LiveSummary()` and `LivePercentiles()`: thread-safe reads
  for the TUI and dashboard.

---

## [v0.2.0] — Scenario DSL

### Added
- **YAML scenario files** (`--scenario`): declare stages, steps, assertions, and
  thresholds in a single file.
- **`ramplio validate`** command: parse and validate a scenario without running.
- **RampEngine** (`internal/engine/ramp.go`): multi-stage VU ramping with linear
  interpolation between `target` values.
- **Per-step assertions**: `status` code check; failures counted as errors.
- `scenarios.ParseFile()` validates duration strings and VU counts at parse time.

---

## [v0.1.0] — Core Engine MVP

### Added
- `ramplio run --url --vus --duration`: minimal one-liner load test.
- `internal/engine`: fixed VU pool with context cancellation.
- `internal/protocols`: HTTP executor with configurable method, headers, and body.
- `internal/metrics`: buffered channel collector + aggregator goroutine.
- `internal/reporter`: terminal summary table printed after each run.

---

## [v0.0.1] — Bootstrap

### Added
- Go module (`github.com/ramplio/ramplio`).
- Full directory skeleton: `cmd/`, `internal/`, `docs/`, `testdata/`.
- `golangci-lint` configuration (`.golangci.yml`).
- GitHub Actions CI pipeline (lint + test + race detector).
- `Makefile` with `build`, `test`, `lint`, `run` targets.
- `testdata/example.yaml`: first scenario fixture.

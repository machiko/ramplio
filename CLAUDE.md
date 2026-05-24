# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Role

You are a **professional senior automation test engineer** specializing in performance and stress testing infrastructure. Approach every decision with these priorities: test accuracy, measurement reliability, concurrency correctness, and actionable reporting. Tools like k6, Vegeta, Artillery, and Locust are your reference points — this project aims to be in their class.

## Project Purpose

**Ramplio** is a developer-first stress testing service for HTTP APIs and websites. It generates configurable load (ramp-up, sustained, spike, soak), collects real-time performance metrics, and produces structured reports. The goal is a clean CLI experience with a YAML-driven scenario DSL and an optional live dashboard.

## Tech Stack

- **Language**: Go — chosen for goroutine-based concurrency, single-binary distribution, and performance parity with the load under test
- **CLI framework**: `cobra` + `viper`
- **Config format**: YAML (scenarios) + environment variables (secrets/overrides)
- **Metrics storage**: in-process with `hdrhistogram` for latency percentiles; optional Prometheus export
- **Dashboard**: optional Vue 3 frontend served by the Go binary (embedded via `embed.FS`)
- **Testing**: `testing` stdlib + `testify` for assertions; `httptest` for protocol handler unit tests

## Architecture

```
ramplio/
├── cmd/                   # cobra CLI commands (run, validate, report)
├── internal/
│   ├── engine/            # Core load orchestrator — manages worker pools, ramping logic
│   ├── scenarios/         # YAML scenario parser and validator
│   ├── protocols/         # Protocol handlers: HTTP, WebSocket (future: gRPC)
│   ├── metrics/           # Collector, aggregator, HDR histogram, percentile math
│   └── reporter/          # Output formatters: terminal, JSON, HTML
├── dashboard/             # Vue 3 SPA for live metrics (embedded into binary)
├── config/                # Shared config types and defaults
└── testdata/              # Fixture YAML scenarios used in tests
```

### Key Design Contracts

- **Engine ↔ Protocol**: the engine calls `protocols.Executor` interface; each protocol implements `Execute(ctx, Request) Result` — this is the single extension point for new protocols.
- **Engine ↔ Metrics**: workers emit `metrics.Sample` structs onto a buffered channel; the collector drains and aggregates independently of the worker hot path.
- **Scenarios**: a scenario file declares stages (duration + target VU count), steps (requests with assertions), and thresholds (pass/fail criteria). Validation happens at parse time, not at runtime.
- **Reporter**: reporters consume a `metrics.Summary` snapshot — they are read-only and have no coupling to the engine.

### Concurrency Model

Each virtual user (VU) runs in its own goroutine. The engine controls VU count per stage via a semaphore/worker pool. Metrics samples are written to a `chan metrics.Sample` (buffered, capacity = max VUs × 10) and consumed by a single aggregator goroutine. Context cancellation propagates cleanly through all layers.

## Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run a single test
go test ./internal/engine/... -run TestRampUp

# Run tests with race detector (required before any commit touching concurrency)
go test -race ./...

# Lint
golangci-lint run

# Run a scenario
go run ./cmd/ramplio run --scenario testdata/example.yaml

# Validate a scenario file without running
go run ./cmd/ramplio validate --scenario testdata/example.yaml
```

## Scenario DSL (YAML)

```yaml
name: API smoke load
duration: 2m
stages:
  - duration: 30s
    target: 50      # ramp to 50 VUs
  - duration: 60s
    target: 50      # hold
  - duration: 30s
    target: 0       # ramp down

steps:
  - name: GET homepage
    method: GET
    url: https://example.com/
    assertions:
      - status: 200
      - latency_p95_ms: 500

thresholds:
  error_rate_pct: 1.0
  p99_ms: 1000
```

## Metrics Collected

| Metric | Description |
|--------|-------------|
| `latency_p50/p90/p95/p99` | Response time percentiles (HDR histogram) |
| `throughput_rps` | Requests per second |
| `error_rate_pct` | % of non-2xx or assertion-failed responses |
| `active_vus` | Current virtual user count |
| `bytes_received` | Total payload received |

## Project Docs

| 文件 | 說明 |
|------|------|
| `docs/tech-decisions.md` | 技術選型決策記錄，含每個選型的原因與取捨 |
| `docs/roadmap.md` | 開發生命週期與歷程，Milestone 0–6 的交付項目與 Definition of Done |

## Testing Approach

- Unit test every `metrics` calculation independently — percentile math is easy to get wrong.
- Use `net/http/httptest` to test protocol handlers without real network calls.
- Integration tests in `testdata/` spin up a local `httptest.Server` and run a full scenario end-to-end.
- The race detector (`-race`) must pass for any code touching shared state.

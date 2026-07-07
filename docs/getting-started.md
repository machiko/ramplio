# Getting Started with Ramplio

## Installation

```bash
go install github.com/machiko/ramplio/v2/cmd/ramplio@latest
```

Or build from source:

```bash
git clone https://github.com/machiko/ramplio.git
cd ramplio
make build         # outputs ./ramplio binary
```

Verify:

```bash
ramplio --version
```

---

## Quickstart: One-liner load test

Hit any URL with 10 virtual users for 30 seconds:

```bash
ramplio run --url https://httpbin.org/get --vus 10 --duration 30s
```

Sample output:

```
Running 10 VUs for 30s → GET https://httpbin.org/get

 Requests  │ 1 420
 Errors    │ 0  (0.00%)
 RPS       │ 47.3
 Mean      │ 210ms
 p50       │ 195ms
 p90       │ 340ms
 p95       │ 410ms
 p99       │ 520ms
 Duration  │ 30.01s
```

---

## Quickstart: Scenario file

Create `smoke.yaml`:

```yaml
name: API smoke
stages:
  - duration: 15s
    target: 10    # ramp up to 10 VUs
  - duration: 30s
    target: 10    # hold
  - duration: 15s
    target: 0     # ramp down

steps:
  - name: GET homepage
    method: GET
    url: https://httpbin.org/get
    assertions:
      status: 200

thresholds:
  error_rate_pct: 1.0
  p99_ms: 1000
```

Run it:

```bash
ramplio run --scenario smoke.yaml
```

---

## CLI Reference

### `ramplio run`

| Flag | Default | Description |
|------|---------|-------------|
| `--url`, `-u` | — | Target URL (mutually exclusive with `--scenario`) |
| `--scenario`, `-s` | — | Path to YAML scenario file |
| `--vus` | `1` | Number of virtual users (URL mode only) |
| `--duration`, `-d` | `30s` | Test duration — e.g. `30s`, `2m` (URL mode only) |
| `--method` | `GET` | HTTP method |
| `--header`, `-H` | — | Repeatable header: `-H "Authorization: Bearer token"` |
| `--body` | — | Request body string |
| `--output`, `-o` | — | Save results to JSON file |
| `--timeout` | `30s` | Per-request timeout; overrides scenario default |
| `--dns-cache` | `false` | Cache DNS lookups to reduce latency noise |
| `--dashboard` | `false` | Open live web dashboard |
| `--dashboard-port` | `9999` | Dashboard HTTP port |
| `--prometheus` | — | Expose Prometheus metrics — e.g. `:9100` |

### `ramplio validate`

Validates a scenario YAML file without running a load test:

```bash
ramplio validate --scenario smoke.yaml
```

---

## Live Dashboard

Pass `--dashboard` to open a browser-based real-time dashboard:

```bash
ramplio run --scenario smoke.yaml --dashboard
# Dashboard → http://localhost:9999
```

The dashboard shows RPS, latency percentiles (p50/p90/p99), error rate, and
active VU count as time-series charts. No extra software required — it is served
directly by the Ramplio binary.

---

## Prometheus Integration

Expose metrics for Grafana / alerting:

```bash
ramplio run --scenario smoke.yaml --prometheus :9100
# Prometheus → http://:9100/metrics
```

Available metrics: `ramplio_requests_total`, `ramplio_errors_total`,
`ramplio_error_rate_pct`, `ramplio_rps`, `ramplio_latency_p50_ms`,
`ramplio_latency_p90_ms`, `ramplio_latency_p99_ms`,
`ramplio_mean_latency_ms`, `ramplio_active_vus`, `ramplio_elapsed_seconds`.

---

## CI Integration

Scenarios exit with code `1` when a threshold is exceeded, making them
suitable for CI/CD gates:

```yaml
# .github/workflows/perf.yml
- name: Smoke load test
  run: ramplio run --scenario testdata/smoke.yaml
```

If `error_rate_pct` or `p99_ms` thresholds are breached, the step fails and
the pipeline stops.

---

## DNS Cache

By default Ramplio resolves DNS on every new connection. Add `--dns-cache`
to cache lookups (60s TTL) when you want latency measurements that reflect
only application and network overhead:

```bash
ramplio run --url https://api.example.com/health --vus 50 --duration 60s --dns-cache
```

---

## Save and replay results

```bash
# Save results to JSON
ramplio run --scenario smoke.yaml --output results.json

# Print summary from a saved result
ramplio report --input results.json
```

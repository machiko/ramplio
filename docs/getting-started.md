# Getting Started with Ramplio

## Installation

```bash
go install github.com/machiko/ramplio/v3/cmd/ramplio@latest
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

Hit a URL with 10 virtual users for 30 seconds. Point it at your own service,
or use the built-in mock server for a zero-dependency first run:

```bash
ramplio mock-server --port 8080 --latency 20ms &   # or use your own URL below
ramplio run --url http://localhost:8080/ --vus 10 --duration 30s
```

Sample output (abridged):

```
✓ 預檢通過：http://localhost:8080/（HTTP 200，總共 32ms）
Running 10 VUs for 30s → GET http://localhost:8080/

測試結果
────────────
  總請求數：                6772
  測試時長：                30.00s
  每秒請求：                225.7

延遲分佈
────────────
  平均：                  22ms
  p50：                 21ms
  p90：                 23ms
  p95：                 24ms
  p99：                 26ms

回應狀態
────────────
  成功 (2xx)：            6772 (100.0%)
  失敗：                  0 (0.0%)

量測可信度
───────────────
  ✓ 高：量測過程中產生器沒有丟樣本、GC 干擾也很低，數字可信。

測試結果說明
──────────────────
  整體結論：✓ 網站很健康，可以放心上線
  ...
  一句話總結：整體來說，網站又快又穩，可以放心。
```

Every run ends with a plain-language verdict (overall conclusion, speed,
stability, capacity headroom) and a **measurement confidence** section that
tells you whether the numbers themselves can be trusted — if the load
generator dropped samples or was disturbed by GC, Ramplio says so instead of
letting you read a distorted result.

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
    url: http://localhost:8080/    # swap in your service URL
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
| `--rps` | — | Target requests per second — rate mode (mutually exclusive with `--vus`) |
| `--save-baseline` | — | Save this run's result as a baseline snapshot for `ramplio compare` |
| `--observe` | — | Pull traces from the target's APM after the run for bottleneck correlation — e.g. `jaeger://localhost:16686?service=checkout` (rate mode only) |
| `--strict-trust` | `false` | Treat untrustworthy observation (fetch failure / truncation / insufficient correlation) as failure; requires `--observe` |
| `--trace-context` | `false` | Inject a W3C `traceparent` header into every request so the target's APM can tag load-test traffic (~63ns per request, opt-in) |
| `--sink` | — | Push results to an external sink (repeatable): `csv:<file>`, `influxdb://…`, `loki://…`, `otel://host:4318` |

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

## Capacity regression gate (`--save-baseline` + `compare`)

Thresholds catch absolute failures; the regression gate catches *relative*
ones — "did this change make us slower than last release?". Save a snapshot
from a known-good run, then compare every subsequent run against it:

```bash
# On the known-good build: save a baseline snapshot (git-friendly JSON)
ramplio run --url http://localhost:8080/ --rps 100 --duration 60s --save-baseline base.json

# After your change: run again, then compare
ramplio run --url http://localhost:8080/ --rps 100 --duration 60s --save-baseline after.json
ramplio compare base.json after.json
```

Sample output:

```
容量回歸比較(本次 vs 基準)
────────────────────────────
  ✓ p50（伺服器處理）     22ms → 22ms(-0.2%,持平)
  ✓ p99（伺服器處理）     24ms → 22ms(-7.1%,持平)
  ✓ 錯誤率            0.0% → 0.0%(+0.0%,持平)
  ✓ 每秒請求           15 → 15(-0.0%,持平)
  ✓ p99（使用者實感）     25ms → 26ms(+1.8%,持平)

  結論:✓ 沒有超出容差的退步,整體持平或更好。
```

`compare` exits `1` on any regression, so it slots straight into CI as a
merge gate. Verdicts use a **dual-threshold tolerance** (relative % *and* an
absolute floor must both be exceeded) so normal run-to-run noise does not
produce false alarms; tighten with `--rel-tolerance-pct`. If either snapshot's
measurement is questionable (dropped samples, generator at its worker cap),
warnings are always printed — add `--strict-trust` to make questionable
measurements fail the gate outright.

`discover` also supports `--save-baseline`, so you can gate on discovered
capacity (`safe_limit_rps` / `breaking_point_rps`) instead of a fixed-rate run.

---

## Bottleneck correlation (`--observe`)

A load test tells you *that* the target slowed down; `--observe` asks the
target's own tracing backend *where*. After a rate-mode run, Ramplio pulls
traces from Jaeger or Tempo, compares per-operation p95 between the ramp-up
window (baseline) and the sustained window (stress), and names the operation
that degraded most:

```bash
ramplio run --url http://localhost:8080/checkout --rps 200 --duration 2m \
  --observe "jaeger://localhost:16686?service=checkout"

# Grafana Tempo works the same way:
#   --observe "tempo://localhost:3200?service=checkout"
# (jaegers:// / tempos:// for HTTPS)
```

Honesty rules, by design: with insufficient samples it reports "correlation
insufficient" instead of guessing; operations excluded from comparison are
listed by name; when everything slows down uniformly it reports "suspected
resource saturation" rather than blaming a single operation; truncated
sampling is always disclosed. Requires `--rps` (the rate profile provides the
comparison windows).

Pair it with `--trace-context` to inject W3C `traceparent` headers so the
APM can tag which traces came from the load test. To export the final
metrics to an OpenTelemetry collector, add `--sink otel://localhost:4318`.

---

## Prove the tool itself (`verify`)

Why trust Ramplio's numbers at all? `verify` stresses a built-in mock server
with a *known* injected latency distribution and checks that the measured
percentiles land within tolerance of that ground truth — no external target,
no comparing against other tools:

```bash
ramplio verify
ramplio verify --latency 100ms --tolerance 30ms
ramplio verify --latency-fast 10ms --latency-slow 200ms --slow-pct 10   # bimodal tail
```

Sample output:

```
  量測自證 — 對已知延遲分佈施壓，反推 Ramplio 量得準不準
  注入分佈：固定 50ms    施壓：10 VU × 3s    容差：±20ms

  量測結果（注入值 → 量到值）
    p50            50ms → 50ms    ✓
    p99            50ms → 53ms    ✓

  ✓ 量測準確：所有百分位都落在注入值 +0~20ms 內。
```

Measured values can only be ≥ the injected value (local round-trip adds
overhead) — a measurement *below* it would mean Ramplio under-reports
latency, which is a bug. Exits `0` when accurate, `1` otherwise, so you can
run it in CI before trusting any load-test result.

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

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

## Quickstart: One command, capacity answer

Give Ramplio a URL — or paste a request copied from your browser — and it
probes how much traffic the endpoint can take:

```bash
ramplio https://example.com          # same as: ramplio discover --url https://example.com
pbpaste | ramplio -                  # DevTools → Network → right-click → Copy as cURL
ramplio 'curl https://api.example.com/me -H "Authorization: Bearer …"'
```

- Headers and body from the curl are sent on every probe request; the terminal
  only shows header **names** (values are masked as `***`).
- Write methods (POST/PUT/PATCH/DELETE, including Chrome's implicit POST from
  `--data-raw`) are **not** load tested by default: Ramplio sends one preflight,
  prints the result and tells you to re-run with
  `pbpaste | ramplio discover --curl - --allow-write` if you really mean it.
- `ramplio x.yaml` / `ramplio x.har` print the right command
  (`run --scenario` / `import`) instead of running; a mistyped subcommand such
  as `ramplio dicover` gets a suggestion and never starts traffic.

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

  反應速度  ⚡ 非常快（幾乎即時）
      99% 的人點下去後，26 毫秒內就看到回應。
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

## Generate your first scenario (`ramplio init`)

Don't want to hand-write YAML? The guided wizard asks a few questions and
produces a ready-to-run scenario file — including login setup (session cookie
or JWT), multiple steps, load shape, and pass/fail thresholds:

```bash
ramplio init
```

The wizard walks through: basic settings → login (optional) → **variable
parameters** (optional) → test steps → load settings → thresholds → output.

### Variable parameters: a different value per virtual user

Real APIs behave differently when every request hits the same `user_id`
(caches flatter your numbers). Declare a variable parameter in the wizard and
Ramplio generates a companion data file, so each virtual user draws different
values. Reference a column in any path or body as `{{data.column_name}}`:

```
【變動參數 / 資料檔（可選）】
  讓每個虛擬用戶帶不同的參數值（例如不同 user_id、搜尋關鍵字）。
  接下來設定測試步驟時，在路徑或 body 中用 {{data.欄位名}} 引用，ramplio 會自動生成資料檔。
  要設定變動參數嗎？ [y/N]
  > y
    參數欄位名稱？（例如 user_id、keyword）
  > user_id
  這個欄位的值怎麼產生？
  [1] 遞增數字（1, 2, 3…，適合 ID）
  [2] 隨機 UUID
  [3] Email（loadtest+1@example.com…）
  [4] 自訂清單（從你提供的值中循環挑選）
  [5] 留空範本（產生 <欄名> 佔位，之後自己填真實資料）
```

Answering `user_id` (遞增數字) and a step path of `/api/users/{{data.user_id}}`
produces two files (real wizard output):

```yaml
# scenario.yaml
name: 商品 API 煙霧測試

vars:
  base_url: "http://localhost:8080"

vars_from:
  file: data.csv
  mode: random

stages:
  - duration: 30s
    target: 20
  - duration: 1m
    target: 20
  - duration: 30s
    target: 0

steps:
  - name: "GET /api/users/{{data.user_id}}"
    method: GET
    url: "{{vars.base_url}}/api/users/{{data.user_id}}"
    assertions:
      status: 200
```

```csv
# data.csv (200 rows generated)
user_id
1
2
3
```

The wizard cross-checks your declarations against your steps at generation
time: referencing an undeclared `{{data.X}}` column (or declaring a column no
step uses) prints a warning immediately, instead of every request failing at
run time.

Then validate and run as usual:

```bash
ramplio validate --scenario scenario.yaml
ramplio run --scenario scenario.yaml
```

### Generate a scenario in the dashboard

The same wizard lives in the web dashboard: start `ramplio run --dashboard`,
pick **進階：用精靈產生情境檔** on the home screen, and fill in the form. You
can download the generated `scenario.yaml` + `data.csv`, or press **直接開跑**
to load them straight into memory and run — the data file never needs to touch
the server's disk, so this also works against a remote dashboard.

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

Besides running tests, the dashboard can also discover your capacity limit
(探測容量上限), guide non-technical users through a test (帶我設定), and
**generate scenario files** with the built-in wizard — see
[Generate your first scenario](#generate-your-first-scenario-ramplio-init).

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

Without arguments, `ramplio compare` uses the automatic run records instead
(latest record vs the previous record of the same target) — see
[Save and replay results](#save-and-replay-results).

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

Every `run` / `discover` automatically saves a run record to `.ramplio/runs/`
in the current directory (the latest 50 are kept; the directory ignores itself
via its own `.gitignore`). At the end of each run the result is compared with
the previous run of the **same target and settings** (mode, method, masked URL,
a hash of headers + body, and load flags; scenario mode looks at the scenario
file path only, not its content). Only the first 8 hex chars of a sha256 of the
headers and body are stored — never their values — so rotating a token starts a
new series. That comparison is informational only — it
never changes the exit code.

```bash
ramplio run --url http://localhost:8080/ -d 30s   # run the same command twice
ramplio compare                                   # latest record vs the previous one of the same target (exit 1 on regression)
ramplio report                                    # HTML report from the latest run record
```

Use `--no-history` to skip recording, or `RAMPLIO_HISTORY_DIR` to keep records
elsewhere. A failure to save (read-only directory, full disk) only prints a
warning. URL credentials are masked in records. Corrupt records, or records
written by a newer ramplio, are skipped with a warning but never deleted
automatically.

The header/body hash is **not encryption**: it is unsalted and only 8 hex
chars, so short tokens or PINs can be brute-forced offline. Do not publish
`.ramplio/` records or baseline files. The self-ignoring `.gitignore` is only
written when the history directory belongs to ramplio (newly created, or
holding nothing but `runs/`); if `RAMPLIO_HISTORY_DIR` points at a directory
with other files, ramplio writes nothing and asks you once to ignore `runs/`
yourself.

The explicit flags still work when you want to manage file names yourself:

```bash
# Save results to JSON
ramplio run --scenario smoke.yaml --output results.json

# Print summary from a saved result
ramplio report --input results.json
```

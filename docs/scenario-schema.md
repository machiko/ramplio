# Scenario Schema Reference

Scenario files are YAML documents that describe a complete load test. Pass them
with `ramplio run --scenario <file>` or validate with `ramplio validate --scenario <file>`.

---

## Top-level fields

```yaml
name: string        # Human-readable scenario name (displayed at runtime)
stages: [Stage]     # Ordered list of load stages  (required)
steps:  [Step]      # Ordered list of HTTP requests (required, at least one)
thresholds:         # Optional pass/fail criteria
  error_rate_pct: float
  p99_ms: float
http:               # Optional HTTP connection pool overrides
  max_idle_conns: int
  max_idle_conns_per_host: int
  request_timeout_ms: int
```

---

## Stage

Each stage defines a duration and a target VU count. Ramplio linearly
interpolates the VU count between consecutive stage targets (ramp-up /
ramp-down). To hold a constant load, set the same `target` in two adjacent
stages.

```yaml
stages:
  - duration: 30s   # string — Go duration format: 10s, 1m30s, 2m
    target: 50      # int — number of virtual users at the END of this stage
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `duration` | string | yes | Stage duration. Accepts Go duration strings: `10s`, `1m`, `2m30s`. |
| `target` | int | yes | VU count at the end of this stage. Interpolation starts from the previous stage's target (or 0 for the first stage). |

**Example — ramp up, hold, ramp down:**

```yaml
stages:
  - duration: 30s
    target: 100   # 0 → 100 VUs over 30 s
  - duration: 2m
    target: 100   # hold 100 VUs for 2 min
  - duration: 30s
    target: 0     # 100 → 0 VUs over 30 s
```

---

## Step

Steps define the HTTP requests sent during the test. Each VU cycles through all
steps in order and repeats from the beginning.

```yaml
steps:
  - name: string              # Label shown in logs / reports
    method: string            # HTTP method: GET POST PUT DELETE PATCH HEAD OPTIONS
    url: string               # Full URL including scheme
    headers:                  # Optional key-value map
      Content-Type: application/json
      Authorization: Bearer token
    body: string              # Optional request body (raw string)
    assertions:               # Optional per-request pass/fail check
      status: int             # Expected HTTP status code
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | no | Display label. Defaults to `METHOD URL`. |
| `method` | string | yes | Case-insensitive. |
| `url` | string | yes | Must include scheme (`http://` or `https://`). |
| `headers` | map | no | Merged with default headers. `Content-Type` is not set automatically. |
| `body` | string | no | Sent as-is. Set `Content-Type` explicitly when sending JSON. |
| `assertions.status` | int | no | If set, a response with a different status is counted as an error. |

**Example — POST with JSON body:**

```yaml
steps:
  - name: Create order
    method: POST
    url: https://api.example.com/orders
    headers:
      Content-Type: application/json
      Authorization: Bearer secret
    body: '{"item":"widget","qty":1}'
    assertions:
      status: 201
```

### WebSocket steps

Set `protocol: websocket` to exchange one text frame per step execution
(send `ws_message`, read one frame back). The handshake success status is
reported as `101` and is not counted as an error.

```yaml
steps:
  - name: ws echo
    method: GET
    url: ws://localhost:8080/echo
    protocol: websocket
    ws_message: ping          # text frame to send
    ws_expect: pong           # substring the reply must contain
    ws_mode: persistent       # connection strategy (see below)
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `protocol` | string | no | `http` (default) or `websocket`. |
| `ws_message` | string | no | Text frame sent after the handshake. Falls back to `body` when both set. |
| `ws_expect` | string | no | The reply must contain this substring, otherwise the step counts as an error. |
| `ws_mode` | string | no | `per_request` (default) opens a fresh connection per exchange; `persistent` reuses one connection per VU for the VU's lifetime. |

`ws_mode: persistent` removes the per-exchange handshake cost (locally measured
~180µs → ~24µs per exchange) and avoids ephemeral-port exhaustion under high
rates. A dropped connection surfaces as an error for that exchange — it is a
real event the test should record — and the next exchange redials automatically.
`ws_expect` mismatches keep the (healthy) connection open. Setup/teardown steps
always use per-request connections: they run once and have no VU lifetime.

---

## Capture

Extract values from a response and store them in the VU's variable context.
Captured values are referenced in subsequent steps with `{{capture.key}}`.

```yaml
capture:
  key: expression
```

| Expression prefix | What it extracts | Example |
|-------------------|-----------------|---------|
| `$.path` | JSONPath from response body | `$.data.token` |
| `header:Name` | First value of a response header | `header:X-Request-Id` |
| `cookie:name` | Value of a specific `Set-Cookie` cookie | `cookie:session` |
| `regex:(pat)` | First capture group of a regex on the body | `regex:token=([a-z0-9]+)` |

Captures accumulate within a VU for the lifetime of the test. Each VU has its
own isolated capture state; values do not leak between VUs.

**Example — login and reuse JWT:**

```yaml
steps:
  - name: POST /auth/login
    method: POST
    url: https://api.example.com/auth/login
    headers:
      Content-Type: application/json
    body: '{"email":"user@example.com","password":"pass"}'
    assertions:
      status: 200
    capture:
      jwt: "$.access_token"

  - name: GET /profile
    method: GET
    url: https://api.example.com/profile
    auth:
      bearer: "{{capture.jwt}}"
    assertions:
      status: 200
```

**Example — extract and reuse a session cookie:**

```yaml
steps:
  - name: POST /auth/refresh
    method: POST
    url: https://example.com/auth/refresh
    headers:
      Cookie: "session={{data.session_cookie}}"
    capture:
      new_session: "cookie:session"
    assertions:
      status: 200

  - name: GET /dashboard
    method: GET
    url: https://example.com/dashboard
    headers:
      Cookie: "session={{capture.new_session}}"
    assertions:
      status: 200
```

---

## Auth

Shorthand for injecting authentication headers. Applied after `headers`, so it
overrides any `Authorization` header set explicitly.

```yaml
auth:
  bearer: "{{capture.jwt}}"   # injects: Authorization: Bearer <value>
```

```yaml
auth:
  basic:
    username: admin
    password: "{{env \"ADMIN_PASS\"}}"   # injects: Authorization: Basic <base64>
```

---

## Thresholds

Thresholds define pass/fail criteria evaluated after the test finishes.
When a threshold is exceeded, Ramplio exits with code `1`.

```yaml
thresholds:
  error_rate_pct: 1.0   # fail if error rate exceeds 1%
  p99_ms: 500           # fail if p99 latency exceeds 500 ms
```

| Field | Type | Description |
|-------|------|-------------|
| `error_rate_pct` | float | Maximum allowed error percentage (0–100). |
| `p99_ms` | float | Maximum allowed p99 latency in milliseconds. |

Both fields are optional. If neither is set, Ramplio exits with `1` when any
errors occur.

---

## HTTP connection pool (`http`)

Override the default HTTP transport settings per-scenario. Useful for high-VU
scenarios that need a larger connection pool, or for tests where you want strict
per-request timeout control.

```yaml
http:
  max_idle_conns: 2000          # default: 1000
  max_idle_conns_per_host: 2000 # default: 1000
  request_timeout_ms: 5000      # default: 30000 (30 s)
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_idle_conns` | int | 1000 | Maximum total idle (keep-alive) connections across all hosts. |
| `max_idle_conns_per_host` | int | 1000 | Maximum idle connections per target host. |
| `request_timeout_ms` | int | 30000 | Per-request timeout in milliseconds. Overridden by `--timeout` CLI flag. |

---

## Complete example

```yaml
name: Full API load test

stages:
  - duration: 30s
    target: 50
  - duration: 2m
    target: 50
  - duration: 30s
    target: 0

steps:
  - name: Health check
    method: GET
    url: https://api.example.com/health
    assertions:
      status: 200

  - name: List items
    method: GET
    url: https://api.example.com/items
    headers:
      Authorization: Bearer mytoken
    assertions:
      status: 200

  - name: Create item
    method: POST
    url: https://api.example.com/items
    headers:
      Content-Type: application/json
      Authorization: Bearer mytoken
    body: '{"name":"test","value":42}'
    assertions:
      status: 201

thresholds:
  error_rate_pct: 0.5
  p99_ms: 800

http:
  max_idle_conns: 2000
  max_idle_conns_per_host: 2000
  request_timeout_ms: 10000
```

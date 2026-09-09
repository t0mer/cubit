# Cubit

Cubit reads your Israeli **Cibus / Pluxee** meal-card balance and tells you how
many fixed-denomination vouchers it buys.

```
Cibus balance : ₪273.50
Voucher value : ₪50.00
Affordable    : 5 vouchers
Remainder     : ₪23.50
```

It is a small self-hosted Go service: one static binary, an 11 MB `scratch`
container, a JSON API, and Prometheus metrics.

> **Stage 1.** Cubit reports. It does **not** buy anything — no cart, no orders,
> no scheduling. Purchasing is deliberately out of scope.

---

## How logging in works

Pluxee sends a one-time code out of band, so Cubit cannot log in unattended. It
starts a login, parks in `AWAITING_OTP`, and waits for you to hand it the code:

```
IDLE ──login──> AWAITING_OTP ──otp──> AUTHENTICATED
  ^                  │                      │
  └── ttl / attempts ─┘   ── 401 / expiry ──┘
```

The resulting session is **encrypted to disk with AES-256-GCM**, so a restart
normally goes straight back to `AUTHENTICATED` without a new code. That is what
makes the service usable day to day.

## Quick start

```bash
docker run -d --name cubit \
  -p 8080:8080 \
  -v cubit-data:/data \
  -e CUBIT_PLUXEE_USERNAME='you@example.com' \
  -e CUBIT_PLUXEE_PASSWORD='...' \
  -e CUBIT_AUTH_ENCRYPTION_KEY="$(head -c 32 /dev/urandom | base64)" \
  techblog/cubit:latest
```

On boot Cubit starts a login and logs the masked destination the code went to.
Submit the code:

```bash
curl -X POST http://localhost:8080/api/v1/auth/otp \
     -H 'Content-Type: application/json' \
     -d '{"code":"123456"}'
```

Then read the balance:

```bash
curl -s http://localhost:8080/api/v1/balance | jq
```

**Keep the volume.** It holds the encrypted session. Lose it — or lose
`CUBIT_AUTH_ENCRYPTION_KEY` — and the next start needs a fresh code.

### docker compose

```bash
export CIBUS_USER='you@example.com' CIBUS_PASS='...'
export CUBIT_KEY="$(head -c 32 /dev/urandom | base64)"
docker compose up -d
```

See [`docker-compose.yml`](docker-compose.yml). It uses a **named volume**, not a
bind mount: the image runs as uid 10001 and a root-owned host directory would not
be writable.

## HTTP API

All endpoints are JSON. Application routes live under `/api/v1`.

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/v1/auth/credentials` | Supply Cibus credentials at runtime |
| `POST` | `/api/v1/auth/login` | Begin login; triggers the OTP |
| `POST` | `/api/v1/auth/otp` | Submit the OTP code |
| `GET`  | `/api/v1/auth/status` | Current state, challenge details, whether credentials are set |
| `GET`  | `/api/v1/balance` | Balance and voucher calculation |
| `GET`  | `/healthz` | Liveness |
| `GET`  | `/readyz` | Ready only when `AUTHENTICATED` |
| `GET`  | `/metrics` | Prometheus |
| `GET`  | `/api/docs` | Swagger UI |

`/api/v1` and `/metrics` are guarded by `CUBIT_SERVER_API_TOKEN` when it is set;
`/healthz` and `/readyz` are always open so orchestrator probes keep working.

### Interactive documentation

Swagger UI is served at **`/api/docs`**, with the OpenAPI 3 document behind it at
`/api/docs/openapi.yaml`. Both stay open even when an API token is configured:
the specification is documentation, not data, and reading how to authenticate
should not require having authenticated.

The UI assets are embedded in the binary rather than pulled from a CDN, so the
docs work on a host with no outbound internet, and a page that handles
credentials loads no third-party JavaScript.

`internal/apidocs/openapi.yaml` is the committed source of truth. A test walks
the router and fails the build if a route is added without documenting it, or
documented without existing.

### `POST /api/v1/auth/otp`

```json
{ "code": "123456" }
```

| Status | Meaning |
|---|---|
| `200` | Authenticated |
| `400` | Malformed body or empty code |
| `401` | Wrong code — the body carries `attempts_remaining` |
| `409` | No OTP is pending |
| `410` | The challenge expired; start a new login |

### `GET /api/v1/balance`

```json
{
  "balance_agorot": 27350,
  "balance": "273.50",
  "currency": "ILS",
  "voucher_value_agorot": 5000,
  "voucher_value": "50.00",
  "vouchers_affordable": 5,
  "remainder_agorot": 2350,
  "remainder": "23.50",
  "restaurant_id": "31999",
  "checked_at": "2026-09-08T14:22:31+03:00"
}
```

Returns `503` when not authenticated, with the current state in the body so the
caller can tell whether an OTP is pending.

### `POST /api/v1/auth/login`

`202` with the masked destination when an OTP was sent, `200` if the backend
authenticated outright. A second login while one is already pending returns
`409` — it will not silently send you another SMS. `412` when no credentials are
held yet.

### `POST /api/v1/auth/credentials`

```json
{ "username": "alice", "password": "secret", "company": "" }
```

Credentials do not have to come from configuration. Post them here instead and
cubit will use them for the next login.

`204` on success, with no body — the password is never echoed back, and never
appears in a log line at any level.

| Code | Meaning |
|---|---|
| `204` | Stored |
| `400` | Malformed JSON, or either half of the pair is empty |
| `409` | A login is awaiting an OTP; answer it or let it expire first |
| `412` | No API token is configured — see below |
| `401` | An API token is configured and the request did not carry it |

**This endpoint refuses to run on an unguarded instance.** Every other route is
open when `CUBIT_SERVER_API_TOKEN` is unset, which is deliberate first-run
behaviour, but an open endpoint that accepts the password to a financial account
is a different proposition — anyone who can reach the port could hand cubit
their own credentials, and yours would cross the wire unprotected. Set a token
first.

**Credentials posted here are held in memory only.** They are never written to
disk, so a restart drops them. The common restart is unaffected: a valid
`token.enc` still reaches `AUTHENTICATED` with no OTP and no credentials. But if
the session has expired, you will need to post them again before logging in.

## Configuration

Precedence: **flags > environment > YAML file > built-in defaults.**

The YAML file is read from `/config/config.yaml` by default; see
[`config/config.yaml.example`](config/config.yaml.example).

### Environment variables

Every setting has a `CUBIT_`-prefixed variable: the config key with dots
replaced by underscores.

| Variable | Default | Purpose |
|---|---|---|
| `CUBIT_PLUXEE_USERNAME` | — | Cibus/Pluxee username; optional if posted to the API |
| `CUBIT_PLUXEE_PASSWORD` | — | Cibus/Pluxee password; optional if posted to the API |
| `CUBIT_AUTH_ENCRYPTION_KEY` | — | **Required.** 32 bytes, raw / hex / base64 |
| `CUBIT_AUTH_ENCRYPTION_KEY_FILE` | — | Path to a key file; wins over the above |
| `CUBIT_PLUXEE_COMPANY` | — | Only if your employer's login asks for it |
| `CUBIT_PLUXEE_RESTAURANT_ID` | `31999` | Restaurant the report is labelled with |
| `CUBIT_PLUXEE_TIMEOUT` | `30s` | Per-request timeout |
| `CUBIT_PLUXEE_LANGUAGE` | `he` | `Accept-Language` sent upstream |
| `CUBIT_PLUXEE_RECAPTCHA_TOKEN` | — | Only if login starts demanding a captcha |
| `CUBIT_VOUCHER_VALUE_AGOROT` | `5000` | Voucher denomination (₪50) |
| `CUBIT_AUTH_AUTOSTART` | `true` | Begin login on boot |
| `CUBIT_AUTH_OTP_TTL` | `5m` | How long a challenge stays valid |
| `CUBIT_AUTH_OTP_MAX_ATTEMPTS` | `3` | Wrong codes before a fresh login is needed |
| `CUBIT_SERVER_API_TOKEN` | — | Guards `/api/v1` and `/metrics`; min 16 chars |
| `CUBIT_SERVER_ADDRESS` | `:8080` | Listen address |
| `CUBIT_DATA_DIR` | `/data` | Where the encrypted session lives |
| `CUBIT_LOG_LEVEL` | `info` | `debug`, `info`, `warning`, `error` |
| `CUBIT_LOG_FORMAT` | `json` | `json` or `text` |

Credentials are accepted from the environment or a mounted file only — never as
a command-line flag, because flags are visible in the process table. They may
also be left unset entirely and posted to
[`/api/v1/auth/credentials`](#post-apiv1authcredentials) at runtime; setting only
one half of the pair is rejected at startup. With neither set, cubit starts in
`IDLE` and waits.

### CLI flags

| Flag | Default | Purpose |
|---|---|---|
| `--config` | `/config/config.yaml` | YAML configuration file |
| `--address` | `:8080` | Listen address |
| `--data-dir` | `/data` | Encrypted session directory |
| `--voucher-value` | `5000` | Voucher denomination in agorot |
| `--restaurant-id` | `31999` | Restaurant to price against |
| `--autostart` | `true` | Begin login on boot |
| `--otp-ttl` | `5m` | Challenge lifetime |
| `--log-level` | `info` | Log level |
| `--log-format` | `json` | Log format |
| `--version` | — | Print the version and exit |
| `--help` | — | Usage |

## The voucher maths

All money is `int64` **agorot** — never `float64`. The API's decimal string is
converted to integers once, at the client boundary, by parsing its digits: `1.15`
is exactly `115` agorot, whereas `1.15 * 100` in binary floating point is
`114.99999999999999`.

```
vouchers  = balance / voucherValue   (integer division)
remainder = balance % voucherValue
```

A zero or negative balance buys nothing and is not an error. A non-positive
denomination is rejected at startup rather than dividing by zero later.

## Metrics

| Metric | Type | Meaning |
|---|---|---|
| `cubit_balance_agorot` | gauge | Last observed balance |
| `cubit_vouchers_affordable` | gauge | Vouchers the last balance covered |
| `cubit_remainder_agorot` | gauge | Leftover after those vouchers |
| `cubit_last_balance_check_timestamp_seconds` | gauge | Last successful check |
| `cubit_balance_checks_total` | counter | By `result` |
| `cubit_auth_state` | gauge | One series per state; exactly one is `1` |
| `cubit_otp_submissions_total` | counter | By `result` |
| `cubit_logins_total` | counter | By `result` |

## Authentication

Set `CUBIT_SERVER_API_TOKEN` and every `/api/v1` and `/metrics` request must
present it, either way round:

```bash
curl -H 'X-API-Token: <token>'   http://localhost:8080/api/v1/balance
curl -u cubit:<token>            http://localhost:8080/api/v1/balance
```

Comparison is constant-time. `/healthz` and `/readyz` stay open for probes.

**If you do not set a token the API is open**, and Cubit warns about it on every
start. That is deliberate first-run behaviour — the service has to be reachable
before it is configured — but an open instance lets anyone who can reach the port
read your balance and make Pluxee send *you* an OTP. Set a token before exposing
it beyond localhost.

## Security

- Credentials, OTP codes and session tokens are **never logged** at any level,
  and are never echoed in an API response. Redaction happens at the logging
  boundary.
- The session is stored **AES-256-GCM encrypted**, file mode `0600`, written
  atomically.
- The container runs as **uid 10001** on `scratch`: no shell, no package
  manager, no libc.
- A rejected login is **never retried**. This talks to a real financial account,
  and a retry loop risks a lockout. Backoff with jitter applies only to `429`
  and `5xx`, capped at three attempts, honouring `Retry-After`.
- OTP submissions are capped (default 3) before a fresh login is required.
- Because the token is required as a header (or basic-auth), a configured token
  also closes the cross-site request path that could otherwise make a browser
  trigger a login.
- `go.mod` requires Go 1.25.13 or newer, the release that fixed the standard
  library advisories `govulncheck` reports against older toolchains.

## A caveat worth reading

Pluxee's API is undocumented. Cubit's understanding of it came from reading the
shipped web app and probing the live endpoints; the notes are in
`docs/api-notes.md` (kept out of git as a local working document).

**One finding governs how Cubit can be operated: the login endpoint enforces
reCAPTCHA.** This was confirmed on 2026-09-09 against a real account. The same
username and password return `210` (OTP sent) from a browser that supplies a
captcha token, and `401` from a plain HTTP client that does not. The endpoint
never mentions the captcha — it answers a bare `401`, indistinguishable from a
wrong password.

Because reCAPTCHA v3 tokens are single-use and expire in about two minutes,
there is no way to capture one and reuse it. `CUBIT_PLUXEE_RECAPTCHA_TOKEN`
exists, but a token pasted into configuration is almost certainly dead before
it is read.

**So Cubit cannot log in unattended.** What it does instead:

- A session, once obtained, is held and re-used — encrypted at rest in
  `token.enc`. Day to day, restarts need no OTP and no captcha.
- Re-authentication, when a session finally expires, is a manual act performed
  with a browser.
- When a login fails for want of a captcha, Cubit returns `412` and says so,
  rather than blaming your password.

Cubit does not solve, bypass, or outsource captchas.

## Building

```bash
go test ./...
go build -ldflags "-X github.com/t0mer/cubit/internal/version.Version=dev" ./cmd/cubit
```

Local development, with credentials from the environment:

```bash
export CUBIT_PLUXEE_USERNAME=... CUBIT_PLUXEE_PASSWORD=...
export CUBIT_AUTH_ENCRYPTION_KEY="$(head -c 32 /dev/urandom | base64)"
./scripts/dev.sh
```

Releases are `workflow_dispatch` only and versioned `YYYY.M.PATCH`. The Release
workflow builds every target with goreleaser; a separate Docker workflow
publishes `linux/amd64`, `linux/arm64` and `linux/arm/v7` images.

## Licence

Apache-2.0. See [LICENSE](LICENSE).

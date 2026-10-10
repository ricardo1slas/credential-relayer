# Release notes

## v1.2.0 (2026-10-09)

### Changed

- **Webhook delivery is now at-least-once (behavior change).** Earlier releases called webhooks durable, but
  workers took jobs with `BRPOP`, so a crash, `kill -9` or a deploy between taking a job and finishing it
  lost the job (or left it stuck in `processing`). Jobs now move atomically into a
  `relayer:jobs:processing` list (`BLMOVE`) and leave it only when completed, requeued or dead-lettered. A
  reaper on every instance requeues claims older than the slowest target's full timeout plus `retry_delay`
  and a minute. The trade-off: a worker that dies after the upstream accepted a job but before marking it
  done gets it redelivered, so the upstream can see it twice.

### Upgrading

- **Webhook targets must tolerate duplicate deliveries** (dedupe on the job ID or make the handler
  idempotent). Jobs carry no idempotency key yet.
- Jobs a v1.1.0 worker is processing at the moment of the upgrade use the old path and can still be lost
  once; upgrade while the queue is idle if that matters.
- No config change. New Redis keys: `relayer:jobs:processing` (list) and `relayer:jobs:claims` (zset).

## v1.1.0 (2026-10-09)

### Fixed

- `RELAYER_*` env overrides can reach keys that contain an underscore, using `__` as the level separator
  (`RELAYER_RATE_LIMIT__FAIL_OPEN` -> `rate_limit.fail_open`). Previously every `_` split a level, so
  those keys could only be set in YAML.
- **Rate limits now hold across instances.** The limiter was an in-memory map per process, so the
  reference AWS deployment (2 ECS tasks behind an ALB) allowed each key twice its configured limit. The
  default backend is now a Redis token bucket (atomic Lua script, Redis `TIME` as the shared clock, idle
  buckets expire after one window), so every instance spends from one budget per key.
- `make deploy-pi` refuses to deploy while Grafana would get the default `admin` password (unset, empty or
  `admin`), and fails closed if the value can't be resolved.
- Builds with Go 1.27.2 and requires Go 1.26.9 or later, picking up the standard library fixes for
  `net/http`, `net/textproto` and `crypto/tls` (GO-2026-6603 to GO-2026-6617, CVE-2026-78667,
  CVE-2026-97031).

### Added

- `rate_limit.backend: redis|memory` (default `redis`) and `rate_limit.fail_open` (default `false`).
- `relayer_ratelimit_backend_errors_total{decision="allowed|denied"}` counts requests decided without the
  backend.
- `SecretProvider` interface for resolving `secret_headers`; the environment is still the only
  implementation and still fails fast on a missing variable.
- Benchmarks run every scenario directly against the upstream as well and report relayer overhead
  (delta p50/p95, throughput ratio).
- CI writes test coverage (total and per package) to the job summary.
- Architecture decision records in `docs/adr/`.

### Changed

- **Behavior change:** with the Redis backend, every `/api/v1` request makes one Redis round trip, and if
  Redis is unavailable the API returns `503 {"error":"rate limiter unavailable"}` (fail-closed). Previously a
  Redis outage only affected webhooks, job status and readiness. Set `rate_limit.fail_open: true` to let requests through
  instead, or `rate_limit.backend: memory` for the old per-process behavior.
- `ratelimit.New`/`*ratelimit.Limiter` became `ratelimit.NewMemory`/`*ratelimit.Memory`, and the middleware is
  now `ratelimit.Middleware(limiter, failOpen, logger, metrics)` (internal packages only).
- `how_to.md` moved to `docs/build-log.md` and was condensed to the dated record of decisions, bugs and
  findings.
- README reorganized: TL;DR, results, limitations and non-goals. "Zero-trust" wording in the build log
  replaced with "tailnet-only, deny-by-default Tailscale ACLs".

### Upgrading

- `RELAYER_*` env overrides: use `__` between levels for any key that contains an underscore
  (`RELAYER_RATE_LIMIT__FAIL_OPEN`, `RELAYER_RELAY__MAX_REQUEST_BYTES`, `RELAYER_QUEUE__COMPLETED_TTL`).
  The single-`_` form mapped those to the wrong key (`RELAYER_RATE_LIMIT_REQUESTS` -> `rate.limit.requests`),
  so they were silently ignored; if you set one expecting it to work, it now must be renamed, and the new
  name *will* take effect, so check the value before deploying. Single-`_` names for keys without
  underscores (`RELAYER_SERVER_PORT`, `RELAYER_QUEUE_REDIS_ADDR`, `RELAYER_QUEUE_REDIS_TLS`) behave as before.
- Pi: make sure `GRAFANA_PASSWORD` is set in `~/relayer/infra/docker/.env`. If the Grafana volume already
  exists, the password there won't change by itself; see `docs/pi-deploy.md` for the reset command.
- Multi-instance deployments get the Redis backend with no config change. Single-instance deployments that
  would rather not depend on Redis for every request can set `rate_limit.backend: memory`.

## v1.0.0 (2026-09-28)

First tagged release: a self-hosted credential broker for APIs and LLMs. Clients hold scoped, revocable
relayer keys; the real upstream secrets live only on the relayer, which injects them per request.

### Features

- **Three entry points behind one `Authorize` gate**: synchronous `POST /api/v1/relay` (retries 5xx and
  transport errors with exponential backoff and jitter, never 4xx), durable `POST /api/v1/webhook` (Redis
  queue, worker pool, dead-letter state, `GET /api/v1/jobs/{id}` scoped to the owning key), and a streaming
  passthrough `/api/v1/proxy/{target}/...` (SSE, no retries) for SDKs and agents such as Gemini CLI and the
  Anthropic SDK.
- **Credential injection** from environment variables named in config (`secret_headers`); the relayer
  refuses to start if one is missing. Injected last so clients can't override them; redacted if an
  upstream echoes them, including across stream chunks.
- **Auth**: SHA-256-hashed relayer keys, constant-time compare, optional expiry, overlapping keys for
  rotation, per-key target scopes, accepted as `X-API-Key` or `x-goog-api-key`.
- **Per-target policy**: allowed methods, path prefixes (judged after `../` resolution; encoded
  separators refused) and body size.
- **SSRF hardening**: targets by name only, dial-time blocking of private/loopback addresses, capped and
  re-validated redirects, HTTPS-only by default.
- **Observability**: structured logs naming the key behind every call, Prometheus metrics with bounded
  labels, four provisioned Grafana dashboards, 30-day availability SLO with multi-window burn-rate alerts,
  Alertmanager email.
- **Go SDK** (`pkg/client`), health/readiness probes, graceful shutdown.
- **Deployment**: Raspberry Pi over Tailscale (tailnet-only, deny-by-default Tailscale ACLs; CI deploys
  only commits that passed `ci`, via an ephemeral tagged node); reference AWS Terraform (VPC, ALB, ECS
  Fargate, ElastiCache, ECR, CloudWatch, GitHub OIDC).

### Performance

- Fixed connection reuse: the per-request timeout context was cancelled before the body was read, so
  `net/http` discarded every connection. Found with `pprof`; 200-concurrency throughput went from 282 to
  1048 req/s (3.7x) on a Raspberry Pi 4. Results in `benchmarks/results/`.

### Security

- Header deny-list now folds `_` to `-` before checking (a bypass found by fuzzing).
- Path policy fuzzed (`FuzzPathPolicy`).
- CI: golangci-lint (incl. gosec), `go test -race`, govulncheck, gitleaks over full history, Trivy image
  and IaC scans, Dependabot.

### Known limitations

- The rate limiter is in-memory per instance: behind the reference AWS setup (2 tasks) the effective
  per-key limit is doubled. Fixed in v1.1.0.

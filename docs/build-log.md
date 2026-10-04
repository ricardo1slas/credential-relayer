# Build Log: Credential Relayer

> The phase-by-phase record of what was decided, what broke, what the security reviews found, and why. Earlier sections describe the project as it was at the time, with dated **Update** notes where later work changed the facts. For the current state, start with the [README](../README.md); the main design decisions are recorded as ADRs in [`adr/`](adr/README.md).
>
> The live deployment moved from AWS to a Raspberry Pi over Tailscale (§15); the AWS sections are kept as the reference path. Phase 12 (§16) is where the project became a credential broker. The project was called `private_relayer` until 2026-09-27; older entries use that name.

---

## Table of Contents

1. [Objective and Origin](#1-objective-and-origin)
2. [Architecture](#2-architecture)
3. [Technology Decisions](#3-technology-decisions)
4. [Phase 1: Scaffolding](#4-phase-1-scaffolding) ✅
5. [Phase 2: Core Relay Logic](#5-phase-2-core-relay-logic) ✅
6. [Phase 3: Authentication & Rate Limiting](#6-phase-3-authentication--rate-limiting) ✅
7. [Phase 4: Redis Queue & Async Processing](#7-phase-4-redis-queue--async-processing) ✅
8. [Phase 5: Docker & Docker Compose](#8-phase-5-docker--docker-compose) ✅
9. [Phase 5.5: Security & Privacy Hardening](#9-phase-55-security--privacy-hardening) ✅
10. [Phase 6: CI with GitHub Actions](#10-phase-6-ci-with-github-actions) ✅
11. [Phase 7: AWS Infrastructure with Terraform](#11-phase-7-aws-infrastructure-with-terraform) ✅
12. [Phase 8: Continuous Deployment](#12-phase-8-continuous-deployment) ✅
13. [Phase 9: Observability & Alerting](#13-phase-9-observability--alerting) ✅
14. [Phase 10: Hardening & Documentation](#14-phase-10-hardening--documentation) ✅
15. [Phase 11: Raspberry Pi, Tailnet-Only Deployment](#15-phase-11-raspberry-pi-tailnet-only-deployment) ✅
16. [Phase 12: Credential Broker: Secrets, Policy, Streaming](#16-phase-12-credential-broker-secrets-policy-streaming) ✅

---

## 1. Objective and Origin

Started (2026-04-09) as a general-purpose request relayer: accept requests from clients, authenticate and rate-limit them, forward them to configured targets with retries and timeouts, queue heavy work for async delivery, and log and measure everything.

Based on [OpenZeppelin Relayer](https://github.com/openzeppelin/openzeppelin-relayer), which relays blockchain transactions. The same patterns (relay, queue, retry, config, metrics) were reused, made chain-agnostic.

> **Update (2026-09-26): what it became.** The project grew from "a smart proxy" into a **credential broker**: upstream secrets live only on the relayer and are injected into outbound requests, every relayer key is scoped to specific targets, each target can restrict methods, paths and body size, and a streaming proxy lets SDKs and agents (Gemini CLI, the Anthropic SDK) use it by changing only their base URL. That also brought it back toward OpenZeppelin's core idea (the relayer holds the key; clients only submit intents), generalized from blockchain signing keys to API secrets. The implementation shares no code with it (Rust there, Go here), and the hard problems differ: transaction lifecycle there, safe credential use over HTTP here. See Phase 12 (§16).

---

## 2. Architecture

**Original design (April 2026).** Router (chi) with auth and rate-limit middleware in front of a synchronous relay handler; an async path (webhook handler, Redis queue, worker pool) beside it; `/health` and `/ready` for the orchestrator; `/metrics` for Prometheus. Planned deployment: AWS VPC across two AZs, ALB in public subnets, ECS Fargate tasks and ElastiCache Redis in private subnets, Secrets Manager, ECR and CloudWatch; CI (lint, test, build, Trivy) and CD (push to ECR, update the ECS task definition) in GitHub Actions.

- **Sync relay:** look up the target by name from config, build the request, send it with a per-request timeout, retry failures with exponential backoff and jitter, return the response.
- **Async webhook:** validate, create a job, enqueue it in Redis, return `202` with a job ID; workers dequeue, relay, update status, retry with backoff, and eventually dead-letter; clients poll `GET /api/v1/jobs/{id}`.

> **Update (2026-09-26): current request flow.** Every entry point now goes through one gate, and the deployment is a Raspberry Pi on Tailscale (§15) rather than AWS:
>
> ```
> client --relayer key--> auth (hashed keys) --> rate limit --> Authorize: key scope, method, path, body policy
>    /relay   --> retries + backoff -----------------\
>    /webhook --> Redis queue --> worker pool --------+--> secret injection --> SSRF-hardened client --> upstream
>    /proxy   --> streaming passthrough (no retries) -/        |                                          |
>                                                              \--- redaction of echoed secrets <---------/
> ```
>
> Refusals, upstream outcomes and stream timing are counted per key and target and shown on four Grafana dashboards (§13). The AWS/Terraform/ECS path is kept as a manual-only reference (`deploy-aws.yml`); the live target is the Pi (`deploy.yml` -> `make deploy-pi`; see [pi-deploy.md](pi-deploy.md)).

---

## 3. Technology Decisions

- **Go** over Node.js, Python or Rust: compiled and fast for a relay, goroutines fit a worker pool, a single static binary makes tiny images, and it is the language of the cloud-native tooling this project targets. Rust (OpenZeppelin's choice) was rejected for its learning curve relative to the goal.
- **chi v5** over gin or stdlib: handlers stay plain `http.HandlerFunc` (gin's `gin.Context` would make every handler, middleware and test framework-specific); stdlib `ServeMux` (even 1.22) lacked middleware chaining and route groups, which are needed to scope auth to `/api/v1`.
- **koanf** over viper: no global singleton (easier testing), fewer dependencies, and the `RELAYER_SERVER_PORT -> server.port` env transform is explicit code. YAML for local defaults, env vars override per environment (12-factor), `setDefaults()` as a final safety net so a missing field never means port 0 or a zero timeout.
- **zerolog** over slog or zap: JSON by default, zero-allocation, fluent typed fields. slog would also have worked; this was a preference call. Every log line carries `request_id` so a single request can be traced.
- **golangci-lint v2** with `errcheck`, `govet`, `staticcheck`, `unused`, `ineffassign` (gosec added in Phase 6). Config in a file so CI and local runs agree.
- **Standard Go layout:** `cmd/` for the entrypoint only, `internal/` for everything private (a compile-time encapsulation guarantee), `pkg/client` for the Go SDK.

---

## 4. Phase 1: Scaffolding

**Goal:** a running service with health endpoints, config loading, structured logging and dev tooling.

> **Update (2026-09-27).** The module is now `github.com/ricardo1slas/credential-relayer`, matching the public repository name; it started as `github.com/ricardo1slas/private_relayer`.

Decisions:

- **Middleware order:** RequestID first (so everything downstream can log it), then the request logger, then Recoverer (so a panic becomes a logged 500). Originally RealIP came second.
  > **Update (2026-09-25).** `middleware.RealIP` was removed: chi deprecated it as spoofable (roadmap T6).
- **`/health` and `/ready` outside `/api/v1`**, so probes never need an API key and auth/rate-limit middleware can be scoped to the API group. `/health` is liveness (never fails while the process answers); `/ready` is readiness (fails when a dependency such as Redis is down, Phase 4).
- **Server timeouts set from day one** (read 10 s, write 10 s, idle 60 s): Go's defaults are unlimited, which invites Slowloris-style connection hoarding. `ReadHeaderTimeout` followed in Phase 5.5.
- **`CONFIG_PATH`** env var to relocate the config file for Docker and tests.

---

## 5. Phase 2: Core Relay Logic

**Goal:** request forwarding with retries and timeouts. 12 tests at completion.

- **Target registry by name.** Clients name a configured target (`"target": "httpbin"`); the URL comes from config. The registry validates names and URLs at startup (fail fast at boot, not at request time) and applies defaults (5 s timeout, 3 retries) so a missing YAML field can't mean a zero timeout or unbounded retries.
- **Retry policy:** retry 5xx and transport errors; never retry 4xx (the request is wrong and will fail the same way) or a cancelled client context (no one is waiting). After all attempts fail, the relay returns 502.
- **Backoff:** `500ms x 2^attempt x jitter`, jitter in [0.5, 1.0], capped at 30 s. Jitter breaks synchronized retry waves; 50-100% rather than full jitter keeps a meaningful minimum wait; the cap stops absurd waits at high attempt counts.
- **Per-attempt `context.WithTimeout`** so a slow target can't hold a goroutine.
  > **Update.** This exact pattern later caused the 3.7x performance bug in §14 (the context was cancelled before the caller read the body).
- **Separate `config.TargetConfig` and `relay.TargetConfig` types**, mapped in `main.go`, so `relay` doesn't import `config`.
- **Tests against `httptest.NewServer`**, a real HTTP server, not mocked interfaces, so transport, headers, bodies and timeouts are exercised for real.

---

## 6. Phase 3: Authentication & Rate Limiting

**Goal:** secure the API and prevent abuse. 9 new tests (21 total).

- **API keys via `X-API-Key`, compared in constant time** (`crypto/subtle`) so response timing can't reveal how much of a guess matched.
- **Only the key's name goes into the request context**, never its value, so the name can be logged, rate-limited and put in metrics without risking the secret. The context key is an unexported struct type to avoid collisions.
- **Token bucket** rate limiter, chosen over fixed window (double bursts at window boundaries), sliding window (per-request timestamps cost memory) and leaky bucket (no bursts, too strict for an API). Refill is computed lazily on each request, no background goroutine. Buckets live in a `sync.Map` (written once per client, read on every request) with a per-bucket mutex so clients never block each other. 429s carry `Retry-After` (RFC 6585).
- **Order:** auth before rate limit (don't spend limiter work on anonymous requests; the limiter keys on the authenticated name), both scoped to `/api/v1`. Defaults (100 requests per 60 s) apply if the section is missing, so there is no zero-limit lockout.

---

## 7. Phase 4: Redis Queue & Async Processing

**Goal:** reliable async delivery for webhooks and slow targets. 6 new tests (27 total).

- **Fail fast on Redis at startup** (`PING`); `/ready` returns 503 if Redis goes away later, so the load balancer stops routing.
- **Redis lists (LPUSH/BRPOP) over streams.** One worker pool, one queue, retries handled in code: consumer groups and acks would be overhead. Upgrade path to streams if multi-consumer is ever needed. BRPOP blocks instead of polling.
- **Enqueue in a pipeline** (job data `SET` + `LPUSH`) so data and queue entry are written together in one round trip.
- **Job lifecycle:** queued, processing, completed or failed; failed jobs requeue after `retry_delay` (default 30 s, so the target can recover) until `max_retries`, then move to a dead-letter list. Job data originally kept a 24 h TTL so Redis can't grow unbounded (shortened for completed jobs in §9.7).
- **Workers call `relay.Handler.ExecuteRelay()` directly** rather than POSTing to the relayer's own API, which would have pushed them through auth and rate limiting and added a loopback hop.
- **Graceful shutdown** on SIGINT/SIGTERM: stop accepting connections, cancel workers (they finish the current job), drain in-flight requests for up to 10 s. Without it an ECS deploy would reset connections and strand jobs in `processing`.
- **`RouterDeps` struct** instead of a growing positional parameter list.
- **Queue tests use Redis DB 15** and skip when Redis is absent, so they never touch dev data.

---

## 8. Phase 5: Docker & Docker Compose

**Goal:** the whole stack (relayer + Redis) up with one command, and the basis for the ECS image.

**Status:** completed. The Prometheus/Grafana work originally planned here as "Phase 5b" landed in Phase 9 (§13).

| Decision | Choice | Why |
|---|---|---|
| Runtime base | `alpine:3.21` | Keeps a shell for local debugging. `distroless/static` is smaller and was the planned production swap (still open as hardening.md item 6). *Since bumped; see the Dockerfile.* |
| Builder | `golang:1.26-alpine` | Matched `go.mod`; much smaller than the Debian image. *Later pinned to an exact patch tag: a floating tag can be stale on a host with a cached image (security-roadmap, 2026-09-25).* |
| Stages | builder + runtime | Ship only the binary and CA certs: no toolchain, no source. |
| User | non-root UID 65532 | Conventional distroless/Kubernetes UID; a compromised process can't write the filesystem or install packages. |
| Build | `CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"` | Fully static binary that runs on any base; no absolute paths or symbols. |
| Cache | BuildKit cache mounts for modules and build cache; `go.mod`/`go.sum` copied before source | ~90 s first build, ~5 s code-only rebuilds. |
| Healthcheck | `curl /health` | Mirrors the planned ALB probe. `curl` exists in the image only for this; a Go `-healthcheck` flag was judged not worth it yet. |
| Compose order | relayer waits for Redis `service_healthy` | Otherwise the startup `PING` kills the relayer. |
| Redis address | `RELAYER_QUEUE_REDIS_ADDR=redis:6379` | The koanf env mapping overrides the YAML's `localhost` default; the same mechanism later injects the ElastiCache endpoint. |
| Config | mounted read-only | Edit and restart without a rebuild. |

`.dockerignore` mattered more than expected: without it the build context included a committed 10 MB `relayer` binary, a 55 MB `AWSCLIV2.pkg`, `.git`, and a folder of private notes that could have ended up in image layers.

---

## 9. Phase 5.5: Security & Privacy Hardening

**Date:** 2026-04-18. **Goal:** close the highest-impact application, supply-chain and privacy risks before CI turns scanners into gates. The threat model and long-form roadmap live in [security-roadmap.md](security-roadmap.md); this section records what was fixed and why.

**Outcome:** 14 threats addressed (11 fixed, 2 false positives suppressed with justification, 1 deferred), new unit tests, scanners green, smoke tests of SSRF, method abuse and oversized bodies behaving correctly.

### 9.0 Why Phase 5.5 Exists

Security was originally a Phase 10 polish pass. Midway through Phase 5 that looked wrong: a relay that naively forwards client requests is an SSRF engine unless designed otherwise, and fixing it after CI, AWS and observability were built would have meant rewriting the relay handler late. So the security pass slotted between Docker (5) and CI (6): make it reproducible, fix what's broken, then make the scanners blocking gates with no red baseline on day one.

### 9.1 What "Private" Means

"Private" could mean **infra-private** (self-hosted; the operator is trusted and can see payloads) or **user-private** (anonymity or payload confidentiality even from the operator, like Tor or iCloud Private Relay). The code at the time was 100% infra-private: it forwarded whatever the client sent (including `Authorization`), logged identifying metadata, and kept full payloads in Redis for 24 h.

**Decision: stay infra-private, with privacy as a first-class secondary goal.** User-private would be a research project (key management, a client SDK, correlation-attack resistance), and done poorly it gives false confidence. But infra-private hygiene still buys real privacy: strip client identity from outbound traffic, scrub auth headers before persisting jobs, log route patterns instead of concrete paths, and limit how long bodies linger. Those landed below. The roadmap's §5 sketches a user-private track; nothing in the code blocks it.

> **Update (2026-09-27).** Resolved by renaming the project to Credential Relayer: "private" meant privately hosted, never anonymity.

### 9.2 The Threat Model

STRIDE plus a privacy category, full version in [security-roadmap.md §2](security-roadmap.md#2-threat-model). Clients are hostile; targets are semi-trusted (configured by the operator, but able to redirect to `169.254.169.254`, return huge bodies, or poison headers).

T1 SSRF / URL confusion (HIGH), T2 header injection (HIGH), T3 method abuse (MED), T4 resource exhaustion (MED), T5 weak secret posture (HIGH), T6 trusted proxy headers (MED), T7 cleartext / downgrade (MED), T8 `math/rand` jitter (false positive), T9 payload persistence (HIGH, privacy), T10 metadata in logs (MED, privacy), T11 identity leak to target (MED, privacy), T12 committed artefacts (MED), T13 error message leak (LOW), T14 dependency/image drift (LOW).

Phase 5.5 closed T1-T11 except T6 (partial, §9.10). T12 history purge was deferred to Phase 10; T13 and T14 to Phases 6 and 10.

### 9.3 Scanner Baseline and the Main Lesson

| Tool | Purpose | Baseline | After Phase 5.5 |
|---|---|---|---|
| `govulncheck` | Reachable CVEs in Go deps | Clean | Clean |
| `gosec` | Go security antipatterns | 2 HIGH, both false positives | 0 (suppressed with justification) |
| `gitleaks` | Secrets in tree and history | 2: a dev API key in the build log's curl examples (same value in `configs/config.yaml`) | Working tree clean; 2 remain in history (deferred) |
| `trivy image` | OS and Go binary CVEs in the image | Clean | Clean |

**Scanners find regressions, not design flaws.** gosec did not flag the SSRF-shaped string concatenation, the missing header filter, the unbounded body reads, or the 24 h retention of request bodies. Every HIGH finding in this phase came from manual review.

### 9.4 Fix Group A: Repo Hygiene (T5, T12)

`configs/config.yaml` was tracked with a real-looking API key; a 10 MB `relayer` binary of unverifiable provenance and a 55 MB `AWSCLIV2.pkg` were committed. All three were untracked and gitignored; doc examples switched to `$API_KEY`; `config.example.yaml` keeps an obvious placeholder. Rewriting history to remove the old key from commit `6943cd8` was deferred to Phase 10 to avoid force-pushing early.

> **Update (2026-09-26).** The history was never rewritten. When CI switched gitleaks to full-history scanning, it flagged this commit, and the value turned out to be the *live* `dev-client` key, still accepted by the relayer. It was rotated to a hashed key (the old value now gets 401), redacted from the docs, and its two historic findings are ignored by fingerprint in `.gitleaksignore` so any new leak still fails CI. Lesson: "the working tree is clean" is not the same as "the secret is dead"; rotate first, clean up history second. See §14.

Treating a low-value dev key as a real secret was deliberate: the habit of never committing a credential-shaped value is what protects the real one.

### 9.5 Fix Group B: SSRF Hardening (T1, T1b, T7)

**T1, URL concatenation.** `url := target.URL + req.Path` meant `path: "@evil.com/x"` produced `https://httpbin.org@evil.com/x`, which Go parses as userinfo `httpbin.org`, host `evil.com`. Now the path is resolved with `url.ResolveReference` under three checks, each catching a different shape: it must start with `/` (blocks `@evil.com/x`); the parsed reference must carry no scheme, host or userinfo (blocks `//evil.com/x` and absolute URLs); and the resolved host and scheme must equal the base (a backstop for parser edge cases).

**T1b, private-network dials.** A custom `DialContext` resolves the host and refuses RFC1918, loopback, link-local (including `169.254.169.254`), IPv6 ULA, CGNAT and unspecified addresses, then dials the checked IP. Because it runs at dial time it also covers redirect targets. Per-target opt-out `allow_private_ips`, passed to the transport through the request context.

**T7, cleartext targets.** The registry rejects `http://` targets unless `allow_http: true`, rejects schemes other than http/https, and rejects userinfo in base URLs.

**Redirects** are capped at `max_redirects` (default 3) and restricted to http(s); each hop re-enters the dial check.

### 9.6 Fix Group C: Request Validation (T2, T3, T4)

**T2, header injection.** Every client header used to be copied upstream, so a client could set `Authorization`, `Host`, `X-Forwarded-For` or hop-by-hop headers: the relay was a credential-laundering tool by accident. Now a deny-list drops hop-by-hop headers (RFC 7230), auth and routing headers (`Authorization`, `Cookie`, `Host`, `Proxy-*`), and proxy hints (`X-Forwarded-*`, `X-Real-IP`); a second list of identity headers (`User-Agent`, `Referer`, `Forwarded`, `Via`) is dropped unless the target opts in. A hard allow-list was rejected because passing application headers through is the relay's purpose; the deny-list is built from actual attack shapes. (A bypass of it was found by fuzzing in §14.)

**T3, method abuse.** Only GET, POST, PUT, PATCH and DELETE; anything else (notably `CONNECT`) gets 400 before any retry.

**T4, unbounded bodies.** Inbound bodies go through `http.MaxBytesReader` (default 1 MiB, mapped to 413 rather than 400 because retry logic treats them differently); target responses through `io.LimitReader` (default 10 MiB) so a hostile target can't OOM the relay.

### 9.7 Fix Group D: Privacy Controls (T9, T10, T11)

- **T11, outbound identity.** The relay sends its own neutral `User-Agent` (then `private-relayer/0.1`, now `credential-relayer/0.1`) and strips `Referer`, `Forwarded` and `Via`, so targets can't fingerprint individual callers. Opt-out per target with `preserve_user_agent`.
- **T9, queue privacy.** Webhook headers pass through `StripSensitiveHeaders` before enqueue, so `Authorization` and friends never reach Redis. Completed jobs have body and headers cleared and a 1 h TTL; failed and dead-lettered jobs keep everything for 24 h for debugging. Verified against live Redis: no `headers` or `body` field, TTL ~3584 s.
- **T10, paths in logs.** Concrete paths are often PII-shaped (IDs, tokens). The request logger records chi's route pattern (`/api/v1/jobs/{id}`), falling back to the raw path only for unrouted 404s.

### 9.8 Fix Group E: Operational Controls (T4c, T8, G101)

- **T4c, rate-limiter memory leak.** Buckets were never removed, and an attacker rotating IPs could grow the map. A sweep goroutine (every 2x window) evicts buckets idle for 10x window; it shares the shutdown context.
- **T8, `math/rand` for jitter** (gosec G404): suppressed with a justification. Jitter de-synchronizes retriers; it is not a security boundary, so a CSPRNG buys nothing.
- **G101, `const headerAPIKey = "X-API-Key"`**: a header name, not a credential; suppressed with justification. Suppressions are greppable (`#nosec`) so reviewers can audit them.
- **`ReadHeaderTimeout`** (5 s default, `server.read_header_timeout`) against Slowloris-style header dribbling.

### 9.9 Fix Group F: Container Hardening

Both Compose services run with `read_only: true`, tmpfs for `/tmp` and `/run`, `cap_drop: [ALL]`, `no-new-privileges`, `mem_limit` and `pids_limit`. Bug found: the Redis entrypoint starts as root and `setuid`s to `redis`, which fails once `CAP_SETUID` is dropped (`failed switching to "redis": operation not permitted`). Fix: run the service as `user: redis` so it never needs to drop privileges.

### 9.10 What Was Not Fixed (and Why)

| Item | Why deferred |
|---|---|
| **T6 proxy-header trust** (partial) | `RealIP` trusts `X-Forwarded-For`: right behind an ALB, a rate-limit bypass when exposed directly. Deferred until the ALB CIDR was known. *Update 2026-09-25: RealIP removed entirely (chi deprecated it).* |
| **T12b history purge** | Avoid force-pushing private history; planned for the public fork. *Update 2026-09-26: the key was rotated instead, see §9.4.* |
| **T13 error-message leakage** | Low risk (stdlib errors, no filesystem paths exposed); generic errors plus a request ID planned for Phase 10. Still open. |
| **T14 continuous drift** | Phase 6's purpose (nightly scans, Dependabot). |
| **Secrets Manager, ElastiCache TLS + AUTH, WAF, ALB and VPC flow logs** | Phase 7, once AWS exists. |
| **Pen-test / k6 abuse suite** | Phase 10. |

Every deferral was tied to a named later phase, so those phases close specific items rather than "do security".

### 9.11 Before / After

| Check | Before | After |
|---|---|---|
| gosec | 2 HIGH (false positives) | 0 |
| gitleaks, working tree / history | 2 / 2 | 0 / 2 (deferred) |
| govulncheck, trivy HIGH/CRITICAL | 0 | 0 |
| SSRF path `//evil.example.com/x` | not tested | refused ("path must not contain scheme, host, or userinfo") |
| `CONNECT` | forwarded | 400, 0 attempts |
| 2 MB body | buffered | 413 |
| `Authorization` in a webhook | persisted in Redis | stripped before enqueue |
| Completed job | body kept 24 h | body cleared, ~1 h TTL |
| Outbound `User-Agent` | client's | relayer's own |
| Containers | default | read-only, all capabilities dropped, no-new-privileges |
| Rate-limit buckets | unbounded | idle-evicted |

### 9.12 Tests

New tests pin each fix: allowed headers forwarded while `Authorization` is stripped; the `User-Agent` rewrite; `CONNECT` refused; a `//attacker.com/x` path never yields a 200 (it surfaces as 502 because the URL error happens inside the retry loop; the invariant is "the attacker never gets the 200"); `http://` targets rejected without `allow_http`; and `StripSensitiveHeaders`. 31 tests total.

### 9.13 Why This Order Matters

Two options: turn on CI scanners first and fix (or suppress) findings reactively, or fix first and then gate. The first reaches green faster but makes the scanners rubber stamps full of exclusions. Fixing first means a clean baseline, so every future CI finding is a real regression.

---

## 10. Phase 6: CI with GitHub Actions

**Date:** 2026-04-18/19. **Goal:** turn the Phase 5.5 baseline into an enforceable gate: every PR and push runs the scanners, tests and a Docker build, and a nightly run re-scans so upstream CVEs surface without a push.

### 10.0 Why Two Workflows

`ci.yml` (lint, test, build) and `security.yml` (scanners, plus a nightly cron) are split for **failure clarity** ("security failed: gitleaks" is actionable, "CI failed" isn't) and **scheduling** (only the security scans need to run nightly).

### 10.1 `ci.yml`

- **lint:** golangci-lint with **gosec added**. The two known false positives are suppressed inline with justifications, so any new occurrence fails.
- **test:** `go test -race -count=1 -timeout=3m` against a Redis service container. `-race` because the worker pool, sweeper and signal handler are all goroutines; `-count=1` so queue tests always hit Redis; the timeout caps runaway tests.
- **build:** Buildx with the GitHub Actions cache, loaded locally for a smoke test, no push (ECR push belongs to Phase 8).
- Go version from `go-version-file: go.mod`, so CI can't drift from the source.

### 10.2 `security.yml`

- **govulncheck:** reachability-aware, so few false positives; zero tolerance.
- **gitleaks:** initially `--no-git` (working tree only), so CI would gate new secrets without forcing a decision about rewriting history for the known old key.
  > **Update (2026-09-26).** `security.yml` now scans the full history (`fetch-depth: 0`, no `--no-git`), with revoked secrets ignored by fingerprint in `.gitleaksignore`. The first history scan is what found the live key described in §9.4.
- **trivy image:** HIGH/CRITICAL, `ignore-unfixed` (an unpatchable CVE isn't actionable).
- **trivy config:** Dockerfile and Compose misconfigurations, so a later change can't quietly undo `read_only` or `cap_drop`.
- **Nightly at 06:17 UTC:** the answer to T14 drift.

### 10.3 CodeQL: Prototyped, Removed, Deferred

A `codeql.yml` ran fine but its SARIF upload failed: code scanning on a private repo requires GitHub Advanced Security, a paid add-on not available on this plan. It was removed rather than leave a permanently red check that devalues real failures. Kept instead: gosec (most of what CodeQL would flag for Go), govulncheck (reachability, which CodeQL doesn't do), trivy, and manual threat modelling. Cost: less taint tracking. Plan: re-add on the public repository, where CodeQL is free.

First-push fixups (2026-04-19): action versions bumped, the golangci-lint binary pinned (v2.1.6 was built with Go 1.24 and couldn't parse `go 1.26.2` in `go.mod`), the Dockerfile stopped copying the gitignored live config, and alpine was bumped.

### 10.4 Dependabot

Weekly for `gomod`, `docker` and `github-actions`; Go minor and patch updates grouped into one PR, majors individually; security advisories open immediately. Dependabot proposes, CI validates: a bump that breaks a test sits red until a human looks.

### 10.5 Permissions and Concurrency

Every workflow declares `permissions: contents: read` (least privilege per workflow) and a concurrency group that cancels superseded runs.

### 10.6 Branch Protection on the Free Tier

On a private repo on GitHub Free, neither classic branch protection nor rulesets are available (403, "Upgrade to GitHub Pro or make this repository public"). Options were Pro ($4/month), going public, or discipline plus free-tier settings. Chosen: discipline. The checks still run and show on PRs, but merges are not machine-blocked. Settings that approximate it: always merge through PRs, squash-only merging, auto-delete head branches, read-only default workflow token. The checks that would be required: `lint (golangci-lint)`, `test (with redis)`, `build (docker image)`, `govulncheck (reachable Go CVEs)`, `gitleaks` (now named `gitleaks (secrets in history)`), `trivy (image CVEs)`, `trivy (Dockerfile + compose misconfigs)`.

### 10.7 Explicitly Out of Scope

No deploy (Phase 8), no cloud credentials in CI yet (Phase 8 adds OIDC), no release automation and no SBOM (Phase 10). Named so later phases close specific items.

### 10.8 What Closed

T14 (dependency and container drift): govulncheck and trivy on every PR and nightly, Dependabot weekly. The Phase 5.5 fixes also became regression gates: reintroducing the dev key or removing a `#nosec` justification now fails CI.

---

## 11. Phase 7: AWS Infrastructure with Terraform

**Date:** 2026-04-26. **Goal:** all cloud infrastructure as code. Shipped as modules (`vpc`, `alb`, `ecs`, `redis`) composed by `dev` and `prod` environments; at the time, not yet applied to a real account.

- **Fargate over EC2:** no servers to manage; ARM64 by default for cost.
- **Two AZs minimum** (the ALB requires it); subnets computed with `cidrsubnet` from an AZ list.
- **ECS and Redis in private subnets**, egress via NAT (one shared in dev, one per AZ in prod), inbound only through the ALB security group. **Security-group-to-security-group rules** everywhere, no intra-VPC CIDR rules.
- **Remote state** declared as `backend "s3" {}` and configured at `init` time, so the code works against any account.
- **AWS provider pinned (`~> 6.42`) with the lock file committed** for reproducible plans.
- **`ignore_changes` on the ECS service's `task_definition` and `desired_count`:** the deploy pipeline owns those, Terraform owns the shape.
- **Secrets through the task definition's `secrets`** (SSM / Secrets Manager); plain config through `environment`.
- **Phase 5.5 defenses carried over:** read-only root filesystem, `drop_invalid_header_fields` on the ALB, Redis encrypted at rest.
- **Container Insights opt-in**, off by default: not in the free tier. CloudWatch Logs are always on and fit the free tier.
- **Redis TLS end to end in prod** (`transit_encryption_enabled` plus `RELAYER_QUEUE_REDIS_TLS=true`, TLS 1.2+ in the client); dev keeps it off.

Out of scope: building images (Phase 8), DNS and ACM (prod assumes an existing certificate), and applying to an account.

---

## 12. Phase 8: Continuous Deployment

**Date:** 2026-04-26. **Goal:** automated deploys from GitHub to ECS. Dev pipeline shipped; prod intentionally deferred (§12.6).

> **Update.** The AWS deploy workflow is now `deploy-aws.yml` and manual-only; the default `deploy.yml` deploys to the Pi (§14, §15).

### 12.1 What It Added

`ecr` (private repository; keep the last 10 tagged images, expire untagged after 7 days) and `github_oidc` (OIDC provider plus an IAM role scoped to ECR push and ECS deploy) modules, and a workflow that builds, pushes `:sha-<commit>`, renders the task definition with the new image, registers it and waits for service stability.

### 12.2 Why Terraform and the Pipeline Don't Fight

`ignore_changes` on the service (§11) lets the pipeline register revisions without Terraform drift. The deploy role can register task definitions and update only the dev service, push only to the dev repository, and `PassRole` only to the ECS module's task and execution roles.

### 12.3 OIDC

No long-lived AWS keys. The role's trust policy requires `aud == sts.amazonaws.com` and a `sub` matching this repository's `main` branch, so a fork, another branch or a PR cannot assume it. The hard-coded thumbprint is the historical GitHub value; AWS now validates the chain itself but the API still requires the field.

### 12.4 Lifecycle Is Not Access Control

The ECR lifecycle policy controls cost (how long old images live); IAM controls who can push and pull. Conflating them is common; privacy here is purely IAM.

### 12.5 Bootstrap

The first deploy needs a placeholder image pushed by hand so the service can start once, then a second `apply` pointing at it. The role ARN is stored as a repository *variable*, not a secret: it isn't sensitive on its own, because the OIDC trust policy is what keeps others out. (Commands: `infra/terraform/README.md`.)

### 12.6 Deliberately Not Done

- **No prod pipeline.** The prod composition exists, but no workflow, ECR repo or OIDC role; when added it gets its own GitHub Environment with a required reviewer.
- **No tag-based releases**; every push to `main` deployed to dev.
- **No rollback beyond ECS's rolling deploy:** ECS won't replace healthy tasks with unhealthy ones, and the workflow waits for stability, so a bad deploy fails and the previous revision stays live. Metric-driven rollback was left for later.

---

## 13. Phase 9: Observability & Alerting

**Goal:** know when things break before users say so, and answer "who used what".

### What was built

- **Metrics on their own port** (`:9090`), never the API port, so scraping needs no API key and API clients can't read them. Collectors on a private `prometheus.Registry` (no globals), which also lets every test build a fresh set.
- **Prometheus, Alertmanager and Grafana in Compose**, provisioned from files. Alertmanager's SMTP app password comes from the host's `.env` and is templated in at container start, so it never lands in a committed file.
- **Alert rules** for relayer down, 5xx rate, p95 latency, failing upstreams, queue backlog, dead-lettered jobs, and spikes of auth failures and rate-limit hits (the "someone is probing" signal).
- Tracing (OpenTelemetry) was left as a future enhancement.

### Growing it into four dashboards (2026-09-26)

Once the relayer brokered real credentials, "requests per second" stopped being the interesting question. New metrics:

| Metric | Answers |
|---|---|
| `relayer_rejections_total{target,key,reason}` | What did the gate stop, for whom, and why? |
| `relayer_upstream_responses_total{target,key,code}` | Which key used which upstream, and how did it go? |
| `relayer_stream_ttfb_seconds{target}` | How long until the first token? (the LLM latency people feel) |
| `relayer_stream_duration_seconds`, `relayer_stream_bytes_total` | How long and how big are streams? |

Dashboards: **Security & usage**, **LLM & upstreams**, **Pi host** (node_exporter) and an SLO row on the main one. Chart choices followed a data-visualization method rather than taste: time to first byte is a **heatmap** of the histogram (a p95 line hides a second population, a heatmap shows it); refusals per reason are single-hue sorted bars (the job is comparing magnitudes); headline numbers are stat tiles, not one-bar charts; and the palette was run through a colorblind-safety validator. The four-way status palette *failed* it (4xx yellow next to 429 orange was too close), so 429s moved to their own panel.

### SLO and burn-rate alerting

- **SLI:** share of API requests not failed *by the relayer*: 5xx except 502, because a 502 means the upstream failed, and upstream health is tracked separately. Health probes excluded.
- **Target:** 99.5% over 30 days, so the error budget is 0.5% of requests.
- **Alert:** the multi-window burn-rate rule from the Google SRE workbook: fire when both the 1-hour and 5-minute error ratios exceed 14.4x the budget rate (2% of a month's budget in an hour); the short window makes it clear quickly after a fix.
- Prometheus retention went from 7 to 35 days, since a 30-day SLO needs a full window, with a 1 GB size cap for the SD card.

### Bugs this phase found

- **Label cardinality from clients.** Rejections are labelled by target, but a client can send any target name. Recording it verbatim would let anyone create unbounded series. Names the registry doesn't know are recorded as `unknown`.
- **False latency alerts.** `HighLatencyP95` measured every route; a streamed LLM answer legitimately takes seconds, so the proxy route is excluded (streams have their own histograms).
- **Counters that start above zero read as zero.** Three real 413s showed as `0` on the security dashboard: the series was *created* by those events, and `increase()` can't see a series' first increments. Prometheus 3's `created-timestamp-zero-ingestion` records each counter's start at zero; the same three events then read 3. Rare events are exactly the ones a security dashboard exists for, so this mattered.
- **A wrong metric name** (`node_cpu_scale_frequency_hertz` instead of `..._scaling_...`). Caught by running every panel's query against live Prometheus before committing, which is now the habit for dashboard changes.

---

## 14. Phase 10: Hardening & Documentation

**Dates:** 2026-09-24 to 2026-09-28. **Goal:** close the backlog from the threat model, prove the fixes, and make the repo fit to publish.

### Authentication and authorization

- **Hashed API keys** (`key_sha256`, generated by `make new-key`), optional `expires_at`, and several entries with one name so a key can be rotated without downtime. Expired and unknown keys return the same 401: no oracle for "this key used to work".
- **Per-key target scopes** that fail closed, and **job ownership**: a key can only read jobs it enqueued, and gets 404 (not 403) for anyone else's, so it can't even learn they exist.
- **Short plaintext keys refused** (2026-09-28): a plaintext `key` under 32 characters stops startup; `key_sha256` remains the recommended form.

### Fuzzing found a real bypass

`FuzzBuildTargetURL` held (2.7M executions). `FuzzHeaderFilter` did not: a header named `X_Real_IP` passed the deny-list, but CGI/WSGI-style backends read it as `X-Real-IP`. The filter now folds `_` to `-` before checking. The fuzz target models the backend's folding independently, so the test isn't just the implementation checking itself.

### Performance: a 3.7x fix from a profile

Under 200 concurrent requests the Pi managed 282 req/s. `pprof` showed ~80% of CPU in dialing new TCP connections: the per-request timeout context was cancelled as soon as the handler returned, before the caller had read the response body, so `net/http` couldn't return the connection to the pool. Cancelling on `Body.Close()` instead (a small `cancelOnClose` wrapper) took it to **1048 req/s** and p95 from 1.95 s to 0.41 s. A regression test pins it.

### Deploys gated on CI

The deploy used to run on every push, in parallel with tests, so a failing commit still reached the Pi. It now triggers on a *successful* `ci` run (`workflow_run`), checks out exactly the commit that was tested, and skips if `main` has already moved on, so an older run finishing last can't roll the Pi back. One trap: `workflow_run`'s branch filter also matches a **fork's** branch named `main`, and this job holds tailnet credentials, so it additionally requires `event == 'push'` from this repository.

### The leaked key, for real

See the update in §9.4: the first full-history secret scan found that the "old" dev key in an early commit was still the live key. Rotated, redacted, fingerprint-ignored. During the rotation the relayer went down for two minutes because the rewritten `config.yaml` was left mode `600`, rsync preserved it, and the non-root container couldn't read its own config. Now noted in the deploy docs: config stays `644`.

### Preparing to publish

Machine-specific values (the Pi's tailnet IP and SSH user) moved out of tracked files into a gitignored `local.mk` and, for CI, repository variables. The deploy job runs only where those variables exist, so a fork or public mirror never touches a tailnet.

---

## 15. Phase 11: Raspberry Pi, Tailnet-Only Deployment

**Date:** from 2026-09-24 (host audit in [hardening.md](hardening.md)). **Goal:** keep a live deployment after the AWS credits ran out, without exposing anything to the internet.

**Why a Pi.** It is free to run, always on, and a more honest constraint than a cloud VM: 4 cores, an SD card, and a CPU that throttles at 80 °C. Performance work (§14) and the Pi host dashboard (§13) both came from taking that seriously.

**How it is reached.** Only over **Tailscale**: tailnet-only, with deny-by-default Tailscale ACLs. Every Compose port binds to the Pi's tailnet address (`BIND_IP=$(tailscale ip -4)`), never `0.0.0.0`; the tailnet policy grants access by tag (`tag:pi`, `tag:ci`) and carries policy tests; SSH is Tailscale SSH (identity-based, no keys to manage). Details: [hardening.md](hardening.md), [pi-deploy.md](pi-deploy.md).

**How it is deployed.** `make deploy-pi` rsyncs the repo (gitignored files excluded, host-only files such as `configs/config.yaml` and `.env.relayer` protected from `--delete`) and runs `docker compose up -d --build`. CI does the same from an ephemeral, tagged tailnet node (§14).

**Things the Pi taught:**
- `/tmp` is mounted `noexec`: good hardening, and the reason a diagnostic binary had to run from the home directory.
- The containers had no IPv6 route while DNS returned IPv6 addresses first. It turned out not to matter (Go ordered IPv4 first), but it was worth proving with a probe rather than assuming.
- Host metrics come from node_exporter with `pid: host` and a read-only view of `/`, no published port. Network-interface stats would need host networking, which would put `:9100` on the tailnet, so they were left out on purpose.

---

## 16. Phase 12: Credential Broker: Secrets, Policy, Streaming

**Date:** 2026-09-26. **Goal:** clients should never hold upstream secrets. This phase is where the project stopped being "an HTTP relay" and became what the README now leads with.

### Secret injection

A target maps header names to the **names of environment variables**:

```yaml
secret_headers:
  X-Goog-Api-Key: GEMINI_API_KEY
```

- Values are read once at startup; a missing or empty variable **stops startup**, never a silent unauthenticated request. Error messages name the variable, never the value.
- Injected **after** client headers, and any client header that folds to the same name (`x_goog_api_key`) is dropped, so a client can neither override the secret nor send a second copy.
- **Redirects:** Go's client strips only `Authorization` and `Cookie` on a cross-host redirect; a custom header like `X-Api-Key` would follow a redirect anywhere. The relayer strips its secrets on any host or scheme change. The test was verified by removing the fix and watching the secret arrive at the other host.
- **Echoes:** httpbin's `/headers` returns the request headers, secret included. Injected values are redacted from responses.
- Secrets are injected at send time, so they never reach Redis with queued jobs.

The secret lives in `infra/docker/.env.relayer`, given only to the relayer container (not to Grafana or Alertmanager). Getting a key onto the Pi safely turned out to be the hardest part: one attempt pasted the key into the command line instead of the hidden prompt, which puts it in shell history. The fix was procedural: revoke that key, and use an editor or a length-checked `read -s`.

### Per-target policy and one gate

```yaml
allow_methods: ["POST"]
allow_paths: ["/v1/messages"]
max_body_bytes: 262144
```

Without this, any key allowed to use a target could call *every* endpoint of the upstream with your credential. `Handler.Authorize` is the single gate for `/relay`, `/webhook` (before enqueue, so a bad job gets a 4xx instead of dead-lettering later), `/proxy` and the worker (so a job queued before a policy change can't bypass it). Order: key scope, method, target, path, policy.

Paths are matched after `../` resolution, on segment boundaries (`/v1/messages` doesn't allow `/v1/messagesX`). When a path policy is set, encoded dots/slashes/backslashes, raw backslashes and `;` are refused: an upstream might decode `%2e%2e` or treat `..;/` as traversal after the relayer approved the path. `FuzzPathPolicy` models such an upstream (decode, fold backslashes, clean) and asserts every accepted path still lands inside the prefix: 4.4M inputs, no bypass.

### Streaming proxy for SDKs and agents

`/api/v1/proxy/{target}/...` forwards the raw request and streams the response back as it arrives, flushing after every chunk, so an SDK or agent only needs its base URL changed.

- **Authentication by coincidence, then by design.** The Anthropic SDK sends `ANTHROPIC_API_KEY` as `x-api-key`, which is already the relayer's header, so a relayer key works as-is. Google SDKs send `x-goog-api-key`; auth now accepts that too. Either way, the caller's relayer key is **never forwarded** upstream (tested by removing the drop rule).
- **Redaction across chunks.** A secret can be split between two stream chunks. `streamRedactor` holds back only the longest tail that could still become a secret, so ordinary SSE events flush immediately; a test checks every three-way split of a stream. `Accept-Encoding` is stripped so the transport decompresses and redaction sees plaintext. A truncated stream drops the held tail rather than leak half a secret. (On `/relay` this can't happen: a body cut mid-string is invalid JSON and never encoded, which corrected an earlier over-cautious note.)
- **Timeouts.** The server's 10 s write timeout would cut every long answer, so the proxy lifts it for that response only (target timeout plus a grace period, so a 502 can still be written). `/relay` later got the same treatment, sized to its worst case: every attempt timing out plus backoff.
- **No retries.** A stream can't be replayed and LLM POSTs aren't idempotent.

### Trying it for real: Gemini free tier and Gemini CLI

Chosen because it costs nothing. What happened, in order:

1. Every request returned an **empty 404**. The relayer, the key and DNS were ruled out one by one (a probe binary in the relayer's own image and network; the same relayer code working locally). Two causes overlapped: the model name picked from memory had been retired for new users, and the brand-new key was still propagating.
2. **Gemini CLI** requires HTTPS unless the base URL is `localhost`, so it runs through an SSH port-forward over Tailscale. It worked first time once the relayer accepted `x-goog-api-key`.
3. Asked to do an agentic task, the CLI started getting **413s**: agents resend the whole conversation (file contents, tool output) every turn, and the 64 KB body limit was sized for chat. The policy worked exactly as intended; the limit was wrong. And the security dashboard showed those refusals as 0, which led to the Prometheus fix in §13.

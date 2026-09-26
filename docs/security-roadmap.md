# Private Relayer -- Security & Privacy Roadmap

> Living document. Owns the threat model, the tooling we use to catch issues, the backlog of findings, and the phased plan to fix and prevent them. Updated as we scan, audit, and ship.

Last baseline: 2026-04-18. 2026-09-25: govulncheck CI failed on Go 1.26.2 stdlib + chi 5.2.5 CVEs; fixed by go 1.26.6 + chi 5.3.0 + pinned `golang:1.26.6-alpine` in the Dockerfile (the floating `golang:1.26-alpine` tag can be stale on a host with a cached image). Last rescan: 2026-04-18 (post Phase 5.5).

**Status at a glance (post Phase 5.5):**

| Threat | Sev | Status |
|---|---|---|
| T1 SSRF via URL concat | HIGH | ✅ fixed |
| T1b Private-IP dial filter | HIGH | ✅ fixed |
| T2 Header injection | HIGH | ✅ fixed |
| T2b Header-name folding (`X_Real_IP`) | MED | ✅ fixed 2026-09-25 (found by fuzzing) |
| T3 Method allowlist | MED | ✅ fixed |
| T4a Inbound body cap | MED | ✅ fixed |
| T4b Response body cap | MED | ✅ fixed |
| T4c Rate-limit bucket leak | MED | ✅ fixed |
| T5 Secret hygiene (working tree) | HIGH | ✅ fixed |
| T6 Proxy-header trust | MED | ✅ fixed 2026-09-25: `middleware.RealIP` removed (chi deprecated it as spoofable). Behind an ALB use `ClientIPFromXFFTrustedProxies` |
| T7 HTTPS enforcement | MED | ✅ fixed |
| T8 gosec G404 (jitter) | FP | ✅ nosec |
| T9 Payload persistence | HIGH | ✅ fixed |
| T10 Metadata in logs | MED | ✅ fixed |
| T11 Client identity leak | MED | ✅ fixed |
| T12a Committed binaries removed | MED | ✅ fixed |
| T12b Git history purge | MED | ⏳ deferred -- Phase 10 (public showcase) |
| T13 Error message leakage | LOW | ⏳ deferred -- Phase 10 |
| T14 Dependency/image drift | LOW→MED | ✅ fixed (Phase 6: govulncheck + trivy on PR + nightly, Dependabot weekly) |
| gosec G101 (header name) | FP | ✅ nosec |
| T15 Upstream credential exposure (secrets on clients; leaks via override, redirect, echo) | HIGH | ✅ fixed 2026-09-26: `secret_headers` injection; see `docs/hardening.md` §4 item 3b |

---

## 0. Framing: What Does "Private" Mean Here?

The repo name is `private_relayer` but "private" is ambiguous and shapes everything downstream. Two distinct interpretations:

| Interpretation | Goal | Who is the adversary | What we protect |
|---|---|---|---|
| **Infra-private** (self-hosted) | The operator runs their own relay instead of a shared SaaS. Security = confidentiality/integrity of the operator's deployment and the data flowing through it. | Internet attackers, malicious API clients, compromised target services, rogue insiders. | Credentials, the relay's uptime, data in transit and at rest, logs. |
| **User-private** (privacy-preserving) | The *clients* of the relay get anonymity or confidentiality benefits -- the relay actively strips metadata, hides the true origin, or encrypts payloads end-to-end. | The relay operator itself, network observers, target services trying to fingerprint callers. | Client identity, request contents, timing/traffic-analysis resistance. |

**Current code is 100% infra-private.** The relay faithfully forwards whatever the client sends, logs identifying metadata (request IDs, IPs via chi's `RealIP`), and stores full request payloads in Redis for 24h. Nothing about it is privacy-preserving in the cryptographic/anonymity sense.

**Decision needed.** We should pick one explicitly. Both are legitimate; they drive different features.

- If we pick **infra-private only**: the roadmap below applies as-written.
- If we add **user-private goals**: we layer on extra controls -- client header stripping (remove `User-Agent`, `X-Forwarded-For`, cookies by default), payload encryption at rest, request-coalescing or batching to resist traffic analysis, no logging of paths/query strings, short TTLs on job state, option for clients to submit pre-encrypted bodies the relay can't decrypt.

For now this doc treats privacy as a **first-class secondary goal** -- we'll call out privacy impact on every decision, and keep the door open for user-private features without committing to them yet.

---

## 1. Assets & Trust Boundaries

```
┌─────────────┐    (1) HTTPS     ┌──────────────────┐   (2) HTTPS     ┌──────────────┐
│   Client    │ ───────────────> │  Private Relayer │ ──────────────> │  Target API  │
│ (trusted?)  │   X-API-Key      │  (this service)  │   via registry  │  (semi-trust)│
└─────────────┘                  └────────┬─────────┘                 └──────────────┘
                                          │
                                 (3) Redis │ (internal)
                                          ▼
                                  ┌──────────────┐
                                  │ Redis queue  │
                                  │ + job state  │
                                  └──────────────┘
```

**Assets we protect (ranked):**

1. **API keys** -- used to authenticate clients. Compromise = full abuse of the relay.
2. **Client payloads in flight** -- headers and bodies sent through `/relay` and `/webhook`. May contain PII, tokens, financial data.
3. **Client payloads at rest** -- Redis stores full job payloads for 24h. Same sensitivity as in-flight.
4. **Target URLs and registry config** -- discloses what internal/external systems the operator integrates with.
5. **Operator infrastructure** -- the relay itself, its container, the Redis node, logs.
6. **Audit trail** -- who sent what, when. Both a security asset (incident response) and a privacy liability (surveillance data).

**Trust boundaries:**

- **(1) Client → Relay.** Assume hostile. Everything the client sends must be validated.
- **(2) Relay → Target.** Semi-trusted (you configured the targets) but still: a compromised target can return malicious redirects, huge responses, poisoned headers.
- **(3) Relay → Redis.** Internal network. Today: compose bridge (trusted). Production (ECS): ElastiCache with auth token and in-transit TLS.
- **Operator insider.** Has access to logs + Redis state = can see payloads. Not in scope today; in scope if we promise user-private.

---

## 2. Threat Model

Using STRIDE (Spoofing, Tampering, Repudiation, Information disclosure, Denial of service, Elevation of privilege) + a seventh category for **Privacy / linkability** since we're treating privacy as first-class.

### T1. SSRF / URL confusion -- **HIGH**
*Category: Tampering + Elevation of privilege.*
The relay resolves target URL by name (good) but constructs the final URL by raw string concat: `url := target.URL + req.Path` at `internal/relay/relay.go:186`. A client-supplied path that begins with `@`, `//`, or `:` can shift the host under Go's URL parser (e.g., `https://httpbin.org` + `@evil.com/x` → userinfo `httpbin.org` + host `evil.com`). Also: `http.Client{}` has default redirect policy (up to 10), so a cooperating target can redirect into private RFC1918 space.
**Impact:** Relay reaches attacker-controlled endpoint or internal AWS metadata (`169.254.169.254`) once in VPC.
**Mitigations:** parse `target.URL` once, use `url.ResolveReference` with a sanitized path; reject paths that don't start with `/`; strip `..` segments; custom `CheckRedirect` on `http.Client` that rejects private CIDRs; optional allowlist of hosts per target.

### T2. Header injection / request smuggling / auth laundering -- **HIGH**
*Category: Tampering.*
At `relay.go:199-201`, client headers are copied into the outbound request with no allowlist. Clients can set `Authorization` (impersonate the relay to the target), `Host` (bypass vhost routing), `X-Forwarded-For` (poison target logs), `Transfer-Encoding` (Go net/http sanitizes outbound, but paranoia is cheap), or hop-by-hop headers (`Connection`, `Upgrade`). On the response side, the relay currently only returns `response_code` and `response_body`, so response-header leakage is limited -- but that's accidental, not by design.
**Mitigations:** allowlist of forwardable request headers (`Content-Type`, `Accept`, application-specific `X-*`); strip hop-by-hop per RFC 7230; never let clients override `Authorization`/`Host`/`Content-Length`/`Transfer-Encoding`; document what's stripped.

### T3. Method abuse -- **MEDIUM**
*Category: Elevation / Tampering.*
No method allowlist. `CONNECT`, `TRACE`, `OPTIONS` accepted. `CONNECT` to a non-HTTPS target could be used as a proxy.
**Mitigations:** allowlist `GET POST PUT PATCH DELETE`; reject others with 400.

### T4. Resource exhaustion / DoS -- **MEDIUM**
*Category: DoS.*
- No inbound body size limit: `json.NewDecoder(r.Body).Decode(&req)` reads arbitrary bytes (`relay.go:51`, `webhook.go:21`).
- Unbounded response body read from target: `io.ReadAll(resp.Body)` (`relay.go:103`) -- a malicious target can OOM the relay.
- Rate limiter's `sync.Map` never evicts idle buckets -- memory grows with unique keys/IPs forever.
- Worker pool fixed at 5; slow-drain attack via many queued jobs is possible but bounded by Redis.
**Mitigations:** `http.MaxBytesReader` on incoming bodies (e.g., 1 MiB); `io.LimitReader` on target responses (e.g., 10 MiB, configurable per target); TTL / LRU eviction on rate-limit buckets; `http.Server.ReadHeaderTimeout` (not set today).

### T5. Weak secret posture -- **HIGH**
*Category: Information disclosure.*
- `configs/config.yaml` ships with a real-looking API key `<revoked-dev-key>` and is tracked in git.
- Same value appears in `how_to.md` in curl examples (flagged by gitleaks, see §6).
- `configs/config.example.yaml` also has a placeholder, but the example and the live file coexist, so committing changes to config.yaml is easy to forget.
- No support yet for loading keys from Secrets Manager / env-file / SOPS.
- API keys are stored in-process in a plaintext map.
**Mitigations:** remove `configs/config.yaml` from git (keep only `.example`); load API keys from env var (list) or from AWS Secrets Manager in Phase 7; redact keys from all log output; hash keys with a KDF if we need to persist them.

### T6. Trust of proxy headers -- **MEDIUM**
*Category: Spoofing.*
chi's `middleware.RealIP` trusts `X-Forwarded-For`/`X-Real-IP` from the direct peer. Behind the ALB this is fine. If the relay is ever exposed directly to the internet (or for local `docker compose` play), a client can forge `X-Forwarded-For` to bypass the rate limiter (which uses `r.RemoteAddr` as the fallback key) and poison logs.
**Mitigations:** only enable RealIP when a trusted-proxy CIDR is known; in compose/local, drop it; config flag `server.trust_proxy: false` by default.

### T7. Cleartext and weak transport -- **MEDIUM**
*Category: Information disclosure + Tampering.*
- Relay listens on plain HTTP :8080. Fine behind an ALB with TLS termination, but the docker-compose setup publishes :8080 on the host directly and never encrypts.
- Target URLs can be `http://` -- nothing enforces HTTPS. Silent downgrade.
- Redis connection is cleartext (ACL `password: ""`). Acceptable on the docker bridge; not acceptable on ElastiCache without `tls: true` and an auth token.
**Mitigations:** enforce `https://` scheme on target URL at config-load time (fatal if http://), with an explicit opt-in flag `allow_http_targets: true` for dev; ElastiCache in-transit encryption + AUTH token from Secrets Manager in Phase 7; optional local TLS via a reverse-proxy sidecar.

### T8. Weak crypto source for non-security use -- **LOW (false positive)**
*Category: gosec G404 noise.*
`internal/relay/retry.go:22` uses `math/rand` for jitter. Jitter does **not** need cryptographic randomness; this is a benign use. Mark with `// #nosec G404 -- jitter does not require CSPRNG`.
**Mitigation:** nosec comment, document reasoning, don't switch to `crypto/rand` (unnecessary overhead).

### T9. Privacy: payload persistence -- **HIGH (privacy)**
*Category: Privacy/linkability + Information disclosure.*
Every `/webhook` call persists the full client request (target, method, path, headers, body) as JSON in Redis with a 24h TTL (`internal/queue/job.go`, `internal/queue/queue.go`). Anyone with Redis access (operator, insider, compromised attacker, backup snapshots) sees plaintext payloads including any `Authorization` headers the client passed in.
**Mitigations:** (a) at minimum, strip known-sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`) before persisting; (b) shorten TTL for completed jobs (e.g., 1h); (c) optional per-target flag `persist_body: false` that stores only metadata; (d) Phase 7+: encrypt job body at rest using KMS envelope encryption; (e) user-private mode: require clients to submit an encrypted blob the relay cannot decrypt.

### T10. Privacy: metadata logging -- **MEDIUM (privacy)**
*Category: Privacy/linkability.*
The request logger (`internal/api/middleware.go`) writes `method`, `path`, `status`, `latency`, `request_id` to stdout. Paths frequently contain PII (`/users/42/email`). Logs go to the container runtime → CloudWatch in prod. There is no allowlist for loggable paths, no hashing of path parameters, no redaction.
**Mitigations:** log registered route patterns (`/users/{id}`) not concrete paths; hash or truncate query strings; provide a config switch `logging.log_paths: none | pattern | full`.

### T11. Privacy: client identity leakage to target -- **MEDIUM (privacy)**
*Category: Privacy/linkability.*
The relay does not strip identifying headers coming from the client (`User-Agent`, `X-Forwarded-For` if set by a client-side proxy, `Referer`). It does set `Content-Type: application/json` but keeps everything else. A target can fingerprint callers through header combinations.
**Mitigations:** strip-by-default list (`User-Agent`, `Referer`, `Forwarded`, `Via`); set a neutral `User-Agent: private-relayer/1.0`; document the stripping policy.

### T12. Committed artefacts / repo hygiene -- **MEDIUM**
*Category: Information disclosure + Supply chain.*
- `relayer` (10 MB compiled binary) committed to git. Binary provenance unclear (who built it, with what toolchain, against what source). Clones trust a binary they didn't build.
- `AWSCLIV2.pkg` (55 MB) committed. Installer blob, zero reason to be in git.
- `configs/config.yaml` committed with a "real" dev API key (see T5).
- `.profile.md` gitignored (good) but lives next to public `private/` contents -- worth auditing.
**Mitigations:** `git rm --cached relayer AWSCLIV2.pkg`, add to `.gitignore`; remove `configs/config.yaml` from tracking; consider `git filter-repo` to purge from history before publishing; run `gitleaks` in CI to prevent regressions.

### T13. Error-path information disclosure -- **LOW**
*Category: Information disclosure.*
Error responses include raw error strings (`"invalid request body: " + err.Error()`). Usually fine -- Go's stdlib errors are safe -- but inadvertent leakage of filesystem paths or internal type names is possible.
**Mitigations:** return generic messages to clients ("bad request"); log the detailed error server-side with the request ID.

### T14. Dependency + container drift -- **LOW (today) / MEDIUM (over time)**
*Category: Supply chain.*
`govulncheck` and `trivy` are clean today, but this is a point-in-time baseline. Without automation, CVEs will land in dependencies and the alpine base image over weeks/months.
**Mitigations:** Dependabot for Go modules + the Dockerfile base tag; re-run scanners in CI on every PR and on a nightly schedule; pin the alpine patch version explicitly.

---

## 3. Tooling Stack

| Tool | Layer | What it catches | When it runs | Fail-build threshold |
|---|---|---|---|---|
| `govulncheck` | Source + deps | CVEs in Go stdlib + deps that are *reachable* from your code | Local + CI (PR + nightly) | Any vuln |
| `gosec` (via `golangci-lint`) | Static analysis | Go security antipatterns (SSRF-shaped code, weak crypto, hardcoded creds, unchecked errors on crypto paths) | Local + CI (PR) | HIGH or MEDIUM |
| `gitleaks` | Git tree + history | Committed secrets, tokens, keys | Local + CI (PR) + pre-commit | Any |
| `trivy image` | Container | OS package CVEs, Go binary CVEs, image secrets, misconfigs | CI after docker build + nightly | HIGH or CRITICAL |
| `trivy config` | IaC | Dockerfile + compose misconfigs (USER, HEALTHCHECK, network_mode host) | CI (PR) | HIGH |
| CodeQL (GitHub) | Source | Deeper semantic analysis, SSRF/injection patterns | CI (PR + weekly) | HIGH |
| Dependabot / Renovate | Deps + base image | Keeps deps current so scanners find less | Ongoing | — |
| ECR image scanning (prod) | Registry | Continuous CVE re-scan of pushed images | On push + daily | Notify |
| AWS Inspector (prod) | Runtime | Runtime agent findings on ECS tasks | Continuous | Notify |
| WAF (ALB) | Edge | OWASP rule set, rate-based rules, bad-bot | Runtime | Block |

We will **not** adopt all of these at once. The sequence matches the roadmap below.

---

## 4. Phased Roadmap

Slots into the existing 10-phase plan. New work goes into Phase 5.5 (pre-CI) and is reinforced in Phase 6 (CI), Phase 7 (AWS), and Phase 10 (hardening).

### Phase 5.5 -- Security baseline & code fixes (NOW)

**Goal:** Turn today's baseline scan into a fixed backlog; land the highest-impact code fixes before CI goes in.

1. **Fix the HIGH application threats (T1, T2, T3, T4, T5, T9, T10, T11)** -- code changes in `internal/relay/` and `internal/api/`:
   - URL construction via `url.Parse` + `ResolveReference`, reject non-slash paths, reject `@` and `://` in paths.
   - Custom `http.Client` with `CheckRedirect` that rejects private CIDRs (`10/8`, `172.16/12`, `192.168/16`, `127/8`, `169.254/16`, `::1`, `fc00::/7`, `fe80::/10`).
   - Per-target `allow_http: false` default; fatal at config load if a target URL is `http://` without the flag.
   - Request-header allowlist (`Content-Type`, `Accept`, `Accept-Encoding`, `X-*` custom). Deny-list for `Authorization`, `Host`, `Cookie`, `Set-Cookie`, `Connection`, `Upgrade`, `Transfer-Encoding`, `Content-Length`, `X-Forwarded-*`. Configurable.
   - Method allowlist: `GET POST PUT PATCH DELETE`.
   - `http.MaxBytesReader` at 1 MiB (configurable) on `/relay` and `/webhook` bodies.
   - `io.LimitReader` at 10 MiB (configurable) on target response bodies.
   - Rate-limit bucket eviction (time-based LRU; remove buckets idle > 10×window).
   - `http.Server.ReadHeaderTimeout` set.
2. **Secret hygiene (T5, T12)**:
   - `git rm --cached configs/config.yaml relayer AWSCLIV2.pkg`.
   - Add to `.gitignore`.
   - Rewrite `configs/config.example.yaml` with obviously-fake placeholders (`replace-me`).
   - Move API keys to env var `RELAYER_AUTH_API_KEYS` (comma-separated `name:key` pairs), backwards-compatible with YAML for dev.
   - Remove the committed dev key from `how_to.md` -- use `$API_KEY` in examples.
3. **Privacy controls (T9, T10, T11)** -- scoped, documented:
   - Strip `Authorization`, `Cookie`, `X-Api-Key` from jobs before Redis persistence.
   - Log registered route pattern (`/api/v1/relay`) instead of concrete path for now; revisit when we have user-defined paths.
   - Set neutral outbound `User-Agent: private-relayer/<version>`; strip inbound `User-Agent` and `Referer`.
4. **Suppress the false positives (T8, gosec G101 on header const)** with `// #nosec` comments + justification.
5. **Container hardening** in `docker-compose.yml`:
   - `read_only: true`, `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, `tmpfs: /tmp` if needed.
   - `mem_limit`, `pids_limit`, `cpus`.
6. **Threat-model doc (this file)** -- keep updated as items close.

**Deliverable:** baseline backlog → zero HIGH open; docs reflect reality; the scanners still pass.

### Phase 6 -- CI security gates

**Goal:** Every PR runs the same scanners locally, fails on regressions.

1. GitHub Actions workflow `.github/workflows/security.yml`:
   - Job `govulncheck`: `go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...`.
   - Job `gosec`: run via `golangci-lint run` (gosec linter enabled in `.golangci.yml`).
   - Job `gitleaks`: `gitleaks detect --source . --redact --exit-code 1`.
   - Job `trivy`: build image, `trivy image --severity HIGH,CRITICAL --exit-code 1`.
   - Job `trivy-config`: `trivy config infra/docker/` for Dockerfile/compose misconfigs.
   - Job `codeql`: GitHub-native CodeQL analysis (Go).
2. Dependabot: `.github/dependabot.yml` for `gomod`, `docker`, `github-actions` ecosystems.
3. Branch protection: require `security` workflow to pass on `main`.
4. Pre-commit hook (optional, opt-in via `scripts/install-hooks.sh`): gitleaks + gosec on staged files.

**Deliverable:** PRs can't merge with new HIGH findings; dependency updates auto-PR'd weekly.

### Phase 7 -- AWS / production controls

**Goal:** Move from "works in a container" to "secure in AWS".

1. **Secrets Manager** for API keys + Redis AUTH token. ECS task injects via `secrets:` in the task definition. No secrets in env vars, no secrets in YAML.
2. **ElastiCache with in-transit TLS** and an AUTH token. Security group allows only the ECS task's SG on :6379.
3. **IAM least-privilege**: ECS task role has `secretsmanager:GetSecretValue` for only the named secrets. ECS execution role has only ECR + CloudWatch.
4. **ALB**: HTTPS listener via ACM cert; redirect :80 → :443; WAF WebACL with the AWS Managed Core Rule Set + rate-based rule.
5. **VPC flow logs** to CloudWatch; ALB access logs to S3 with lifecycle 90d.
6. **ECR image scanning** enabled on push; block pushes with HIGH.
7. **GuardDuty + Inspector** enabled (account-level).
8. **OIDC** for GitHub Actions → AWS role assumption. No long-lived access keys.

### Phase 10 -- hardening & pen-test pass

**Goal:** Adversarial review before labelling the project "portfolio-ready."

1. **Load + abuse testing** with `k6`: realistic rate-limit exhaustion, SSRF probe payloads (`@169.254.169.254`, `//metadata.google.internal`), oversized bodies, slowloris.
2. **Dependency SBOM** via `cyclonedx-gomod` + `syft` for the image; publish as a release artifact.
3. **Threat-model refresh** based on findings.
4. **Runbook** for common alerts (auth failures surge, dead-letter queue growth, 5xx from a specific target).
5. **Public `SECURITY.md`** with disclosure contact.
6. **Consider user-private features** if we've committed to that goal (payload encryption, traffic-analysis resistance, client-side library in `pkg/client/`).

---

## 5. Privacy-Preserving Upgrades (Optional Track)

If we commit to user-private as a goal, these land in Phases 5.5b / 7 / 10:

- **Header stripping by default** -- already in T11 mitigations.
- **Pre-encrypted payloads** -- client encrypts body with per-target public key (age/NaCl box); relay forwards opaquely; target decrypts. Relay never sees plaintext.
- **Ephemeral job state** -- Redis TTL ≤ 5 min for completed jobs; dead-letter only keeps metadata, not body.
- **Minimal logging mode** -- `logging.mode: minimal` logs only status code + latency, no request-id-based correlation.
- **Traffic shaping** -- batch pending jobs in fixed time windows to break request/response timing correlation (costly in latency).
- **Tor/mix support** -- outbound HTTP via a SOCKS proxy (Tor daemon as a compose sidecar). Exotic; only if the operator wants unlinkability from the target.
- **Deniable job submission** -- accept a job hash now, require a second call with the payload; server can't prove which client submitted which job. Niche.

None of these are committed work. Listed so the decision is visible.

---

## 6. Baseline Scan Findings (2026-04-18)

Full command output saved to `/tmp/{govulncheck,gosec,gitleaks,trivy}.out`. Summary:

### 6.1 govulncheck
```
No vulnerabilities found.
```
Clean. Module uses recent chi v5.2.5, koanf v2.3.4, go-redis v9.18.0, zerolog v1.35.0 -- no known CVEs reach our code paths.

### 6.2 gosec
**2 findings, both false positives.**

| Severity | Rule | Location | Verdict |
|---|---|---|---|
| HIGH | G404 (weak random) | `internal/relay/retry.go:22` | **False positive.** Jitter does not require CSPRNG. Fix: add `// #nosec G404` with justification. |
| HIGH (LOW confidence) | G101 (hardcoded credentials) | `internal/auth/apikey.go:9` (`const headerAPIKey = "X-API-Key"`) | **False positive.** Header *name*, not value. Fix: `// #nosec G101`. |

No true positives. Note: gosec did **not** flag the SSRF/header-injection patterns in `internal/relay/relay.go` -- this is why static analysis alone is insufficient. Manual audit (§6.5) caught them.

### 6.3 gitleaks
**2 findings, 1 unique secret.**

Both findings are the same dev API key (`<revoked-dev-key>`) appearing in `how_to.md` in curl examples (lines 1130 and 1455). The same value also lives in `configs/config.yaml` (gitleaks didn't flag the YAML because the rule is `curl-auth-header`). This matches T5 + T12 in the threat model.

Fix:
- Remove `configs/config.yaml` from tracking (`git rm --cached`).
- Replace the dev key in `how_to.md` with `$API_KEY`.
- Add `.gitleaks.toml` allowlist for known-safe examples if any remain.

### 6.4 trivy (image scan)
```
private-relayer:local (alpine 3.21.7)    alpine      0 vulns
app/relayer                              gobinary    0 vulns
```
Clean. `alpine:3.21.7` is current; our Go binary is statically linked with up-to-date deps. This result will drift; re-scan in CI.

### 6.5 Manual code audit (not caught by any scanner)

Findings are documented in §2 as T1–T14. The ones that require code changes (all in Phase 5.5 step 1):

- **T1 (SSRF via URL concat)** -- `relay.go:186`
- **T2 (header injection)** -- `relay.go:199-201`
- **T3 (method allowlist)** -- `relay.go:69-71`
- **T4a (inbound body size)** -- `relay.go:51`, `webhook.go:21`
- **T4b (response body size)** -- `relay.go:103`
- **T4c (rate-limit bucket leak)** -- `ratelimit/limiter.go:41`
- **T7 (no HTTPS enforcement on target)** -- `target.go`, config load path
- **T9 (full payload in Redis)** -- `queue/queue.go` (persistence), `queue/worker.go` (use)
- **T10 (concrete paths in logs)** -- `api/middleware.go:20`
- **T11 (client-identity headers passed to target)** -- `relay.go:199-201` and inbound side

---

## 7. Open Questions for the Operator (You)

Before we execute Phase 5.5, decide:

1. **User-private or infra-private?** Drives whether we implement header stripping, payload encryption, and traffic shaping. Default: infra-private.
2. **Public repo or private?** If public, the `git filter-repo` cleanup of `relayer` / `AWSCLIV2.pkg` / `configs/config.yaml` is mandatory and needs a force-push decision.
3. **Which target host allowlist model?** (a) Any path under a registered target URL (today's model, once T1 is fixed), (b) explicit allowlisted paths per target, (c) allowlisted regex per target. Recommend (a) for now.
4. **Retention policy for job bodies?** Default 24h TTL today. Shorter is more private, longer is better for debugging. Recommend 1h for completed, 24h for failed/dead.
5. **Is `User-Agent` rewriting acceptable?** Some targets log/alert on UA changes. Recommend yes, default to a neutral UA.

---

## 8. Change Log

| Date | Who | Change |
|---|---|---|
| 2026-04-18 | initial | Threat model, tooling, roadmap, baseline scans complete. |
| 2026-04-18 | phase 5.5 | Closed T1 (SSRF via URL concat), T1b (private-IP dial filter), T2 (header allow/deny), T3 (method allowlist), T4a (inbound body cap), T4b (response body cap), T4c (rate-limit eviction), T5 (removed dev key from tracked config + docs), T7 (HTTPS enforcement on target URLs), T8 (nosec G404 with justification), T9 (headers scrubbed at webhook boundary + 1h TTL for completed jobs + body/headers cleared on completion), T10 (log route pattern not concrete path), T11 (outbound UA rewrite, Referer/Forwarded/Via stripped), G101 (nosec for header-name constant). Container hardened: read_only, cap_drop [ALL], no-new-privileges, tmpfs /tmp + /run, mem_limit, pids_limit. Added 6 new unit tests (31 total, all passing). Scanner deltas: gosec 2→0, gitleaks working-tree 2→0 (history-purge deferred to Phase 10). Partial: T6 (RealIP trust) -- deferred to Phase 7 once ALB CIDR is known. Not started: T12b history purge (Phase 10), T13 error-message tightening (Phase 10), T14 continuous drift scanning (Phase 6). |
| 2026-04-18 | phase 6 | Closed T14 via CI: two workflows (`ci.yml`, `security.yml`) + `dependabot.yml`. `security.yml` runs govulncheck + gitleaks (`--no-git`) + trivy image + trivy config on every PR and nightly (06:17 UTC cron), failing on HIGH/CRITICAL. `ci.yml` runs golangci-lint (now including gosec), `go test -race` with Redis service, and Docker build with GHA cache. Dependabot bumps gomod/docker/github-actions weekly. All workflows validated with actionlint; all local scanner commands green. Branch protection required checks documented in how_to.md §10.6. Not yet: CD pipeline (Phase 8), SBOM publication (Phase 10), ECR push (Phase 8). |
| 2026-04-19 | phase 6 fixup | Fixed CI on first push: bumped action versions (golangci-lint-action v6→v8, trivy-action 0.28.0→0.35.0, setup-buildx v3→v4, build-push v6→v7), pinned golangci-lint binary to v2.11.4 (v2.1.6 was compiled with Go 1.24, couldn't parse go.mod's 1.26.2), fixed Dockerfile CI build (dropped empty pkg/ copy, renamed configs/config.yaml copy to use config.example.yaml since live config is gitignored), bumped alpine 3.21→3.23. Removed `codeql.yml`: SARIF upload requires GitHub Advanced Security on private repos, not available on this plan. Trade: lost CodeQL's taint tracking; kept gosec + govulncheck + trivy + gitleaks. Re-add CodeQL in Phase 10 public showcase repo (free on public). |

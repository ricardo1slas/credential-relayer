# How To: Private Relayer -- Complete Development Guide

> This is the living document for the Private Relayer project. It explains **what** was built, **how** it was built, and **why** every decision was made. Updated after each phase.

---

## Table of Contents

1. [Project Objective](#1-project-objective)
2. [Architecture Overview](#2-architecture-overview)
3. [Technology Decisions](#3-technology-decisions)
4. [Phase 1: Project Scaffolding & Local Development](#4-phase-1-project-scaffolding--local-development) ✅
5. [Phase 2: Core Relay Logic](#5-phase-2-core-relay-logic) ⬜
6. [Phase 3: Authentication & Rate Limiting](#6-phase-3-authentication--rate-limiting) ⬜
7. [Phase 4: Redis Queue & Async Processing](#7-phase-4-redis-queue--async-processing) ⬜
8. [Phase 5: Docker & Docker Compose](#8-phase-5-docker--docker-compose) 🚧
9. [Phase 5.5: Security & Privacy Hardening](#9-phase-55-security--privacy-hardening) ✅
10. [Phase 6: CI/CD with GitHub Actions](#10-phase-6-cicd-with-github-actions) ✅
11. [Phase 7: AWS Infrastructure with Terraform](#11-phase-7-aws-infrastructure-with-terraform) ⬜
12. [Phase 8: Continuous Deployment](#12-phase-8-continuous-deployment) ⬜
13. [Phase 9: Observability & Alerting](#13-phase-9-observability--alerting) ⬜
14. [Phase 10: Hardening & Documentation](#14-phase-10-hardening--documentation) ⬜
15. [Glossary](#15-glossary)

---

## 1. Project Objective

### What Are We Building?

A **general-purpose request relayer service** -- a backend that sits between clients and target services, forwarding HTTP requests, webhooks, and messages with added reliability, security, and observability.

```
┌──────────┐       ┌─────────────────────┐       ┌──────────────┐
│  Client  │ ────> │   Private Relayer    │ ────> │  Target API  │
│ (caller) │       │  (this project)     │       │  (upstream)  │
└──────────┘       └─────────────────────┘       └──────────────┘
```

Think of it like a **smart proxy** that:
- Accepts requests from clients
- Authenticates and rate-limits them
- Forwards them to configured target services
- Handles retries, timeouts, and failures
- Queues heavy work for async processing
- Logs and monitors everything

### Why This Project?

This project exists to demonstrate **production-grade cloud/DevOps skills** through a real, working system. It's not a toy CRUD app -- it touches every layer of the stack:

| Skill Area | How This Project Demonstrates It |
|---|---|
| Backend Development | Go service with clean architecture |
| Containerization | Multi-stage Docker builds, docker-compose |
| Infrastructure as Code | Terraform modules for AWS (VPC, ECS, ALB, etc.) |
| CI/CD | GitHub Actions pipelines for build, test, deploy |
| Observability | Prometheus metrics, CloudWatch dashboards, alerting |
| Security | API auth, rate limiting, container scanning, least-privilege IAM |
| Cloud Architecture | ECS Fargate, ALB, ElastiCache, Secrets Manager |

### Inspiration

Based on [OpenZeppelin Relayer](https://github.com/openzeppelin/openzeppelin-relayer), which relays blockchain transactions. We took the same architectural patterns (relay, queue, retry, config, metrics) but made it **chain-agnostic and general-purpose** -- it relays any HTTP request, not just blockchain transactions.

---

## 2. Architecture Overview

### High-Level Architecture

```
                    ┌─────────────────────────────────────────────────────┐
                    │              Private Relayer Service                │
                    │                                                     │
 ┌──────────┐      │  ┌───────────┐    ┌────────────┐    ┌───────────┐  │     ┌──────────────┐
 │          │      │  │           │    │            │    │           │  │     │              │
 │  Client  │─────>│  │   Auth    │───>│   Router   │───>│   Relay   │──│────>│  Target API  │
 │          │      │  │Middleware │    │  (chi)     │    │  Handler  │  │     │              │
 └──────────┘      │  └───────────┘    └─────┬──────┘    └───────────┘  │     └──────────────┘
                    │                        │                           │
                    │                   ┌────▼─────┐                    │
                    │                   │  Rate    │                    │
                    │                   │  Limiter │                    │
                    │                   └──────────┘                    │
                    │                                                     │
                    │  ┌───────────────────────────────────────────────┐  │
                    │  │              Async Path (Phase 4)             │  │
                    │  │                                               │  │
                    │  │  ┌──────────┐    ┌──────────┐    ┌────────┐  │  │
                    │  │  │ Webhook  │───>│  Redis   │───>│ Worker │  │  │
                    │  │  │ Receive  │    │  Queue   │    │  Pool  │  │  │
                    │  │  └──────────┘    └──────────┘    └────────┘  │  │
                    │  └───────────────────────────────────────────────┘  │
                    │                                                     │
                    │  ┌──────────────────────┐                          │
                    │  │  /metrics            │──> Prometheus / Grafana  │
                    │  │  /health  /ready     │──> Container orchestrator│
                    │  └──────────────────────┘                          │
                    └─────────────────────────────────────────────────────┘
```

### Request Flow (Synchronous Relay)

```
1. Client sends POST /api/v1/relay with JSON body
2. Auth middleware checks X-API-Key header
3. Rate limiter checks if client is within limits
4. Relay handler:
   a. Looks up target from config by name
   b. Builds HTTP request to target URL
   c. Sends request with timeout (context.WithTimeout)
   d. If failure: retries with exponential backoff + jitter
   e. Returns response to client
5. Request logger records method, path, status, latency
```

### Request Flow (Async Webhook)

```
1. External service sends POST /api/v1/webhook
2. Auth middleware verifies HMAC signature
3. Webhook handler:
   a. Validates payload
   b. Creates a Job (ID, payload, target, status=queued)
   c. Enqueues job to Redis
   d. Returns 202 Accepted with job ID
4. Worker pool:
   a. Dequeues job from Redis
   b. Executes relay to target
   c. Updates job status (completed/failed)
   d. If failed: retry with backoff, eventually dead-letter
5. Client can poll GET /api/v1/jobs/:id for status
```

### AWS Deployment Architecture (Phase 7+)

```
┌─────────────────────────────────────────────────────────────────┐
│                          AWS VPC                                │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                   Public Subnets (2 AZs)                │    │
│  │                                                         │    │
│  │  ┌──────────────────────────────────────────────────┐   │    │
│  │  │         Application Load Balancer (ALB)          │   │    │
│  │  │   - HTTPS listener (ACM cert)                    │   │    │
│  │  │   - Health check: GET /health                    │   │    │
│  │  └────────────────────┬─────────────────────────────┘   │    │
│  └───────────────────────┼─────────────────────────────────┘    │
│                          │                                      │
│  ┌───────────────────────┼─────────────────────────────────┐    │
│  │                Private Subnets (2 AZs)                  │    │
│  │                       │                                 │    │
│  │  ┌────────────────────▼─────────────────────────────┐   │    │
│  │  │            ECS Cluster (Fargate)                 │   │    │
│  │  │                                                  │   │    │
│  │  │  ┌─────────────┐  ┌─────────────┐               │   │    │
│  │  │  │  Task (AZ1) │  │  Task (AZ2) │  Auto-scaling │   │    │
│  │  │  │  relayer:v1  │  │  relayer:v1  │               │   │    │
│  │  │  └─────────────┘  └─────────────┘               │   │    │
│  │  └──────────────────────────────────────────────────┘   │    │
│  │                       │                                 │    │
│  │  ┌────────────────────▼─────────────────────────────┐   │    │
│  │  │        ElastiCache (Redis)                       │   │    │
│  │  │   - Queue backend                               │   │    │
│  │  │   - Rate limit state                             │   │    │
│  │  └──────────────────────────────────────────────────┘   │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │  CloudWatch         │  Secrets Manager  │  ECR          │    │
│  │  - Logs             │  - API keys       │  - Docker     │    │
│  │  - Alarms           │  - Redis auth     │    images     │    │
│  │  - Dashboards       │                   │               │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

### CI/CD Pipeline (Phase 6+8)

```
┌──────────┐     ┌──────────────────────────────────────────────────────┐
│          │     │                 GitHub Actions                       │
│   Push   │────>│                                                      │
│  to main │     │  CI Pipeline:                                        │
│          │     │  ┌────────┐  ┌──────┐  ┌───────┐  ┌──────────────┐  │
└──────────┘     │  │  Lint  │─>│ Test │─>│ Build │─>│ Scan (Trivy) │  │
                 │  └────────┘  └──────┘  └───┬───┘  └──────────────┘  │
                 │                            │                        │
                 │  CD Pipeline:              │                        │
                 │  ┌────────────┐  ┌─────────▼──────┐  ┌───────────┐ │
                 │  │ Push to    │─>│ Update ECS     │─>│ Smoke     │ │
                 │  │ ECR        │  │ Task Definition│  │ Test      │ │
                 │  └────────────┘  └────────────────┘  └───────────┘ │
                 └──────────────────────────────────────────────────────┘
```

---

## 3. Technology Decisions

Every library and tool was chosen deliberately. Here's why:

### Language: Go

**Why Go over Node.js, Python, or Rust?**
- **Performance**: Compiled, fast HTTP handling, low memory footprint -- matters for a relay that processes many requests
- **Concurrency**: Goroutines are perfect for a worker pool consuming a queue
- **Deployment simplicity**: Single static binary -- no runtime dependencies, tiny Docker images
- **Cloud-native ecosystem**: Go is the language of Docker, Kubernetes, Terraform, Prometheus -- recruiters and teams in DevOps expect it
- **Learning investment**: Directly applicable to future roles (writing operators, CLI tools, infrastructure services)

**Why not Rust?** (like OpenZeppelin Relayer) -- Rust is great but the learning curve is steep and Go is more common in DevOps roles. The goal is demonstrating cloud skills, not systems programming mastery.

### Router: chi v5

**Why chi over gin, fiber, or stdlib?**
- **stdlib-compatible**: chi handlers are standard `http.HandlerFunc` -- no framework lock-in, no magic
- **Middleware ecosystem**: RequestID, RealIP, Recoverer come built-in
- **Lightweight**: ~1000 lines of code. No reflection, no binding magic
- **Composable**: `r.Route("/api/v1", ...)` groups routes cleanly; middleware can be scoped per group

**Why not gin?** Gin uses its own `gin.Context` instead of stdlib's `http.Request`/`http.ResponseWriter`. That means every handler, middleware, and test is gin-specific. chi keeps everything portable.

**Why not stdlib alone?** stdlib's `http.ServeMux` (even the improved Go 1.22 version) lacks middleware chaining, route grouping, and URL parameter extraction. chi adds exactly these without adding bloat.

### Config: koanf

**Why koanf over viper?**
- **No global state**: viper uses a global singleton (`viper.GetString("key")`). koanf is instance-based -- you create a `koanf.New()`, load into it, and pass the config struct around. This is better for testing and avoids hidden coupling.
- **Simpler**: viper pulls in 15+ dependencies. koanf is modular -- you import only the providers you use (file, env, etc.)
- **Explicit**: The transform function `RELAYER_SERVER_PORT -> server.port` is written by us, not hidden in viper's internals

**How env var mapping works:**
```
Environment Variable    ->  Config Key      ->  Struct Field
RELAYER_SERVER_PORT     ->  server.port     ->  Config.Server.Port
RELAYER_QUEUE_REDIS_ADDR -> queue.redis.addr -> Config.Queue.Redis.Addr
```
The transform function:
1. Strips `RELAYER_` prefix
2. Lowercases the rest
3. Replaces `_` with `.` (koanf's nested key separator)

**Why support both YAML and env vars?** YAML is for local development (human-readable, version-controlled). Environment variables are for production (12-factor app, injected by ECS task definitions from Secrets Manager). Env vars override YAML, so you can have sane defaults in the file and override specific values per environment.

### Logging: zerolog

**Why zerolog over slog (stdlib) or zap?**
- **JSON-native**: Outputs structured JSON by default -- exactly what CloudWatch Logs expects. No parsing regex needed.
- **Zero-allocation**: Fastest structured logger in Go benchmarks. For a relay processing many requests, this matters.
- **Fluent API**: `logger.Info().Str("method", "GET").Int("status", 200).Msg("done")` -- reads naturally, fields are typed

**Why not slog?** slog (added in Go 1.21) is solid and stdlib, but zerolog's API is more ergonomic for high-volume logging. slog would also work fine -- this was a preference call.

**Log output format:**
```json
{
  "level": "info",
  "method": "GET",
  "path": "/health",
  "status": 200,
  "latency": 0.042,
  "request_id": "abc123",
  "time": "2026-04-09T19:10:38-07:00",
  "message": "request completed"
}
```
This structure means CloudWatch can:
- Filter by `level` (show only errors)
- Create metric filters on `status` (count 5xx responses)
- Search by `request_id` (trace a single request)

### Linting: golangci-lint v2

**Why lint at all?** Linting catches bugs before they reach production. `errcheck` alone has saved countless production incidents by ensuring error return values aren't silently ignored.

**Why these specific linters?**

| Linter | What It Catches | Why It Matters |
|---|---|---|
| `errcheck` | Unchecked error returns | Ignoring errors is Go's #1 source of silent failures |
| `govet` | Suspicious constructs (e.g., Printf format mismatches) | Catches bugs the compiler doesn't |
| `staticcheck` | Deprecated APIs, unnecessary conversions, dead code | Keeps code modern and clean |
| `unused` | Unused variables, functions, types | Removes dead code |
| `ineffassign` | Assignments to variables that are never read | Catches logic errors |

**Why v2 config format?** golangci-lint v2 changed its config schema. The `version: "2"` key at the top signals this. We use it because golangci-lint 2.x is current.

### Project Layout: Go Standard

**Why this directory structure?**

```
cmd/           <-- Entrypoints (main packages). One per binary.
internal/      <-- Private application code. Cannot be imported by other modules.
pkg/           <-- Public library code. CAN be imported by other modules.
configs/       <-- Configuration files.
infra/         <-- Infrastructure code (Terraform, Docker).
scripts/       <-- Helper scripts.
```

This follows the [Go project layout](https://github.com/golang-standards/project-layout) convention. The key insight:

- **`internal/`** means Go enforces that no external module can import this code. This is a **compile-time guarantee** of encapsulation. Your relay logic, auth middleware, and queue implementation are private to this module.
- **`pkg/`** is the opposite -- it's explicitly "this code is a library others can use." We'll put the relay client SDK here.
- **`cmd/relayer/`** has just `main.go`. Its only job is to wire things together and start the server. All logic lives in `internal/`.

---

## 4. Phase 1: Project Scaffolding & Local Development ✅

**Goal:** Get a running Go service with health endpoints, config loading, structured logging, and development tooling.

**Status:** COMPLETED

### Step 1: Initialize the Go Module

**Command:**
```bash
go mod init github.com/ricardo1slas/private_relayer
```

**What this does:** Creates `go.mod`, the file that declares this project as a Go module. The module path `github.com/ricardo1slas/private_relayer` is both an identifier and the import path other packages use.

**Why this path?** Convention is `github.com/<user>/<repo>`. It matches where the code will be hosted, so `go get` can resolve it.

**Result:** `go.mod` with:
```
module github.com/ricardo1slas/private_relayer
go 1.26.2
```

### Step 2: Create the Directory Structure

**Command:**
```bash
mkdir -p cmd/relayer internal/{api,config,queue,relay,auth,ratelimit,metrics} \
         pkg/client configs scripts infra/{terraform/{modules,environments/{dev,prod}},docker} \
         .github/workflows
```

**Why create empty directories now?** Two reasons:
1. It communicates intent -- anyone looking at the repo sees the planned architecture
2. It prevents decision fatigue later -- when you start Phase 2, you don't think "where should relay logic go?" You already decided: `internal/relay/`

### Step 3: Install Dependencies

**Command:**
```bash
go get github.com/go-chi/chi/v5@latest \
       github.com/knadh/koanf/v2@latest \
       github.com/knadh/koanf/parsers/yaml@latest \
       github.com/knadh/koanf/providers/file@latest \
       github.com/knadh/koanf/providers/env@latest \
       github.com/rs/zerolog@latest
```

**What `go get` does:** Downloads the package source code to your local module cache (`$GOPATH/pkg/mod/`) and adds it to `go.mod` (required dependency) and `go.sum` (cryptographic checksums for integrity verification).

**Why `@latest`?** Fetches the newest tagged version. In production, you'd pin to a specific version (e.g., `@v5.2.5`). For a new project, latest is fine.

### Step 4: Create the Configuration System

**File:** `internal/config/config.go`

**How it works, line by line:**

```go
type Config struct {
    Server  ServerConfig  `koanf:"server"`
    Queue   QueueConfig   `koanf:"queue"`
    Metrics MetricsConfig `koanf:"metrics"`
}
```
The struct tags (`koanf:"server"`) tell koanf how to map YAML keys to struct fields. The nested structs mirror the YAML hierarchy:
```yaml
server:           # -> Config.Server
  port: 8080      # -> Config.Server.Port
  read_timeout: 10s
```

**Config loading order (later values override earlier):**
```
1. YAML file (configs/config.yaml)     <-- base defaults
2. Environment variables (RELAYER_*)   <-- per-environment overrides
3. setDefaults()                       <-- fallback if both are empty
```

**Why this order?** It follows the 12-factor app principle: config in the environment. The YAML provides sane development defaults. In production on ECS, environment variables (injected from Secrets Manager) override them without touching the file.

**The setDefaults function:**
```go
func setDefaults(cfg *Config) {
    if cfg.Server.Port == 0 {
        cfg.Server.Port = 8080
    }
    // ...
}
```
This is a safety net. If someone removes a field from the YAML and doesn't set the env var, the service still starts with reasonable values instead of failing or binding to port 0.

### Step 5: Create the HTTP Router

**File:** `internal/api/router.go`

```go
func NewRouter(logger zerolog.Logger) chi.Router {
    r := chi.NewRouter()

    r.Use(middleware.RequestID)    // 1. Generate unique ID per request
    r.Use(middleware.RealIP)       // 2. Extract real client IP from X-Forwarded-For
    r.Use(requestLogger(logger))   // 3. Log every request with structured fields
    r.Use(middleware.Recoverer)    // 4. Catch panics, return 500 instead of crashing

    r.Get("/health", healthHandler())
    r.Get("/ready", readyHandler())

    r.Route("/api/v1", func(r chi.Router) {
        // Phase 2: relay endpoints will go here.
    })

    return r
}
```

**Why this middleware order matters:**

```
Request comes in
  │
  ▼
RequestID ──────── Assigns an ID FIRST so all downstream middleware can reference it
  │
  ▼
RealIP ─────────── Extracts the real client IP (important behind ALB/proxy)
  │
  ▼
requestLogger ──── Logs the request with the ID and IP from above
  │
  ▼
Recoverer ──────── Catches any panic in the handler below, logs it, returns 500
  │
  ▼
Handler ─────────── Your actual business logic
```

If Recoverer were first, it wouldn't have a request ID to log. If RequestID were after the logger, the logger wouldn't have an ID to include. Order matters.

**Why `/health` and `/ready` are separate from `/api/v1`:**
- Health/ready are **infrastructure endpoints** -- ALB checks them, Kubernetes probes them. They should not be behind auth middleware.
- `/api/v1/*` routes will get auth and rate limiting in Phase 3. Having them in a separate `r.Route` group means we can scope middleware to just that group.

### Step 6: Health and Readiness Endpoints

**File:** `internal/api/health.go`

**Why two separate endpoints?**

| Endpoint | Purpose | When it returns non-200 |
|---|---|---|
| `/health` | **Liveness** -- "Is the process alive?" | Never (if it's responding, it's alive) |
| `/ready` | **Readiness** -- "Can it serve traffic?" | When a dependency is down (e.g., Redis unreachable) |

This is a **Kubernetes/ECS pattern**:
- If **health** fails, the orchestrator restarts the container (it's crashed)
- If **ready** fails, the orchestrator stops routing traffic to it (it's alive but can't serve)

Right now `/ready` always returns 200. In Phase 4, it will check Redis connectivity. If Redis is down, the relayer can't process async jobs, so it should tell the load balancer to stop sending traffic.

**Why return JSON instead of plain text?**
```json
{"status": "ok"}
```
Consistency. Every endpoint returns JSON. Monitoring tools and scripts can parse responses uniformly.

### Step 7: Request Logging Middleware

**File:** `internal/api/middleware.go`

```go
func requestLogger(logger zerolog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            start := time.Now()
            ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

            defer func() {
                logger.Info().
                    Str("method", r.Method).
                    Str("path", r.URL.Path).
                    Int("status", ww.Status()).
                    Dur("latency", time.Since(start)).
                    Str("request_id", middleware.GetReqID(r.Context())).
                    Msg("request completed")
            }()

            next.ServeHTTP(ww, r)
        })
    }
}
```

**How this works -- the middleware pattern:**

A middleware in Go is a function that takes an `http.Handler` and returns an `http.Handler`. It wraps the original handler with additional behavior (before and/or after).

```
requestLogger wraps the handler:

BEFORE handler runs:
  - Record start time
  - Wrap ResponseWriter to capture status code

handler.ServeHTTP(ww, r)   <-- the actual handler runs here

AFTER handler runs (via defer):
  - Calculate latency
  - Read captured status code
  - Log everything
```

**Why `middleware.NewWrapResponseWriter`?** Go's `http.ResponseWriter` doesn't expose what status code was written. chi's wrapper intercepts `WriteHeader()` calls and stores the status code so we can read it after the handler finishes.

**Why `defer`?** The log statement runs after `ServeHTTP` completes, even if the handler panics (the Recoverer above will catch the panic, set status to 500, and then our deferred log will record it).

### Step 8: The Application Entrypoint

**File:** `cmd/relayer/main.go`

```go
func main() {
    // 1. Create logger
    logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

    // 2. Load config (YAML + env vars)
    cfg, err := config.Load(configPath)

    // 3. Create router (with middleware and routes)
    router := api.NewRouter(logger)

    // 4. Create and start HTTP server
    srv := &http.Server{
        Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
        Handler:      router,
        ReadTimeout:  cfg.Server.ReadTimeout,
        WriteTimeout: cfg.Server.WriteTimeout,
        IdleTimeout:  60 * time.Second,
    }
    srv.ListenAndServe()
}
```

**Why set ReadTimeout, WriteTimeout, and IdleTimeout?**

Without timeouts, a slow client can hold a connection open forever, exhausting server resources (this is called a **Slowloris attack**).

| Timeout | Default (no timeout) | Our Setting | Why |
|---|---|---|---|
| ReadTimeout | ∞ | 10s | Limits how long to wait for the client to send the full request |
| WriteTimeout | ∞ | 10s | Limits how long the server has to write the response |
| IdleTimeout | ∞ | 60s | How long to keep an idle keep-alive connection open |

**Why `CONFIG_PATH` env var?** Allows overriding the config file location without changing code. Useful in Docker (`CONFIG_PATH=/etc/relayer/config.yaml`) and testing (`CONFIG_PATH=testdata/config.yaml`).

### Step 9: The Makefile

**File:** `Makefile`

```makefile
build:
    go build -o $(BUILD_DIR)/$(APP_NAME) ./cmd/relayer

run:
    go run ./cmd/relayer

test:
    go test ./... -v

lint:
    golangci-lint run ./...

fmt:
    gofmt -w .

clean:
    rm -rf $(BUILD_DIR)
```

**Why a Makefile?** It's a standard entrypoint for any developer (or CI pipeline) joining the project. Instead of remembering `go build -o bin/relayer ./cmd/relayer`, you type `make build`. It's documented-by-default.

**Why `go run` for `make run` instead of building first?** During development, `go run` is faster -- it compiles and runs in one step without writing a binary. For production, you `make build` and run the binary directly.

### Step 10: Linter Configuration

**File:** `.golangci.yml`

**Why a config file instead of command-line flags?** Reproducibility. Every developer and CI pipeline gets the same linter settings. No "works on my machine" for lint.

### Phase 1 Verification

```bash
$ make build
# -> bin/relayer created

$ make lint
# -> 0 issues

$ make run
# -> Server starts on :8080

$ curl localhost:8080/health
{"status":"ok"}

$ curl localhost:8080/ready
{"status":"ready"}

# Log output (structured JSON):
{"level":"info","method":"GET","path":"/health","status":200,"latency":0.042,...}
```

### Phase 1 File Summary

| File | Purpose | Lines |
|---|---|---|
| `cmd/relayer/main.go` | Wire everything together and start server | 47 |
| `internal/config/config.go` | Load YAML + env config, define config structs | 85 |
| `internal/api/router.go` | HTTP router with middleware chain | 25 |
| `internal/api/health.go` | Health and readiness handlers | 25 |
| `internal/api/middleware.go` | Structured request logging | 30 |
| `configs/config.yaml` | Runtime configuration | 12 |
| `configs/config.example.yaml` | Configuration template | 12 |
| `Makefile` | Build, run, test, lint commands | 20 |
| `.golangci.yml` | Linter configuration | 16 |
| `go.mod` / `go.sum` | Dependency management | auto |

---

## 5. Phase 2: Core Relay Logic ✅

**Goal:** Implement the request forwarding engine with retries and timeouts.

**Status:** COMPLETED -- 12 tests passing, 0 lint issues.

### What Was Built

The relay system has three components:

```
┌──────────────────────────────────────────────────────────────┐
│                    POST /api/v1/relay                        │
│                                                              │
│  ┌──────────────┐   ┌─────────────────┐   ┌──────────────┐  │
│  │ relay.go     │──>│ target.go       │   │ retry.go     │  │
│  │              │   │                 │   │              │  │
│  │ - Parse JSON │   │ - Registry map  │   │ - Backoff    │  │
│  │ - Validate   │   │ - Lookup by name│   │ - Jitter     │  │
│  │ - Forward    │   │ - Defaults      │   │ - Cap at 30s │  │
│  │ - Retry loop │   │                 │   │              │  │
│  └──────────────┘   └─────────────────┘   └──────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

### Request/Response Format

**Request:**
```json
POST /api/v1/relay
{
  "target": "httpbin",
  "method": "GET",
  "path": "/get",
  "headers": {"X-Custom": "value"},
  "body": {"key": "value"}
}
```

**Success Response (200):**
```json
{
  "status": "relayed",
  "target": "httpbin",
  "response_code": 200,
  "response_body": {"args": {}, "headers": {...}},
  "attempts": 1,
  "latency": "335.21ms"
}
```

**Failure Response (502):**
```json
{
  "status": "failed",
  "target": "broken-svc",
  "error": "all 4 attempts failed: target returned 500",
  "attempts": 4,
  "latency": "6.2s"
}
```

### Step 1: Target Registry (`internal/relay/target.go`)

**What it does:** Stores configured relay targets in a map for O(1) lookup by name.

```go
type TargetConfig struct {
    Name    string        // "httpbin", "service-a", etc.
    URL     string        // base URL: "https://httpbin.org"
    Timeout time.Duration // per-request timeout: 5s
    Retries int           // max retry attempts: 3
}

type TargetRegistry struct {
    targets map[string]TargetConfig
}
```

**Why a registry instead of passing config directly?**
- **Validation at startup**: `NewTargetRegistry` checks for missing names and URLs. Fail fast at boot, not at request time.
- **O(1) lookup**: Map by name instead of linear scan through a slice on every request.
- **Defaults**: If timeout or retries aren't set, they get sane defaults (5s, 3 retries).

**Why defaults in the registry, not just in YAML?** Defense in depth. A misconfigured YAML (missing `timeout:`) shouldn't result in a 0-second timeout or infinite retries. The code enforces sensible minimums.

### Step 2: Retry Logic (`internal/relay/retry.go`)

**The backoff formula:**
```
delay = baseDelay × 2^attempt × jitter

Where:
  baseDelay = 500ms
  jitter = random float in [0.5, 1.0]
  cap = 30 seconds
```

**Concrete example:**
```
Attempt 0: 500ms × 1 × jitter = 250ms to 500ms
Attempt 1: 500ms × 2 × jitter = 500ms to 1s
Attempt 2: 500ms × 4 × jitter = 1s to 2s
Attempt 3: 500ms × 8 × jitter = 2s to 4s
Attempt 4: 500ms × 16 × jitter = 4s to 8s
...
Attempt N: capped at 15s to 30s
```

**Why exponential backoff?** If a target is overloaded and returning 500s, retrying immediately makes it worse. Exponential backoff gives it time to recover. Each retry waits longer, reducing pressure.

**Why jitter?** Without jitter, if 1000 requests all fail at the same time, they all retry at exactly 500ms, then 1s, then 2s -- synchronized waves that keep hitting the target. Jitter randomizes the delay so retries spread out over the window.

**Why 50-100% jitter range (not 0-100%)?** Full jitter (0-100%) can produce very short delays that barely help. Half-to-full jitter ensures a meaningful minimum wait while still preventing thundering herd.

**Why cap at 30s?** Without a cap, attempt 20 would be ~145 hours. The cap ensures we don't wait absurdly long. 30s is long enough for most services to recover from transient failures.

### Step 3: Relay Handler (`internal/relay/relay.go`)

**The request flow:**

```
Client POST /api/v1/relay
        │
        ▼
   Parse JSON body
   (invalid? → 400)
        │
        ▼
   Validate: target required
   (missing? → 400)
        │
        ▼
   Registry lookup by target name
   (not found? → 404)
        │
        ▼
   relayWithRetries() loop:
   ┌──────────────────────────────┐
   │ for attempt = 0..retries:   │
   │   if not first attempt:     │
   │     wait backoff(attempt)   │
   │   doRelay():                │
   │     build HTTP request      │
   │     set timeout (context)   │
   │     send to target          │
   │   if 5xx: retry             │
   │   if 4xx: return (no retry) │
   │   if transport error: retry │
   └──────────────────────────────┘
        │
        ▼
   Return response to client
```

**Key design decision: What gets retried?**

| Target Response | Retry? | Why |
|---|---|---|
| 2xx (success) | No | It worked |
| 4xx (client error) | No | The request is wrong -- retrying won't fix it |
| 5xx (server error) | Yes | The target is temporarily unhealthy |
| Transport error (timeout, DNS, connection refused) | Yes | Network issues are often transient |
| Context cancelled (client disconnected) | No | No one is waiting for the response |

**Why not retry 4xx?** A 400 Bad Request means the payload we sent is invalid. Sending it again will get the same 400. Retrying would waste time and resources. The relay returns the 4xx response to the caller so they can fix their request.

**The `doRelay` function:**
```go
func (h *Handler) doRelay(ctx context.Context, target TargetConfig, req RelayRequest) (*http.Response, error) {
    ctx, cancel := context.WithTimeout(ctx, target.Timeout)
    defer cancel()

    url := target.URL + req.Path
    httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bodyReader)
    // ... set headers ...
    return h.client.Do(httpReq)
}
```

**Why `context.WithTimeout` per request?** Each `doRelay` call gets its own timeout. If the target takes longer than `target.Timeout`, the context cancels and `client.Do` returns immediately with a deadline exceeded error. This prevents a slow target from holding the relay's goroutine forever.

**Why `defer cancel()`?** Even if the request completes before the timeout, calling `cancel()` releases the timer resources. Not calling it leaks a goroutine until the timeout fires.

### Step 4: Wiring It Together

**Changes to `cmd/relayer/main.go`:**

The entrypoint now:
1. Reads `relay.targets` from config
2. Builds a `TargetRegistry` from the config
3. Creates a `relay.Handler` with the registry and logger
4. Passes the handler to `NewRouter`

**Why map config types to relay types?** The `config` package defines `config.TargetConfig` and the `relay` package defines `relay.TargetConfig`. They have the same fields but are separate types. This keeps the packages decoupled -- `relay` doesn't import `config`, so it can be tested and used independently. `main.go` does the mapping.

**Changes to `internal/api/router.go`:**
```go
func NewRouter(logger zerolog.Logger, relayHandler *relay.Handler) chi.Router {
    // ...
    r.Route("/api/v1", func(r chi.Router) {
        r.Post("/relay", relayHandler.ServeHTTP)
    })
}
```

The relay handler implements `http.Handler` via its `ServeHTTP` method, so it plugs directly into chi's routing.

### Step 5: Unit Tests (`internal/relay/relay_test.go`)

**12 tests covering every path:**

| Test | What It Verifies |
|---|---|
| `TestRelaySuccess` | Happy path: target returns 200, relay returns 200 with response body |
| `TestRelayTargetNotFound` | Unknown target name returns 404 |
| `TestRelayBadRequest` | Missing `target` field returns 400 |
| `TestRelayInvalidJSON` | Malformed JSON body returns 400 |
| `TestRelayRetriesOn5xx` | Target fails 2x with 500, succeeds on 3rd -- relay returns 200, reports 3 attempts |
| `TestRelayAllRetriesExhausted` | Target always 500 -- relay returns 502 after all retries |
| `TestRelayTimeout` | Target sleeps 3s, timeout is 100ms -- relay returns 502 |
| `TestRelayDoesNotRetryOn4xx` | Target returns 400 -- relay returns 200 (forwarding 4xx), only 1 attempt |
| `TestRelayForwardsHeaders` | Custom header in request is received by target |
| `TestRelayForwardsBody` | JSON body in request is received by target |
| `TestBackoffExponentialGrowth` | Backoff values fall within expected exponential range |
| `TestBackoffCap` | Very high attempt number doesn't exceed 30s cap |

**Why `httptest.NewServer`?** It creates a real HTTP server on a random localhost port. Our relay makes real HTTP requests to it. This tests:
- Actual HTTP transport (not mocked interfaces)
- Header forwarding (real HTTP headers)
- Body reading/writing (real io streams)
- Timeout behavior (real network timeouts)

**Why `atomic.Int32` for call counting?** The test server's handler runs in a separate goroutine (the httptest server). Using a regular `int` would be a data race. `atomic.Int32` is safe for concurrent access without a mutex.

### Phase 2 Verification

```bash
$ make lint
# 0 issues

$ make test
# 12 tests pass (8.3s -- retries add real delays)

$ make run
# Server starts

$ curl -X POST localhost:8080/api/v1/relay \
  -H "Content-Type: application/json" \
  -d '{"target":"httpbin","method":"GET","path":"/get"}'
# {"status":"relayed","target":"httpbin","response_code":200,...}
```

### Phase 2 File Summary

| File | Purpose | Lines |
|---|---|---|
| `internal/relay/relay.go` | Core relay handler with retry loop | ~165 |
| `internal/relay/target.go` | Target registry (config → map lookup) | ~55 |
| `internal/relay/retry.go` | Exponential backoff with jitter | ~25 |
| `internal/relay/relay_test.go` | 10 handler tests | ~220 |
| `internal/relay/retry_test.go` | 2 backoff tests | ~30 |
| `internal/config/config.go` | Added `RelayConfig` + `TargetConfig` types | +15 |
| `internal/api/router.go` | Wired relay handler into `/api/v1/relay` | +5 |
| `cmd/relayer/main.go` | Added registry creation + handler wiring | +15 |
| `configs/config.yaml` | Added `relay.targets` section | +6 |

---

## 6. Phase 3: Authentication & Rate Limiting ✅

**Goal:** Secure the API and prevent abuse.

**Status:** COMPLETED -- 9 new tests (21 total), 0 lint issues.

### What Was Built

```
Request to /api/v1/*
  │
  ▼
┌─────────────────────────────────────────────────┐
│ Auth Middleware (internal/auth/apikey.go)        │
│                                                 │
│  Read X-API-Key header                          │
│  ├── Missing? → 401 {"error":"missing API key"} │
│  ├── Wrong?   → 401 {"error":"invalid API key"} │
│  └── Valid?   → Set key name in context, continue│
└────────────────────┬────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────┐
│ Rate Limit Middleware (internal/ratelimit/)      │
│                                                 │
│  Get client ID (API key name or RemoteAddr)     │
│  Get/create token bucket for this client        │
│  ├── Tokens available? → Consume 1, continue    │
│  └── Empty? → 429 + Retry-After header          │
└────────────────────┬────────────────────────────┘
                     │
                     ▼
              Handler (relay, etc.)
```

**Health/ready endpoints bypass both** -- they're registered outside the `/api/v1` route group.

### Step 1: API Key Authentication (`internal/auth/apikey.go`)

**How it works:**

```go
type APIKeyAuth struct {
    keys map[string]string  // key value -> key name ("secret-123" -> "my-client")
}
```

1. Client sends `X-API-Key: secret-123` header
2. Middleware iterates through known keys using constant-time comparison
3. If match found, stores the key **name** (not the key itself) in the request context
4. Downstream middleware and handlers access the name via `auth.KeyName(ctx)`

**Why constant-time comparison (`crypto/subtle.ConstantTimeCompare`)?**

A naive string comparison (`==`) short-circuits: comparing `"secret"` with `"saaaaa"` returns faster than comparing with `"secree"` because it stops at the first mismatched character. An attacker can measure these timing differences to guess the key character by character.

`ConstantTimeCompare` always takes the same time regardless of where the mismatch is. This is standard practice for comparing secrets.

```go
func (a *APIKeyAuth) lookup(candidate string) (string, bool) {
    for key, name := range a.keys {
        if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
            return name, true
        }
    }
    return "", false
}
```

**Why store key name in context, not the key value?**

The key name (`"dev-client"`) is safe to log, pass to rate limiters, and include in metrics. The key value (`"secret-123"`) is a secret that should never appear in logs. By putting only the name in context, we make it hard to accidentally leak the secret.

**Context propagation pattern:**

```go
// internal/auth/context.go
type contextKey struct{}  // unexported type = only this package can read/write it

func withKeyName(ctx context.Context, name string) context.Context {
    return context.WithValue(ctx, contextKey{}, name)
}

func KeyName(ctx context.Context) string {
    name, _ := ctx.Value(contextKey{}).(string)
    return name
}
```

**Why an unexported struct for the context key?** Using a string key like `"auth-key-name"` risks collisions -- any package could accidentally use the same string. An unexported struct type can only be referenced by the `auth` package, guaranteeing no collisions.

### Step 2: Rate Limiting (`internal/ratelimit/limiter.go`)

**Algorithm: Token Bucket**

```
Imagine a bucket that holds 100 tokens (= 100 requests/minute).

Tokens refill at a steady rate: 100/60 ≈ 1.67 tokens per second.

Each request consumes 1 token.

If the bucket is empty, the request is rejected with 429.

The bucket never holds more than 100 tokens (the max).
```

**Why token bucket over other algorithms?**

| Algorithm | Pros | Cons |
|---|---|---|
| Fixed Window | Simple | Boundary problem: 200 requests in 2 seconds if they straddle the window boundary |
| Sliding Window | Accurate | Requires storing per-request timestamps -- memory-heavy |
| Token Bucket | Smooth, burst-friendly | Slightly more complex than fixed window |
| Leaky Bucket | Smooth output rate | Doesn't allow bursts -- too strict for API use |

Token bucket is the best fit because:
- It allows **bursts** (a client can use all 100 tokens in 1 second if they have them)
- It **smooths over time** (tokens refill continuously, not in chunks)
- It's **simple to implement** (just track tokens + last refill time)

**How token refill works:**

```go
func (l *Limiter) allow(key string) (bool, time.Duration) {
    b := l.getBucket(key)
    b.mu.Lock()
    defer b.mu.Unlock()

    now := time.Now()
    elapsed := now.Sub(b.lastFill).Seconds()
    b.tokens += elapsed * b.rate     // refill based on elapsed time
    if b.tokens > b.maxTokens {
        b.tokens = b.maxTokens       // cap at max
    }
    b.lastFill = now

    if b.tokens >= 1 {
        b.tokens--                    // consume one token
        return true, 0
    }

    // Calculate when the next token will be available.
    retryAfter := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
    return false, retryAfter
}
```

We don't use a background goroutine to refill tokens. Instead, we calculate how many tokens should have been added since the last request ("lazy refill"). This is more efficient -- no goroutines running for inactive clients.

**Why `sync.Map` for buckets?**

```go
type Limiter struct {
    buckets sync.Map  // key name -> *bucket
}
```

`sync.Map` is optimized for the case where keys are mostly read and rarely written (exactly our pattern -- each client's bucket is created once, then read on every request). It's faster than a `map` + `sync.RWMutex` for this access pattern.

**Each bucket also has its own `sync.Mutex`** for updating token count. This means two different clients never block each other -- only concurrent requests from the **same** client need to synchronize.

**Why `Retry-After` header?**

```go
w.Header().Set("Retry-After", fmt.Sprintf("%.0f", retryAfter.Seconds()))
```

RFC 6585 recommends including `Retry-After` with 429 responses. It tells the client exactly how long to wait before retrying. Well-behaved clients will respect this and back off automatically.

### Step 3: Middleware Ordering in the Router

```go
r.Route("/api/v1", func(r chi.Router) {
    r.Use(apiKeyAuth.Middleware)  // 1st: reject unauthenticated
    r.Use(limiter.Middleware)      // 2nd: reject over-limit
    r.Post("/relay", relayHandler.ServeHTTP)  // 3rd: process
})
```

**Why this order?**

1. **Auth before rate limit**: Don't waste rate limit checks on unauthenticated requests. Also, the rate limiter needs the API key name from context to identify the client.
2. **Rate limit before handler**: Don't do expensive relay work for a client that's been rate limited.
3. **Scoped to `/api/v1`**: Health/ready endpoints don't go through auth or rate limiting. The ALB needs to health-check without an API key.

### Step 4: Config Changes

```yaml
auth:
  api_keys:
    - name: "dev-client"
      key: "$API_KEY"

rate_limit:
  requests: 100    # 100 requests
  window: 60s      # per minute
```

**Why config, not hardcoded?** API keys change per environment. Dev has a test key. Prod keys come from Secrets Manager via environment variables. Rate limits may differ too (dev is generous, prod is strict).

**Why defaults in code?** If `rate_limit` is missing from YAML entirely, the service still works with 100 req/min. No crash, no zero-limit lockout.

### Step 5: Tests

**9 new tests across 2 packages:**

| Test | What It Verifies |
|---|---|
| `TestAPIKeyAuth_ValidKey` | Valid key returns 200, key name set in context |
| `TestAPIKeyAuth_MissingKey` | No header returns 401 with "missing API key" |
| `TestAPIKeyAuth_InvalidKey` | Wrong key returns 401 with "invalid API key" |
| `TestAPIKeyAuth_MultipleKeys` | Second of two configured keys works |
| `TestLimiter_AllowsUnderLimit` | 5 requests within 5-request limit all pass |
| `TestLimiter_RejectsOverLimit` | 4th request on a 3-request limit gets 429 |
| `TestLimiter_PerClientIsolation` | Client A being limited doesn't affect client B |
| `TestLimiter_UsesAPIKeyName` | Rate limit keys on auth context, not just IP |
| `TestLimiter_TokensRefill` | After exhaustion + wait, tokens refill and requests pass again |

### Phase 3 Verification

```bash
$ curl -X POST localhost:8080/api/v1/relay -d '{"target":"httpbin"}'
{"error":"missing API key"}                    # 401

$ curl -H "X-API-Key: wrong" -X POST localhost:8080/api/v1/relay -d '{"target":"httpbin"}'
{"error":"invalid API key"}                    # 401

$ curl -H "X-API-Key: $API_KEY" -X POST localhost:8080/api/v1/relay \
  -d '{"target":"httpbin","method":"GET","path":"/get"}'
{"status":"relayed","target":"httpbin",...}     # 200

$ curl localhost:8080/health
{"status":"ok"}                                # 200 (no auth needed)
```

### Phase 3 File Summary

| File | Purpose | Lines |
|---|---|---|
| `internal/auth/apikey.go` | API key middleware with constant-time comparison | ~65 |
| `internal/auth/context.go` | Context key helpers for passing auth info | ~15 |
| `internal/auth/apikey_test.go` | 4 auth tests | ~100 |
| `internal/ratelimit/limiter.go` | Token bucket rate limiter with per-client isolation | ~95 |
| `internal/ratelimit/limiter_test.go` | 5 rate limit tests | ~140 |
| `internal/config/config.go` | Added `AuthConfig`, `RateLimitConfig` types + defaults | +20 |
| `internal/api/router.go` | Scoped auth + rate limit middleware to `/api/v1` | +10 |
| `cmd/relayer/main.go` | Wired auth + rate limiter from config | +15 |
| `configs/config.yaml` | Added `auth` and `rate_limit` sections | +8 |

---

## 7. Phase 4: Redis Queue & Async Processing ✅

**Goal:** Reliable async delivery for webhooks and heavy relay jobs.

**Status:** COMPLETED -- 6 new tests (27 total), full end-to-end webhook flow verified.

### Why Async?

Synchronous relay (Phase 2) blocks the client until the target responds. This is fine for fast targets, but:
- If the target is slow (ML inference, batch processing), the client waits
- If the target is down, the client gets an error immediately
- There's no retry-after-failure mechanism for the client

The async path: accept the request immediately (202), queue it, process it in the background, let the client poll for status.

### Architecture

```
┌──────────┐    ┌───────────┐    ┌───────────┐    ┌──────────┐    ┌──────────┐
│  Client   │───>│  Webhook  │───>│   Redis   │───>│  Worker  │───>│  Target  │
│           │<───│  Handler  │    │   Queue   │    │  Pool    │    │  API     │
│  (202)    │    └───────────┘    └───────────┘    └──────────┘    └──────────┘
│           │                                           │
│  GET      │                                    ┌──────▼──────┐
│  /jobs/id │<───────────────────────────────────│  Job Status │
└──────────┘                                     └─────────────┘
```

### Step 1: Redis Connection (`internal/queue/redis.go`)

**What it does:** Creates a Redis client and verifies connectivity at startup with a `PING`.

```go
func NewRedisClient(cfg RedisConfig, logger zerolog.Logger) (*redis.Client, error) {
    client := redis.NewClient(&redis.Options{...})
    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("connecting to redis at %s: %w", cfg.Addr, err)
    }
    return client, nil
}
```

**Why fail fast?** If Redis is unreachable at startup, the service can't process async jobs. Better to crash immediately with a clear error than start and silently fail on every webhook.

**Why `go-redis/v9`?** It's the most widely used Go Redis client, supports context for cancellation/timeout, and has connection pooling built in. The `/v9` version uses Go generics and context natively.

**Health check:** The `HealthCheck` function is called by the `/ready` endpoint. If Redis goes down after startup, the readiness probe returns 503, telling the ALB to stop routing traffic.

### Step 2: Job Model (`internal/queue/job.go`)

```go
type Job struct {
    ID           string          // UUID
    Target       string          // relay target name
    Method       string          // HTTP method
    Path         string          // path on target
    Headers      map[string]string
    Body         json.RawMessage
    Status       JobStatus       // queued → processing → completed/failed/dead
    Attempts     int             // how many times we've tried
    MaxRetries   int             // max allowed attempts
    Error        string          // last error message
    ResponseCode int             // target's HTTP status code
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

**Job lifecycle state machine:**

```
                    ┌─────────┐
                    │ queued  │ ◄── Enqueue / Requeue
                    └────┬────┘
                         │ Worker picks up
                    ┌────▼────┐
                    │processing│
                    └────┬────┘
                    ┌────┴────┐
              ┌─────▼───┐  ┌──▼──────┐
              │completed │  │ failed  │
              └─────────┘  └────┬────┘
                           ┌────┴────┐
                     Yes   │ retries │   No
                    ┌──────┤ left?   ├──────┐
                    │      └─────────┘      │
               ┌────▼────┐            ┌─────▼───┐
               │ queued  │            │  dead   │
               │(requeue)│            │(dead-   │
               └─────────┘            │ letter) │
                                      └─────────┘
```

**Why 24-hour TTL on job data?** Jobs are stored as Redis keys with `SET ... EX 86400`. After 24 hours, stale job data is automatically cleaned up. Without a TTL, the Redis instance would grow unbounded.

### Step 3: Queue Operations (`internal/queue/queue.go`)

**Redis data structure:**

```
relayer:jobs:pending   [LIST]  -- job IDs waiting to be processed
relayer:jobs:dead      [LIST]  -- job IDs that exhausted retries
relayer:job:<uuid>     [STRING] -- JSON-serialized Job data
```

**Why Redis lists over streams?**

| Feature | Lists (LPUSH/BRPOP) | Streams (XADD/XREADGROUP) |
|---|---|---|
| Complexity | Simple | Complex |
| Consumer groups | No | Yes |
| Message acknowledgment | No (pop = consumed) | Yes |
| At-least-once delivery | Manual (requeue on failure) | Built-in |
| Good enough for us? | Yes (single consumer group) | Overkill |

We chose **lists** because:
- We have one worker pool consuming one queue -- no need for consumer groups
- We handle retries ourselves (requeue on failure)
- The code is ~50 lines vs ~150 for streams
- If we later need multi-consumer, we can upgrade to streams

**Enqueue uses a Redis pipeline:**

```go
pipe := q.client.Pipeline()
pipe.Set(ctx, jobPrefix+job.ID, data, 24*time.Hour)  // store job data
pipe.LPush(ctx, queueKey, job.ID)                      // add to queue
pipe.Exec(ctx)                                          // atomic
```

**Why pipeline?** Two Redis commands need to happen atomically. A pipeline sends them in one round-trip and ensures either both succeed or neither does. Without it, we could store the job data but fail to add it to the queue.

**Dequeue uses BRPOP (blocking pop):**

```go
result, err := q.client.BRPop(ctx, timeout, queueKey).Result()
```

**Why BRPOP instead of RPOP?** `RPOP` returns immediately with nil if the queue is empty, requiring a polling loop with sleep. `BRPOP` blocks until a job appears (up to `timeout`), which is more efficient -- no busy-waiting, no wasted CPU cycles.

### Step 4: Worker Pool (`internal/queue/worker.go`)

```go
func (wp *WorkerPool) Start(ctx context.Context) {
    for i := 0; i < wp.cfg.Workers; i++ {
        go wp.worker(ctx, i)
    }
    <-ctx.Done()  // block until shutdown
}
```

**Each worker loop:**

```
1. BRPOP from queue (blocks up to 5s)
2. If no job, loop back to 1
3. Set job status = processing, increment attempts
4. Call relay.Handler.ExecuteRelay()
5. If success: status = completed, store response code
6. If failure:
   a. If attempts < maxRetries: wait retryDelay, requeue
   b. If attempts >= maxRetries: send to dead-letter queue
7. Loop back to 1
```

**Why `ExecuteRelay` instead of making an HTTP request to ourselves?**

The worker could POST to `localhost:8080/api/v1/relay`, but that would:
- Go through auth middleware (the worker isn't a client)
- Go through rate limiting (the worker would rate-limit itself)
- Add network overhead (loopback HTTP vs function call)
- Create circular dependencies

Instead, we exposed `relay.Handler.ExecuteRelay()` -- the same core logic, called directly.

**Why configurable retry delay?** Workers wait `retry_delay` (default 30s) before requeueing a failed job. This gives the target time to recover. Without it, failed jobs would immediately re-enter the queue and fail again.

### Step 5: Webhook & Job Status Endpoints

**`POST /api/v1/webhook`** -- accepts same payload as `/relay` but returns 202 immediately:

```json
// Request
{"target": "httpbin", "method": "GET", "path": "/get"}

// Response (202 Accepted)
{"status": "accepted", "job_id": "a6fb3d77-e0ef-4901-98a2-8ec55688dde4"}
```

**`GET /api/v1/jobs/{id}`** -- returns full job state:

```json
{
    "id": "a6fb3d77-e0ef-4901-98a2-8ec55688dde4",
    "target": "httpbin",
    "method": "GET",
    "path": "/get",
    "status": "completed",
    "attempts": 1,
    "max_retries": 3,
    "response_code": 200,
    "created_at": "2026-04-18T17:02:48Z",
    "updated_at": "2026-04-18T17:02:49Z"
}
```

### Step 6: Readiness Probe Upgrade

```go
func readyHandler(redisClient *redis.Client) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if redisClient != nil {
            if err := queue.HealthCheck(r.Context(), redisClient); err != nil {
                // 503 Service Unavailable -- ALB stops routing traffic
                w.WriteHeader(http.StatusServiceUnavailable)
                return
            }
        }
        // 200 -- ready to serve
    }
}
```

**Why `nil` check on redisClient?** Future-proofing. If someone runs the relayer without Redis (sync-only mode), the ready check still works.

### Step 7: Graceful Shutdown

```go
// main.go
ctx, cancel := context.WithCancel(context.Background())
go workerPool.Start(ctx)

go func() {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    cancel()                    // stop workers
    srv.Shutdown(shutdownCtx)   // drain HTTP server
}()
```

**Why graceful shutdown?** Without it, `SIGTERM` (what ECS sends during deployments) would kill the process immediately. In-flight HTTP requests would get connection resets. Workers processing jobs would leave them in "processing" status forever.

With graceful shutdown:
1. Stop accepting new connections
2. Cancel worker context (workers finish current job, then stop)
3. Wait up to 10s for in-flight requests to complete
4. Exit cleanly

### Step 8: Router Refactor -- `RouterDeps`

```go
type RouterDeps struct {
    Logger       zerolog.Logger
    RelayHandler *relay.Handler
    APIKeyAuth   *auth.APIKeyAuth
    Limiter      *ratelimit.Limiter
    Queue        *queue.Queue
    RedisClient  *redis.Client
}

func NewRouter(deps RouterDeps) chi.Router { ... }
```

**Why a struct instead of 6 function parameters?** As the service grows, the router needs more dependencies. A struct is:
- Self-documenting (field names explain what each dependency is)
- Extensible (adding a new dependency doesn't change the function signature)
- Readable (no counting positional parameters)

### Step 9: Tests

**6 new integration tests (require Redis, skip if unavailable):**

| Test | What It Verifies |
|---|---|
| `TestEnqueueDequeue` | Job is enqueued, queue depth is 1, dequeue returns same job, depth is 0 |
| `TestGetJob` | Enqueued job is retrievable by ID |
| `TestGetJobNotFound` | Nonexistent ID returns error |
| `TestUpdateJob` | Status and attempts are persisted after update |
| `TestSendToDeadLetter` | Job moves to dead status and appears in dead-letter queue |
| `TestRequeue` | Failed job goes back to pending queue with queued status |

**Testing strategy:** Tests use Redis DB 15 (isolated from dev data in DB 0). Each test flushes the DB before running. If Redis is unavailable, `t.Skip` is called -- tests pass in CI without Redis, and run fully when Redis is present.

### Phase 4 Verification

```bash
$ redis-cli ping
PONG

$ make run
# "connected to redis"
# "starting worker pool"
# "worker started" x5

$ curl -X POST localhost:8080/api/v1/webhook \
  -H "X-API-Key: $API_KEY" \
  -d '{"target":"httpbin","method":"GET","path":"/get"}'
# {"status":"accepted","job_id":"a6fb3d77-..."}

# Worker log: "processing job" ... "job completed"

$ curl localhost:8080/api/v1/jobs/a6fb3d77-... -H "X-API-Key: ..."
# {"status":"completed","response_code":200,...}
```

### Phase 4 File Summary

| File | Purpose | Lines |
|---|---|---|
| `internal/queue/redis.go` | Redis connection + health check | ~40 |
| `internal/queue/job.go` | Job model and serialization | ~45 |
| `internal/queue/queue.go` | Enqueue, dequeue, update, dead-letter, requeue | ~110 |
| `internal/queue/worker.go` | Worker pool with configurable concurrency | ~100 |
| `internal/queue/queue_test.go` | 6 integration tests | ~170 |
| `internal/api/webhook.go` | POST /api/v1/webhook handler | ~55 |
| `internal/api/jobs.go` | GET /api/v1/jobs/{id} handler | ~35 |
| `internal/api/health.go` | Readiness probe now checks Redis | ~35 |
| `internal/api/router.go` | Refactored to RouterDeps, added new routes | ~45 |
| `internal/relay/relay.go` | Added ExecuteRelay() public method | +20 |
| `cmd/relayer/main.go` | Redis + queue + worker pool wiring + graceful shutdown | ~110 |
| `internal/config/config.go` | Added workers, retry_delay to QueueConfig | +10 |

---

## 8. Phase 5: Docker & Docker Compose 🚧

**Goal:** Containerize the relayer so the entire stack (app + Redis) comes up with a single command -- both for local development and as the basis for the ECS deployment in Phase 7.

**Status:** IN PROGRESS -- Dockerfile, `.dockerignore`, `docker-compose.yml`, and Makefile targets landed. Prometheus metrics + Grafana dashboard remain (tracked as Phase 5b).

### What Was Built

```
┌─────────────────────── docker compose up ──────────────────────────┐
│                                                                    │
│   ┌──────────────────────┐           ┌──────────────────────┐      │
│   │   relayer container  │  6379/tcp │   redis container    │      │
│   │  (alpine, non-root)  │──────────>│   redis:7-alpine     │      │
│   │   :8080 exposed      │           │   data volume: redis-data │  │
│   └──────────┬───────────┘           └──────────────────────┘      │
│              │ /health (curl)                                      │
│              │ HEALTHCHECK                                         │
│              ▼                                                     │
│      host: localhost:8080                                          │
└────────────────────────────────────────────────────────────────────┘
```

### Key Design Decisions

| Decision | Choice | Why |
|---|---|---|
| Base image (runtime) | `alpine:3.21` | Has a shell + `apk` so we can `docker exec -it relayer sh` to debug locally. `distroless/static` is smaller and more secure -- we'll likely swap to it for the production ECS image in Phase 7 once the app is stable and debugging is rare. |
| Base image (builder) | `golang:1.26-alpine` | Matches `go 1.26.2` in `go.mod`. Alpine keeps the builder small (~350MB vs 900MB for the Debian-based image) and layer caching still works. |
| Build stages | 2 (builder + runtime) | Final image ships only the binary and CA certs -- no Go toolchain, no source. Smaller attack surface, smaller pulls. |
| User | Non-root (UID 65532, `relayer`) | If the process is compromised, the attacker has no filesystem write access and can't install packages. 65532 is the conventional UID used by distroless/Kubernetes so the same image will behave the same once it lands on ECS/EKS. |
| CGO | `CGO_ENABLED=0` | Produces a fully static Go binary. No libc dependency at runtime -- image works on any Linux base, including future `scratch` or `distroless/static` swaps. |
| Build flags | `-trimpath -ldflags="-s -w"` | `-trimpath` strips absolute paths from the binary (reproducible builds). `-s -w` drops the symbol table and DWARF debug info -- smaller binary, no effect on runtime behavior. |
| BuildKit cache mounts | `--mount=type=cache,target=/go/pkg/mod` and `.../go-build` | Go module cache and build cache persist across `docker build` invocations. First build takes ~90s; subsequent code-only rebuilds take ~5s. |
| Healthcheck | `curl /health` every 10s | Matches the readiness probe we'll eventually configure on the ECS ALB. `curl` is in the runtime image explicitly for this. |
| Compose dependency order | `relayer` waits for `redis` `service_healthy` | Prevents the relayer from starting before Redis is accepting connections (otherwise the `NewRedisClient` PING in `main.go` fatals the process). |
| Redis addr override | `RELAYER_QUEUE_REDIS_ADDR=redis:6379` env var | The YAML default is `localhost:6379` for bare-metal `go run`. In compose, the env var maps via koanf's `RELAYER_*` → `.` rule to `queue.redis.addr` and wins over the YAML. Same mechanism will inject the ElastiCache endpoint in Phase 7. |
| Config mount | `configs/config.yaml:/app/configs/config.yaml:ro` | The file is baked into the image at build time for standalone `docker run`, but compose mounts the live file read-only so editing `configs/config.yaml` and `make docker-up` picks up changes without a rebuild. |

### Step 1: Multi-Stage Dockerfile (`infra/docker/Dockerfile`)

**Builder stage -- layer order matters:**

```dockerfile
COPY go.mod go.sum ./
RUN go mod download          # <-- cached unless go.mod/go.sum change

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
RUN ... go build ...         # <-- only this layer rebuilds on code changes
```

**Why copy `go.mod`/`go.sum` before the source?** Docker caches layers top-to-bottom. If we copied everything at once, any code change would invalidate `go mod download` and re-pull every dependency. Splitting the copy means dependency resolution is cached on every build except when you add/remove a module.

**Runtime stage -- minimal surface:**

```dockerfile
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S relayer \
    && adduser -S -G relayer -u 65532 relayer
COPY --from=builder /out/relayer /app/relayer
COPY configs/config.yaml /app/configs/config.yaml
USER 65532:65532
```

- `ca-certificates` is required so the relayer can make HTTPS calls to target services (otherwise `x509: certificate signed by unknown authority`).
- `tzdata` so timestamps in logs respect `TZ=...` if set.
- `curl` exists only so the HEALTHCHECK can hit `/health`. Alternative would be writing a tiny Go healthcheck subcommand -- not worth it at this stage.
- `USER 65532:65532` means the process starts as a non-root UID. `WORKDIR /app` and the copied files are readable; the container has no need to write anywhere persistent.

### Step 2: `.dockerignore`

**Why this matters:** Without `.dockerignore`, `COPY . .` (or the build context itself) includes the committed `relayer` binary (10 MB), `AWSCLIV2.pkg` (55 MB), `.git` history, and the `private/` folder. That bloats the build context, slows down `docker build`, and risks leaking `.profile.md` or plan notes into image layers.

Key exclusions:
- `bin/`, `relayer` -- host-built artifacts, unrelated to the container binary
- `AWSCLIV2.pkg` -- installer accidentally committed; 55 MB
- `private/` -- plan notes and profile markdown are not for the image
- `infra/terraform`, `scripts` -- not part of the runtime
- `**/*_test.go` -- tests don't run inside the image; excluding them makes the build context a bit smaller (they're never referenced in `go build ./cmd/relayer` anyway, so this is just hygiene)

### Step 3: `docker-compose.yml` (`infra/docker/docker-compose.yml`)

Two services:

**`redis`:**
- Image: `redis:7-alpine` (official, tiny)
- Port `6379` published to the host so you can point `redis-cli` at `localhost:6379` for debugging
- Named volume `redis-data` for job persistence across restarts
- Healthcheck: `redis-cli ping`

**`relayer`:**
- Built from the repo root context with `dockerfile: infra/docker/Dockerfile`
- `depends_on: redis: condition: service_healthy` so compose blocks start until Redis passes its healthcheck
- Environment: `RELAYER_QUEUE_REDIS_ADDR=redis:6379` -- uses compose's internal DNS (`redis` is the service name)
- Config mounted read-only for edit-reload workflow
- Port `8080` published to the host

**Why build context is `../..`?** The compose file lives in `infra/docker/`, but the Dockerfile needs to `COPY cmd ./cmd` etc. from the repo root. Setting `context: ../..` points Docker at the repo root; `dockerfile: infra/docker/Dockerfile` tells it which file to use.

### Step 4: Makefile Targets

```makefile
COMPOSE := docker compose -f infra/docker/docker-compose.yml

docker-build:   $(COMPOSE) build
docker-up:      $(COMPOSE) up -d
docker-down:    $(COMPOSE) down
docker-logs:    $(COMPOSE) logs -f
docker-ps:      $(COMPOSE) ps
```

**Why `docker compose` (v2) and not `docker-compose` (v1)?** Compose v1 was deprecated in 2023 and is a separate Python binary. Compose v2 is a Docker CLI plugin (`docker compose`), shipped with Docker Desktop and modern Docker Engine installs. All new work uses v2.

### Step 5: Running It

**First-time build + start:**
```bash
make docker-build    # ~90s on first build, ~5s on cached rebuilds
make docker-up       # starts relayer + redis detached
make docker-logs     # follow combined logs
```

**Smoke test:**
```bash
curl http://localhost:8080/health        # {"status":"ok"}
curl http://localhost:8080/ready         # 200 when redis is reachable

# Authenticated relay (default dev API key from configs/config.yaml):
curl -X POST http://localhost:8080/api/v1/relay \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"target":"httpbin","method":"GET","path":"/get"}'
```

**Shutdown:**
```bash
make docker-down          # stops containers, keeps the redis-data volume
make docker-down -v       # also removes the volume (wipe queue state)
```

### What's Next (Phase 5b)

The step-by-step plan's Phase 5 also includes Prometheus metrics and a Grafana dashboard. Tracking those separately so this phase closes cleanly:

- `internal/metrics/metrics.go` -- register counters (`relay_requests_total`, `relay_errors_total`), histograms (`relay_request_duration_seconds`), gauges (`queue_depth`, `workers_active`)
- `GET /metrics` handler using `promhttp.Handler()`
- Add `prometheus` and `grafana` services to `docker-compose.yml`
- `infra/docker/prometheus/prometheus.yml` -- scrape config pointing at `relayer:8080/metrics`
- `infra/docker/grafana/dashboards/relayer.json` -- provisioned dashboard

---

## 9. Phase 5.5: Security & Privacy Hardening ✅

**Goal:** Close the highest-impact application, supply-chain, and privacy risks before CI turns scanners into gates. The full threat model and long-form roadmap live in `private/security_roadmap.md`; this section is the human-readable story of *what was fixed, how, and why*.

**Status:** COMPLETED -- 14 threats addressed (11 hard fixes + 2 nosec-justified + 1 deferred), 4 new unit tests, baseline scanners still green, end-to-end smoke tests including SSRF/method-abuse/oversized-body attempts behave correctly.

### 9.0 Why Phase 5.5 Exists

The original plan had security as a Phase 10 "hardening" pass. Mid-way through Phase 5 (Docker) I paused and re-examined the project name -- `private_relayer` -- and the DevSecOps portfolio goal. Security isn't a polish step for a relay service; it's the thing. A relay that naïvely forwards client requests is an SSRF engine unless you design it otherwise. Pushing security to the end would have meant rewriting the relay handler after CI, AWS, and observability were already in place -- expensive and risky.

So Phase 5.5 slotted in between Docker (5) and CI (6). The sequencing logic:

```
Phase 5:   Containerize           ← make it reproducible
Phase 5.5: Security pass          ← fix what's broken before we gate on it
Phase 6:   CI turns the scanners  ← no baseline red alerts on day one
           into blocking gates
```

### 9.1 The "Private" Question: Two Very Different Goals

Before any fixes, we had to name the goal -- "private" in `private_relayer` can mean two different things, and they drive different features.

| | **Infra-private** (self-hosted) | **User-private** (privacy-preserving) |
|---|---|---|
| **"Private" refers to** | Who runs the relay | Who can see the traffic through it |
| **Goal** | You operate your own instance instead of a shared SaaS; data stays under your operational control. | Clients of the relay get anonymity or payload confidentiality -- even *from you, the operator*. |
| **Analog products** | Self-hosted GitLab, Plausible, Mailcow | Tor, Signal, iCloud Private Relay |
| **Can the operator see payloads?** | Yes. | No, by design (payloads are encrypted to the target, relayed opaquely). |
| **Threat model includes the operator?** | No -- operator is trusted. | Yes -- operator is an adversary. |
| **Typical features** | Auth, rate limits, audit logs, TLS, queue. | Above + client-side payload encryption, header stripping, traffic shaping, onion routing. |
| **Cost to implement** | Normal backend work. | Significant: key management, client SDK, correlation-attack resistance. |

**What the code did before Phase 5.5:** 100% infra-private. The relay faithfully forwarded whatever the client sent (including `Authorization` headers), logged identifying metadata, and stored full request payloads in Redis for 24h. Nothing cryptographic. Nothing anonymity-preserving.

**Decision: stay infra-private, with privacy as a first-class secondary goal.** Why:

- **Portfolio alignment.** The project targets DevOps / DevSecOps roles. Those jobs want evidence you understand the controls real cloud teams ship -- IAM, Secrets Manager, WAF, least-privilege, rate limiting, observability. They do *not* typically want evidence you can build Tor. User-private would be a research project in DevOps clothing.
- **Scope discipline.** User-private done poorly is worse than infra-private done well. Half-encrypted traffic gives false confidence; shipping a client SDK that claims end-to-end encryption but logs timing correlations would embarrass the project.
- **Privacy still matters.** Even with infra-private as the framing, we can and should:
  - strip client identity (`User-Agent`, `Referer`) from traffic going to targets,
  - scrub auth headers before persisting jobs in Redis,
  - log registered route patterns instead of concrete paths,
  - cap how long request bodies linger after a job completes.
  - These are all wins you get "for free" from good infra-private hygiene. They landed in Phase 5.5 below.

If we ever decide user-private is worth committing to, the security roadmap §5 sketches what that track looks like (pre-encrypted payloads, ephemeral state, traffic shaping, Tor outbound). Nothing in the current code blocks that path.

### 9.2 The Threat Model (in One Page)

Full version in `private/security_roadmap.md` §2. Brief here, categorised as the security-roadmap doc categorises them:

```
                    ┌──────────────────┐
                    │   Client (API)   │  Trust: hostile. Everything it sends must be validated.
                    └────────┬─────────┘
                             │
                    ╔════════▼═══════════════════════════╗
                    ║         Private Relayer            ║
                    ║                                    ║
                    ║  T1  SSRF / URL confusion  (HIGH)  ║   ← client-controlled path
                    ║  T2  Header injection      (HIGH)  ║     shifts host, auth laundering
                    ║  T3  Method abuse          (MED)   ║
                    ║  T4  Resource exhaustion   (MED)   ║   ← body/response size, memory
                    ║  T5  Weak secret posture   (HIGH)  ║
                    ║  T6  Trusted proxy headers (MED)   ║
                    ║  T7  Cleartext / downgrade (MED)   ║
                    ║  T8  math/rand for jitter  (FALSE  ║   ← gosec noise
                    ║                             POS)   ║
                    ║  T9  Payload persistence   (HIGH)  ║   ← privacy
                    ║  T10 Metadata in logs      (MED)   ║   ← privacy
                    ║  T11 Identity leak to tgt  (MED)   ║   ← privacy
                    ║  T12 Committed artefacts   (MED)   ║   ← repo hygiene
                    ║  T13 Error msg leak        (LOW)   ║
                    ║  T14 Dep/image drift       (LOW)   ║
                    ╚════════┬═══════════════════════════╝
                             │
                    ┌────────▼─────────┐
                    │   Target API     │  Trust: semi (you picked them).
                    │                  │  Can still: redirect to 169.254.169.254,
                    │                  │  return huge bodies, poison headers.
                    └──────────────────┘
```

Phase 5.5 closes T1 through T11 (minus T6 which is partial -- see §9.10). T12 history-purge is deferred to Phase 10 (public showcase repo). T13 and T14 are addressed in Phase 6 (CI) and Phase 10.

### 9.3 The Tools We Ran (And What They Actually Caught)

Four scanners, run locally before making any fixes, to set a baseline. Installed via `go install` and Homebrew (`brew install gitleaks trivy`). See `private/security_roadmap.md` §3 for the full tooling table and the per-scanner fail-build thresholds planned for CI.

| Tool | What it is for | Baseline finding | After Phase 5.5 |
|---|---|---|---|
| `govulncheck` | CVEs in Go deps *reachable from your code* (reachability-aware, official Go vuln DB) | Clean | Clean |
| `gosec` | Go static analysis for security antipatterns | 2 HIGH (both false positives) | 0 (suppressed with `#nosec` + justification) |
| `gitleaks` | Secrets in tree + git history | 2 (dev API key in `how_to.md` curl examples, same value also in `configs/config.yaml`) | 2 in git history only; working tree clean (deferred; history purge is a Phase 10 task) |
| `trivy image` | OS package CVEs + Go binary CVEs in the built container | Clean | Clean |

**Key lesson: scanners find regressions, not design flaws.** gosec did **not** flag the SSRF-shaped string concat at `relay.go:186`, did not flag the lack of header filter, did not flag unbounded body reads, did not flag the 24h retention of request bodies. Every HIGH finding in this phase came from manual code review. Scanners are necessary (they catch drift) but not sufficient (they miss intent).

### 9.4 Fix Group A -- Repo Hygiene (T5, T12)

**Problem.** `configs/config.yaml` was tracked and contained `key: "<revoked-dev-key>"` -- a real-looking API key anyone cloning the repo would see and could potentially reach across deployments if the value was reused. The committed `relayer` binary (10 MB) had unverifiable provenance. `AWSCLIV2.pkg` (55 MB) was an installer blob that had no business being in a Git history.

**Fix.**

```bash
git rm --cached relayer AWSCLIV2.pkg configs/config.yaml
```

`.gitignore` updated to ensure they stay out:
```
configs/config.yaml
relayer
*.pkg
```

`how_to.md` curl examples changed from `-H "X-API-Key: <revoked-dev-key>"` to `-H "X-API-Key: $API_KEY"` so reviewers copy-paste a placeholder, not a real-looking secret. `configs/config.example.yaml` retains `replace-me-with-a-random-secret` to signal "this is a placeholder."

**What's still flagged.** Gitleaks still finds 2 matches because the old commit `6943cd8` contains the key in `how_to.md` history. Rewriting history (via `git filter-repo --replace-text`) is a Phase 10 task tied to publishing the public showcase repo -- doing it now would force-push private history, which is acceptable but premature. The working tree is clean, so regressions from this point forward are preventable by CI.

**Why this isn't "security theatre".** The dev key itself was low-value, but treating it as a real secret during cleanup builds the muscle: never commit a credential-shaped value, even as a placeholder, even in a private repo. The next secret you commit *will* be the real one if the habit isn't there.

### 9.5 Fix Group B -- SSRF Hardening (T1, T1b, T7)

This is the most security-critical group. A relay service that does SSRF wrong is worse than no relay at all -- it's an attacker's proxy into whatever network the relay runs on.

**Problem 1 -- URL concatenation (T1).** `relay.go:186` had:
```go
url := target.URL + req.Path
```
If `target.URL = "https://httpbin.org"` and the client sent `path: "@evil.com/x"`, the resulting string `"https://httpbin.org@evil.com/x"` parses under Go's `net/url` as: scheme `https`, userinfo `httpbin.org`, host `evil.com`, path `/x`. The relay would then dial `evil.com`. Classic. The glossary even calls out SSRF as a term to know; it took a manual audit to see that the code did the exact thing the glossary warns about.

**Fix.** URL construction now goes through `url.ResolveReference` with explicit invariants:

```go
func buildTargetURL(base *url.URL, clientPath string) (string, error) {
    if clientPath == "" { return base.String(), nil }
    if !strings.HasPrefix(clientPath, "/") {
        return "", fmt.Errorf("path must start with /")
    }
    ref, err := url.Parse(clientPath)
    if err != nil { return "", fmt.Errorf("invalid path: %w", err) }
    if ref.Scheme != "" || ref.Host != "" || ref.User != nil {
        return "", fmt.Errorf("path must not contain scheme, host, or userinfo")
    }
    resolved := base.ResolveReference(ref)
    if resolved.Host != base.Host || resolved.Scheme != base.Scheme {
        return "", fmt.Errorf("path resolves outside target host")
    }
    return resolved.String(), nil
}
```

Three checks, each catches a different attack shape:
- Must start with `/` -- blocks `@evil.com/x`, `evil.com`, and other non-rooted paths that Go's parser treats as paths-that-look-like-hosts.
- Parsed reference must not carry a scheme, host, or userinfo -- blocks `//evil.com/x` (protocol-relative URL) and `https://evil.com/x` (absolute).
- Post-resolution host/scheme must match the base -- final belt-and-suspenders; if any parser edge case slips through, the mismatch check fires.

**Smoke test confirmed:**
```bash
$ curl -X POST localhost:8080/api/v1/relay \
    -H "X-API-Key: $API_KEY" \
    -d '{"target":"httpbin","method":"GET","path":"//evil.example.com/x"}'
{"status":"failed","error":"building URL: path must not contain scheme, host, or userinfo",...}
```

**Problem 2 -- private-network dials (T1b).** Even with URL validation, an attacker could point a target at `http://internal-service.local` or get the target to return an HTTP redirect to `http://169.254.169.254/latest/meta-data/` (AWS instance metadata). A custom `http.Transport.DialContext` handles both:

```go
DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
    host, port, _ := net.SplitHostPort(addr)
    ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
    if err != nil { return nil, err }
    if !allowPrivate {
        for _, ip := range ips {
            if isPrivateIP(ip.IP) {
                return nil, fmt.Errorf("dial blocked: %s resolves to private %s", host, ip.IP)
            }
        }
    }
    return baseDialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}
```

`isPrivateIP` covers RFC1918 (`10/8`, `172.16/12`, `192.168/16`), loopback (`127/8`, `::1`), link-local (`169.254/16`, `fe80::/10`), IPv6 ULA (`fc00::/7`), CGNAT (`100.64/10`), and the unspecified address. Because the filter runs inside `DialContext`, it catches both direct targets and redirect-follow dials -- redirects re-enter DialContext for the new host.

Per-target opt-out via `allow_private_ips: true` exists for legitimate dev use cases (relay to another Docker service), pushed down through a `context.Context` value so the transport-level hook knows which target it's dialing for.

**Problem 3 -- cleartext targets (T7).** `target.URL: "http://..."` was silently accepted; there was no way for an operator to notice at config-load time that a target would be called over unencrypted HTTP. Now the registry constructor rejects http:// unless `allow_http: true`:

```go
if u.Scheme == "http" && !t.AllowHTTP {
    return nil, fmt.Errorf("target %q: http:// rejected (set allow_http: true to override)", t.Name)
}
```

Also: `u.Scheme != "http" && u.Scheme != "https"` to catch `file://`, `ftp://`, `data:`, and Go net/url's various exotic accepted schemes. Userinfo in the base URL is also rejected -- no `https://user:pass@host`.

**Redirects.** `http.Client.CheckRedirect` caps hops at `max_redirects` (default 3) and rejects non-http(s) destinations. Each redirect still goes through `DialContext` because a new host means a new connection pool entry -- so the private-IP check catches redirect-driven SSRF too.

### 9.6 Fix Group C -- Request Validation (T2, T3, T4a, T4b)

**Problem -- header injection (T2).** `relay.go:199-201` copied every client-supplied header into the outbound request. That meant a client could set `Authorization: Bearer <relay's credential for the target>`, `Host: different-vhost.example.com`, `Transfer-Encoding: chunked` (Go sanitizes most outbound smuggling shapes, but belt-and-suspenders), or hop-by-hop headers. The relay was a credential laundering tool by accident.

**Fix.** Two sets, applied in this order:

```go
var deniedRequestHeaders = map[string]struct{}{
    // Hop-by-hop (RFC 7230 §6.1) -- meaningless to forward.
    "connection": {}, "keep-alive": {}, "upgrade": {}, "te": {}, "trailer": {},
    "transfer-encoding": {}, "content-length": {},
    // Auth + routing -- only the relay, not the client, sets these toward the target.
    "authorization": {}, "cookie": {}, "host": {},
    "proxy-authorization": {}, "proxy-authenticate": {},
    // Proxy hints -- relay might be behind an ALB; clients don't get to forge these.
    "x-forwarded-for": {}, "x-forwarded-proto": {}, "x-forwarded-host": {}, "x-real-ip": {},
}

var identityHeaders = map[string]struct{}{
    // Blocked unless preserve_client_identity / preserve_user_agent is set.
    "user-agent": {}, "referer": {}, "forwarded": {}, "via": {},
}
```

The filter function is explicit:
```go
func isHeaderAllowed(name string, preserveIdentity bool) bool {
    lc := strings.ToLower(name)
    if _, blocked := deniedRequestHeaders[lc]; blocked { return false }
    if _, isIdentity := identityHeaders[lc]; isIdentity { return preserveIdentity }
    return true
}
```

Allowlist by default would have been nicer, but relay's value proposition is "pass whatever headers through to the target"; a hard allowlist breaks too many legitimate API calls. The denylist is curated from the actual threat shapes: auth laundering, routing confusion, proxy spoofing, connection-layer attacks.

**Problem -- method abuse (T3).** No allowlist; `CONNECT`, `TRACE`, arbitrary strings were accepted. `CONNECT` in particular could turn the relay into a protocol-level proxy.

**Fix.** Explicit allowlist at the handler boundary:
```go
var allowedMethods = map[string]struct{}{
    http.MethodGet: {}, http.MethodPost: {}, http.MethodPut: {},
    http.MethodPatch: {}, http.MethodDelete: {},
}
```
Non-allowed methods return 400 before any retry logic fires (attempts: 0).

**Problem -- unbounded bodies (T4).** Inbound: `json.NewDecoder(r.Body).Decode(&req)` would read whatever the client sent, all into memory. Outbound response: `io.ReadAll(resp.Body)` -- a malicious or buggy target could OOM the relay by returning gigabytes.

**Fix.** Both paths are now capped, with config knobs:

```go
// Inbound (relay.go and webhook.go):
r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxRequestBytes)  // default 1 MiB
// oversized → http.MaxBytesError → 413 response

// Outbound (relay.go):
body, _ := io.ReadAll(io.LimitReader(resp.Body, h.cfg.MaxResponseBytes))  // default 10 MiB
```

The inbound limit uses `http.MaxBytesReader` which has a special error type that the handler maps to 413 (`Payload Too Large`) instead of 400 -- correct semantics matter for callers that retry on 4xx. Smoke test:

```bash
$ python3 -c "import json; print(json.dumps({'target':'x','body':'a'*2000000}))" \
  | curl -X POST localhost:8080/api/v1/relay \
      -H "X-API-Key: $API_KEY" --data-binary @-
HTTP 413
{"status":"error","error":"invalid request body: http: request body too large","attempts":0,...}
```

### 9.7 Fix Group D -- Privacy Controls (T9, T10, T11)

These land because we committed to privacy as a first-class secondary goal (see §9.1). Each one reduces the amount of client-identifying data the relay leaks to logs, to Redis, or to target services.

**T11 -- outbound User-Agent rewriting.** The relay now always sends `User-Agent: private-relayer/0.1` to the target, regardless of what the client sent. `Referer`, `Forwarded`, and `Via` are stripped. Per-target opt-out via `preserve_user_agent: true` for APIs that care about UA strings. The result: a target can't fingerprint individual callers by their UA -- everything looks like a single relay.

Confirmed in smoke test -- httpbin's echoed `headers.User-Agent` flipped from the default Go client (pre-change) to our neutral value:
```
"User-Agent": "private-relayer/0.1"
```

**T9 -- queue privacy.** Two problems: (a) auth headers in enqueued jobs were being persisted to Redis as JSON with a 24h TTL (insider risk, backup snapshot risk), and (b) even after a job completed, the full request body stayed in Redis for the full 24h.

Two fixes. First, at the webhook boundary, headers pass through `relay.StripSensitiveHeaders` before enqueue -- the same denylist as the relay forwards drops `Authorization`, `Cookie`, `Proxy-Authorization`, etc. before any bytes hit Redis:

```go
scrubbed := relay.StripSensitiveHeaders(req.Headers)
job, _ := q.Enqueue(ctx, req.Target, req.Method, req.Path, scrubbed, req.Body, 3)
```

Second, the queue distinguishes completion from failure for retention:

```go
func (q *Queue) MarkCompleted(ctx context.Context, job *Job, responseCode int) error {
    job.Status = StatusCompleted
    job.ResponseCode = responseCode
    job.Body = nil       // body served its purpose -- drop it
    job.Headers = nil    // same
    job.UpdatedAt = time.Now()
    // ...
    return q.client.Set(ctx, jobPrefix+job.ID, data, q.completedTTL).Err()  // 1h default
}
```

- **Completed** jobs: body + headers cleared, metadata-only, TTL 1 hour. Clients polling `/api/v1/jobs/{id}` still see the status, response_code, attempts, and timestamps -- enough to reconcile, not enough to leak.
- **Failed / dead-lettered** jobs: keep everything for 24h (`failed_ttl`) so operators can debug.

Verified against live Redis after a successful webhook round-trip:
```
$ docker exec relayer-redis redis-cli GET relayer:job:<id>
{"id":"...","target":"httpbin","method":"GET","path":"/get","status":"completed",
 "attempts":1,"max_retries":3,"response_code":200,"created_at":"...","updated_at":"..."}
$ docker exec relayer-redis redis-cli TTL relayer:job:<id>
3584    ← ~59 minutes, as expected
```
No `headers` field. No `body` field. 1h expiry. The Authorization header we sent in the webhook request is nowhere in Redis -- it was stripped at the webhook boundary before Enqueue was even called.

**T10 -- log path sanitisation.** Chi's `RequestID` middleware sets a request ID; our request logger wrote:
```go
.Str("path", r.URL.Path)  // e.g., "/api/v1/jobs/abc-123-def"
```
Concrete paths are routinely PII-shaped -- user IDs, tokens, slugs -- and they flow into stdout → container logs → CloudWatch. The fix uses Chi's route-pattern API:
```go
routePath := chi.RouteContext(r.Context()).RoutePattern()  // e.g., "/api/v1/jobs/{id}"
if routePath == "" { routePath = r.URL.Path }              // fall back for unrouted 404s
logger.Info().Str("route", routePath)...
```
Operators still get the per-route latency/error breakdown they need for observability; the concrete IDs stay out of log storage. Fallback preserves visibility on 404s before routing resolves.

### 9.8 Fix Group E -- Operational Controls (T4c, T8, G101)

**T4c -- rate-limit bucket eviction.** The token-bucket limiter stored one `*bucket` per unique key (API-key name, or `RemoteAddr` for unauthenticated traffic) in a `sync.Map`. Map entries were never removed -- a slow memory leak. An attacker rotating IPs could accelerate it. The fix is a sweep goroutine:

```go
func (l *Limiter) StartEviction(ctx context.Context, logger zerolog.Logger) {
    sweep := l.cfg.Window * 2
    idleCutoff := l.cfg.Window * 10
    ticker := time.NewTicker(sweep)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            cutoff := time.Now().Add(-idleCutoff)
            l.buckets.Range(func(k, v any) bool {
                b := v.(*bucket)
                b.mu.Lock()
                stale := b.lastFill.Before(cutoff)
                b.mu.Unlock()
                if stale { l.buckets.Delete(k) }
                return true
            })
        }
    }
}
```

Wired into `main.go` as `go limiter.StartEviction(ctx, logger)` alongside the worker pool. Shares the cancellation context so graceful shutdown stops it.

**T8 -- `math/rand` for jitter.** gosec flagged `retry.go:22` (`rand.Float64()` for backoff jitter) as HIGH. Suppressed with justification:
```go
// #nosec G404 -- jitter is timing variance, not a security boundary; CSPRNG overhead unwarranted.
jitter := 0.5 + rand.Float64()*0.5
```
Retry jitter does *not* need cryptographic randomness; its purpose is to de-synchronise retriers, not to resist adversarial prediction. `crypto/rand` here would burn syscalls and gain nothing.

**G101 -- header *name* constant.** gosec flagged `const headerAPIKey = "X-API-Key"` as a potential hardcoded credential. It is a header name, not a credential value. Suppressed:
```go
// #nosec G101 -- this is the header *name*, not a credential value.
const headerAPIKey = "X-API-Key"
```

Both suppressions are visible in `grep -n '#nosec' internal/` -- the intent is to make the suppression greppable and auditable; a reviewer can see exactly what was suppressed and why.

**Server read-header timeout.** `http.Server.ReadHeaderTimeout` was not set, which makes the server theoretically vulnerable to a Slowloris-style attack where a client sends headers one byte at a time to hold a connection open. Added a 5 s default, configurable via `server.read_header_timeout`.

### 9.9 Fix Group F -- Container Hardening

Complements Phase 5's Docker work with runtime-level restrictions. `infra/docker/docker-compose.yml` now applies to both services:

```yaml
read_only: true            # container root FS is immutable
tmpfs:
  - /tmp                   # writable scratch (Go may need /tmp)
  - /run                   # redis wants /run for its pidfile
cap_drop: [ALL]            # no Linux capabilities (no CAP_NET_BIND_SERVICE, etc.)
security_opt:
  - no-new-privileges:true # child processes can never gain privs
mem_limit: 256m            # OOM kill before exhausting host
pids_limit: 200            # fork bomb ceiling
```

One subtlety: the Redis image's entrypoint runs as root and tries to `setuid` down to `redis`. `cap_drop: [ALL]` removes `CAP_SETUID`, breaking the switch with `failed switching to "redis": operation not permitted`. Fix: `user: redis` on the Redis service so the entrypoint starts as the right UID and doesn't need to drop privs.

Verified with `docker inspect`:
```
CapDrop       = ["ALL"]
SecurityOpt   = ["no-new-privileges:true"]
ReadonlyRootfs = true
```

### 9.10 What Was *Not* Fixed in Phase 5.5 (and Why)

| Item | Where it lives | Why deferred |
|---|---|---|
| **T6 proxy-header trust** (partial) | `middleware.RealIP` unconditionally trusts `X-Forwarded-For` from the direct peer | Behind an ALB this is correct. On a direct-internet deployment it's a rate-limit bypass. Fix (trusted-proxy CIDR allowlist) lands in Phase 7 when we know the real ALB CIDR. |
| **T12b history purge** | `gitleaks` still flags the dev key in commit `6943cd8` | Rewriting history now means force-pushing private history and invalidating anyone's clone. Cleaner to do once, in Phase 10, when we fork the public showcase repo via `git filter-repo`. |
| **T13 error-message leakage** (generic client-facing errors) | Client-facing 4xx/5xx messages still include raw Go error strings | Low priority. Go stdlib errors are generally safe; real leakage risk is filesystem paths, which this code doesn't expose. Will tighten in Phase 10 with a generic "bad request" + request-ID trailer. |
| **T14 continuous drift** | Nothing today nightly-scans deps and the alpine base image | This is literally Phase 6's purpose -- CI with nightly scanner runs + Dependabot. |
| **Secrets Manager integration** | API keys still loaded from YAML (or env var override) | Phase 7 -- the ECS task definition injects Secrets Manager values via the `secrets:` array. Premature to implement before we have AWS. |
| **ElastiCache TLS + AUTH** | Redis is cleartext on the Docker bridge | Phase 7 -- local Docker bridge is acceptable; ElastiCache requires in-transit TLS + AUTH token. |
| **WAF / ALB logging / VPC flow logs** | — | Phase 7. |
| **Pen-test pass / k6 abuse suite** | — | Phase 10. |

**The discipline:** every deferred item is mapped to a specific later phase, not left vague. That way Phase 6 and Phase 7 aren't "implement CI + AWS"; they're "implement CI + AWS *and close these specific security items*."

### 9.11 Before / After: Scanner Baseline

All four scanners re-run after the code changes, against the rebuilt container. Results captured in `private/security_roadmap.md` §8 changelog.

| Tool | Before Phase 5.5 | After Phase 5.5 | Net change |
|---|---|---|---|
| `govulncheck` | 0 | 0 | -- |
| `gosec` | 2 HIGH (false positives) | **0** | -2 |
| `gitleaks` (working tree) | 2 matches | **0** | -2 |
| `gitleaks` (git history) | 2 matches (commit `6943cd8`) | 2 (unchanged, deferred) | -- |
| `trivy image` HIGH/CRITICAL | 0 | 0 | -- |

Plus the controls you can only measure by exercising:

| Smoke test | Before | After |
|---|---|---|
| SSRF path `//evil.example.com/x` | (not tested) | 502 with `"path must not contain scheme, host, or userinfo"` |
| `CONNECT` method | Accepted, forwarded to target | 400 at the handler, attempts: 0 |
| 2 MB request body | Accepted, buffered in memory | 413 `Payload Too Large` |
| `Authorization` header in webhook | Persisted to Redis in job JSON | Stripped at webhook boundary; not present in stored job |
| Completed job TTL | 24h | 3584s (~1h) |
| Completed job body | Retained for 24h | `nil` in Redis |
| Outbound `User-Agent` | Whatever client sent | `private-relayer/0.1` (overridden) |
| Container privileges | default | `ReadonlyRootfs`, `CapDrop=[ALL]`, `no-new-privileges` |
| Rate-limit bucket memory | Grows unboundedly | Swept every 2 min, idle-evicted after 10× window |

### 9.12 The Test Suite

Four new unit tests in `internal/relay/relay_test.go`:

- `TestRelayForwardsAllowedHeaders` -- asserts an `X-Custom-Header` is forwarded and an `Authorization` header is stripped on the same request.
- `TestRelayRewritesUserAgent` -- asserts the target receives `private-relayer/0.1`, not the client's `evil-client/9000`.
- `TestRelayRejectsInvalidMethod` -- asserts `CONNECT` returns 400 at the handler boundary.
- `TestRelayRejectsPathWithUserinfo` -- asserts a `//attacker.com/x` path does not produce a 200 (the actual failure mode is "build URL error", surfaced as 502 because it goes through the retry loop; the invariant is just "attacker never gets the 200").
- `TestRelayEnforcesHTTPSOnTargets` -- asserts `NewTargetRegistry` rejects `http://` when `allow_http` is false.
- `TestStripSensitiveHeaders` -- unit test for the scrubbing function used by the webhook boundary.

31 total tests passing (up from 27). Full run:
```
ok  github.com/ricardo1slas/private_relayer/internal/auth       0.6s
ok  github.com/ricardo1slas/private_relayer/internal/queue      0.4s
ok  github.com/ricardo1slas/private_relayer/internal/ratelimit  0.5s
ok  github.com/ricardo1slas/private_relayer/internal/relay      11.6s
```

### 9.13 Files Touched in Phase 5.5

```
.gitignore                              # new exclusions
configs/config.example.yaml             # new knobs, placeholder key
configs/config.yaml                     # no longer tracked; local dev copy only
how_to.md                               # this section; dev key scrubbed from examples
internal/auth/apikey.go                 # nosec G101
internal/config/config.go               # new config types (MaxRequestBytes, CompletedTTL, etc.)
internal/api/middleware.go              # log route pattern
internal/api/router.go                  # pass MaxRequestBytes through RouterDeps
internal/api/webhook.go                 # body cap, method allowlist, header scrub on enqueue
internal/ratelimit/limiter.go           # StartEviction sweeper
internal/relay/relay.go                 # full rewrite: URL safety, dial filter, header filter,
                                        # method allowlist, response cap, UA rewrite
internal/relay/retry.go                 # nosec G404
internal/relay/target.go                # validated Target struct, HTTPS enforcement
internal/relay/relay_test.go            # new + updated tests
internal/queue/queue.go                 # MarkCompleted, dual TTLs
internal/queue/queue_test.go            # updated NewQueue signature
internal/queue/worker.go                # use MarkCompleted on success
cmd/relayer/main.go                     # wire new config, start eviction goroutine
infra/docker/docker-compose.yml         # read_only, cap_drop, security_opt, user: redis
private/CLAUDE.md                       # reference to security_roadmap.md
private/security_roadmap.md             # living threat model + tool inventory (new file)
```

### 9.14 Why This Order Matters

The sequencing choice -- fix code first, then wire CI -- is itself a principle worth naming. Two alternatives were possible:

1. **Enable CI scanners first, fix findings reactively.** Fastest path to "CI is green" if you're willing to add `// nolint` or gosec exclude rules liberally. Produces a codebase where the scanners are rubber stamps.
2. **Fix code first, then enable CI.** Slower to first-green, but CI genuinely means "no regression from a clean baseline" -- every future finding is a real problem, not a pre-existing exclusion.

Option (2) is what DevSecOps roles actually value. A portfolio project that says "CI runs trivy" is weaker than one that says "CI runs trivy, baseline is 0, any future HIGH blocks merge." The difference is whether you hit ship-or-don't-ship decision points with real information.

---

## 10. Phase 6: CI/CD with GitHub Actions ✅

**Goal:** Turn the Phase 5.5 security baseline into an enforceable merge gate -- every PR and every push to `main` runs the same scanners we ran by hand, plus the Go test suite and a Docker build. Nightly cron re-scans the latest base image so CVEs landing upstream can't silently rot our image.

**Status:** COMPLETED -- 2 workflows (`ci.yml`, `security.yml`), Dependabot configured, gosec promoted into `golangci-lint`, all green. CodeQL was evaluated and removed for now (see §10.3); can be re-added when the repo goes public.

### 10.0 Why Split Into Two Workflows

One big workflow would have worked, but splitting matches the different audiences and schedules:

```
┌────────────────────── ci.yml ────────────────────────┐
│  Triggers: push + PR                                 │
│  Purpose: "does this code compile, lint, test,       │
│           and build an image?"                       │
│  Fails: lint errors, test failures, Docker issues    │
└──────────────────────────────────────────────────────┘

┌──────────────────── security.yml ────────────────────┐
│  Triggers: push + PR + nightly cron                  │
│  Purpose: "does this code regress any of the         │
│           Phase 5.5 baseline controls?"              │
│  Fails: CVEs in deps or image, committed secrets,    │
│         Dockerfile misconfig                         │
└──────────────────────────────────────────────────────┘
```

Two reasons to split instead of inline:

1. **Failure clarity.** "CI failed" is unhelpful; "security failed → gitleaks found a new secret" is actionable. A reviewer who sees separate checks on a PR knows which category to look at.
2. **Scheduling.** `ci.yml` runs per-push and cancels previous runs via concurrency groups. `security.yml` also runs nightly -- so even if no one pushes for a week, Monday's stand-up discovers "hey, `alpine:3.21.8` dropped a critical CVE."

A third workflow (CD -- build-and-push to ECR) lands in Phase 8 once AWS exists. CodeQL was prototyped and removed (§10.3).

### 10.1 `ci.yml` -- Correctness

Three jobs, run in parallel:

**`lint`** -- `golangci-lint` v2 with the configuration in `.golangci.yml`:
- `errcheck`, `govet`, `staticcheck`, `unused`, `ineffassign` (already in place pre-Phase-6)
- **`gosec`** added in Phase 6. The two known false positives (`G404` in `retry.go`, `G101` in `apikey.go`) are suppressed inline with `// #nosec` comments and justifications, so the linter stays strict; if *new* occurrences appear, the lint job fails.
- Uses `golangci/golangci-lint-action@v6` which pins the linter version (`v2.1.6` today) and caches the analysis DB across runs.

**`test`** -- `go test -race -count=1 -timeout=3m ./...` with a real Redis in a service container:
```yaml
services:
  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
    options: --health-cmd "redis-cli ping" --health-interval 5s --health-timeout 3s --health-retries 5
```
- `-race` catches data races (we rely heavily on goroutines -- worker pool, rate-limit sweeper, signal handler).
- `-count=1` defeats test caching; we always want the queue tests to actually hit Redis.
- `-timeout=3m` keeps a runaway test from burning an hour of CI minutes.
- The queue tests read `TEST_REDIS_ADDR` (set by the test helper) and fall back to `localhost:6379` -- which is exactly where the GitHub-Actions-provided service container maps. No extra config needed.

**`build`** -- `docker/build-push-action@v6` with Buildx + GitHub Actions cache:
```yaml
cache-from: type=gha
cache-to: type=gha,mode=max
```
- `load: true` loads the built image into the runner's local Docker so a follow-up step can smoke-test it.
- No push yet -- pushing to ECR is Phase 8's job once the infrastructure + OIDC trust are in place.

All three use `actions/setup-go@v5` with `go-version-file: go.mod` so Go's version stays pinned by the source, not a CI-specific constant that can drift.

### 10.2 `security.yml` -- Regression Gate

Four parallel scanner jobs. Each one mirrors what we ran locally in Phase 5.5, so `make <whatever>` and "the scan in CI" produce the same result.

**`govulncheck`** -- reachability-aware Go vulnerability scanner:
```yaml
- run: go install golang.org/x/vuln/cmd/govulncheck@latest
- run: govulncheck ./...
```
Unlike `gosec` (pattern match) or `trivy` (package version match), `govulncheck` builds the call graph and only flags vulns your code actually reaches. Fewer false positives, zero tolerance is reasonable.

**`gitleaks`** -- secrets scanner, working-tree only:
```yaml
- run: gitleaks detect --source . --no-git --redact --exit-code 1
```
The `--no-git` flag is the key choice. By default gitleaks scans git history, which would surface the pre-Phase-5.5 dev key in commit `6943cd8` (tracked as T12b in `private/security_roadmap.md`, slated for `git filter-repo` cleanup in Phase 10). `--no-git` limits the scan to what's currently committed in the working tree -- so CI gates *future* secrets without forcing a history-rewrite decision today. If someone adds a new credential in a PR, CI catches it. If someone tries to revive the old one, CI catches it. Both gates, without the cost.

**`trivy-image`** -- CVE scan of the built container:
```yaml
- uses: aquasecurity/trivy-action@0.28.0
  with:
    image-ref: private-relayer:scan
    severity: HIGH,CRITICAL
    exit-code: "1"
    ignore-unfixed: true
```
- `ignore-unfixed: true` means we don't block on CVEs that have no patch available -- not actionable and adds noise.
- `severity: HIGH,CRITICAL` matches our Phase 5.5 threshold.
- Cache hit on the Buildx cache means this job doesn't pay the build cost twice in a run.

**`trivy-config`** -- Dockerfile + compose misconfiguration scan:
```yaml
- uses: aquasecurity/trivy-action@0.28.0
  with:
    scan-type: config
    scan-ref: infra/docker
    severity: HIGH,CRITICAL
    exit-code: "1"
```
Catches things like "no HEALTHCHECK", "running as root", "using `:latest` tag". We pass today because Phase 5 + 5.5 already hardened these, but the gate prevents a future `docker-compose.yml` change from silently undoing `read_only: true` or `cap_drop: [ALL]`.

**Nightly cron at 06:17 UTC** re-runs everything even if nothing is pushed. That's the answer to T14 (dependency/image drift) in the security roadmap -- CVEs landing on `alpine:3.21` a week after we last pushed will surface on the next morning's run instead of whenever someone next opens a PR.

### 10.3 CodeQL -- Prototyped, Removed, Deferred

An earlier draft included a third workflow, `codeql.yml`, running GitHub's CodeQL with the `security-extended` + `security-and-quality` Go query suites. We removed it during Phase 6 shakedown for one reason: **SARIF upload requires repo-level "Code scanning" to be enabled**, and on a private repo that toggle is gated by GitHub Advanced Security (GHAS). GHAS is a paid add-on; on the current plan the "Set up code scanning" option isn't even visible in Settings.

The scan itself ran fine on the runner -- CodeQL produced a SARIF file. What failed was the upload:
```
Code scanning is not enabled for this repository. Please enable code scanning in the repository settings.
```

The decision was pragmatic:

- **Remove the workflow now.** Every push would otherwise show a red `codeql / analyze` check even though the code is fine -- confusing, and it devalues the other (real) failures when they happen.
- **Keep the supporting things.** Nothing else referenced CodeQL; Dependabot's `codeql-action` bump PR becomes a no-op and can be closed.
- **Re-add when the repo goes public.** CodeQL is free on public repos. The public-showcase repo planned for Phase 10 gets CodeQL for free. At that point a re-introduced `codeql.yml` lives there, not here.

What we keep without CodeQL:
- **gosec** via `golangci-lint` -- Go-specific pattern matching (the bulk of what CodeQL would flag).
- **govulncheck** -- reachability-aware CVE scan, which CodeQL does *not* do (they're complementary).
- **trivy** -- package and container CVEs.
- **Manual threat modelling** (`private/security_roadmap.md`) -- the things automated tools miss anyway.

Net cost of removing CodeQL: lower taint-tracking coverage for now. Net benefit: no red checks on every PR from a feature we can't use on this plan. Revisit at Phase 10.

### 10.4 `dependabot.yml` -- Keep Them Current

Three ecosystems:

| Ecosystem | What it watches | Cadence |
|---|---|---|
| `gomod` | `go.mod` / `go.sum` | Weekly, Mondays |
| `docker` | `FROM` lines in `infra/docker/Dockerfile` | Weekly, Mondays |
| `github-actions` | `uses: @v5` pins in `.github/workflows/*.yml` | Weekly, Mondays |

Grouped config for Go:
```yaml
groups:
  go-minor-and-patch:
    update-types: [minor, patch]
```
means patch-level updates arrive as a single weekly PR, not 15 separate PRs. Major bumps still come individually because they may be breaking. Security advisories (any severity) bypass the schedule and open immediately.

The key insight: Dependabot *creates* PRs; CI *validates* them. If Dependabot proposes bumping `chi` from 5.2.5 to 5.2.6 and the new version breaks a test, the PR sits red until a human looks. No silent drift.

### 10.5 Concurrency + Permissions Hygiene

Each workflow declares:
```yaml
permissions:
  contents: read
concurrency:
  group: <name>-${{ github.ref }}
  cancel-in-progress: true
```

- **`contents: read`** is the minimum every workflow uses. Every other token surface is denied. This is "principle of least privilege" at the workflow level.
- **Concurrency groups** cancel superseded runs. If you push three commits in a row, only the last one's CI survives -- saves runner minutes and surfaces the real result faster.

### 10.6 Branch Protection -- Reality Check on the Free Tier

CI being green on a PR only matters if merges require CI to be green. That's a branch-protection rule. On the GitHub free tier with a **private** repo, *neither* classic branch protection *nor* the newer rulesets API are available -- both return `403 Upgrade to GitHub Pro or make this repository public to enable this feature`.

So we have three choices:

| Option | Cost | Effect |
|---|---|---|
| GitHub Pro | $4/mo | Unlocks classic protection + rulesets; required checks enforced by GitHub. |
| Make the repo public | free | Same unlock + free CodeQL; but Phase 10 owns the public showcase fork, not this repo. |
| **Self-discipline + free-tier settings** | free | CI still runs, red checks visible on PRs; merging is not machine-blocked. |

We chose option 3 for now. The signal exists; enforcement is human. For a solo portfolio project, this is honest and cheap. When the public showcase repo is forked in Phase 10, protection turns on there for free.

**Free-tier settings that simulate ~80% of branch protection:**

1. **Default to PRs, never push directly to main.** Even solo. It's a habit, and the CI runs on PRs just like on push.
2. **Settings → General → Pull Requests**: disable "Allow merge commits" and "Allow rebase merging"; keep only "Allow squash merging". Linear history for free.
3. **Settings → General → Pull Requests**: enable "Automatically delete head branches." Repo stays tidy.
4. **Settings → Actions → General**: set "Workflow permissions" to **Read repository contents and packages permissions** (default is too wide). Already covered by our per-workflow `permissions:` blocks, but this sets the floor.

**The 7 checks that *would* be required** (for reference if we upgrade later):
- `lint (golangci-lint)`
- `test (with redis)`
- `build (docker image)`
- `govulncheck (reachable Go CVEs)`
- `gitleaks (secrets in working tree)`
- `trivy (image CVEs)`
- `trivy (Dockerfile + compose misconfigs)`

CodeQL is not listed -- that workflow was removed in Phase 6 shakedown (see §10.3).

### 10.7 Local-Parity Commands

The scanners that gate CI all have a local equivalent. We rely on this when developing:

```bash
# Same tool, same flags, same version family as CI:
make lint                                          # golangci-lint, includes gosec
go test -race -count=1 -timeout=3m ./...           # same as ci.yml test job
~/go/bin/govulncheck ./...                         # same as security.yml
gitleaks detect --source . --no-git --redact       # same as security.yml
trivy image --severity HIGH,CRITICAL private-relayer:local
trivy config --severity HIGH,CRITICAL infra/docker
```

Running these locally before pushing means CI failures are almost always flakes (network) or version drift -- not things a pre-push check would have caught.

### 10.8 What Phase 6 Does *Not* Do

- **No deploy.** Building + scanning is the whole job. ECR push + ECS rollout are Phase 8.
- **No secrets handling in CI yet.** None of these jobs need AWS credentials or API tokens. Phase 8 will add `permissions: id-token: write` + OIDC to federate a short-lived AWS role.
- **No release automation.** Tags, changelogs, GitHub Releases -- Phase 10.
- **No SBOM publication.** Generating `cyclonedx-gomod` / `syft` outputs per release -- Phase 10.

Deferrals tracked by name so Phase 8 + 10 aren't vague "CD" and "polish" phases -- they close specific items from this list.

### 10.9 What Closed in the Security Roadmap

Phase 6 closes **T14 (dependency + container drift)** in `private/security_roadmap.md`. The controls:
- `govulncheck` on every PR and nightly -- new CVEs in reachable Go deps fail the build.
- `trivy` on every PR and nightly -- new CVEs in the alpine base image or Go binary fail the build.
- Dependabot -- deps + base image + GHA versions auto-updated weekly; security advisories bypass the schedule.

Plus it reinforces the Phase 5.5 closures by turning them into regression gates. A future commit that adds an SSRF, reintroduces the committed dev key, or removes `#nosec` justifications (making gosec flag the false positives again) now fails CI instead of sneaking through.

### 10.10 Local Verification Before Commit

Before considering Phase 6 shipped:

```bash
# Validate YAML
actionlint .github/workflows/*.yml                 # clean
python3 -c "import yaml; yaml.safe_load(open('.github/dependabot.yml'))"

# Run what CI runs
make lint                                          # 0 issues
go test -race -count=1 -timeout=3m ./...           # all packages ok
~/go/bin/govulncheck ./...                         # no vulnerabilities
gitleaks detect --source . --no-git --redact --exit-code 1   # no leaks
trivy image --severity HIGH,CRITICAL private-relayer:local   # 0 vulns
trivy config --severity HIGH,CRITICAL infra/docker           # 0 misconfigs
```

All commands pass. First PR after the initial push is where the remote CI runs for real; plan on a 30-second follow-up commit to fix whatever version drift the hosted runners catch that local didn't.

---

## 11. Phase 7: AWS Infrastructure with Terraform 🚧

**Goal:** Define all cloud infrastructure as code.

**Status:** SCAFFOLDED — modules + dev/prod compositions checked in. Not yet `apply`d against a real account; `terraform.tfvars` and remote-state backend config are user-supplied.

### Layout

```
infra/terraform/
├── modules/
│   ├── vpc/      # multi-AZ VPC, public + private subnets, IGW, NAT
│   ├── alb/      # internet-facing ALB, target group, HTTP/HTTPS listeners
│   ├── ecs/      # Fargate cluster, task def, service, IAM, log group
│   └── redis/    # ElastiCache Redis replication group, SG locked to ECS SG
└── environments/
    ├── dev/      # 1 task, single NAT, single-node Redis, HTTP-only by default
    └── prod/     # 2 tasks across AZs, NAT/AZ, Multi-AZ Redis, HTTPS required
```

### Design Decisions (Realized)

- **Fargate over EC2**: No servers to manage. Pay per task. ARM64 by default for cost.
- **2 AZs minimum**: ALB requires it; subnets are computed via `cidrsubnet` so you only pass an AZ list.
- **Private subnets for ECS/Redis**: Tasks have no public IPs. Egress via NAT (one shared in dev, one per AZ in prod). Inbound only through ALB SG.
- **SG-to-SG references everywhere**: ALB SG → task SG on `:8080`; task SG → Redis SG on `:6379`. No CIDR-based intra-VPC rules.
- **Terraform modules**: Each env composes the same four modules with different sizing/HA toggles.
- **Remote state**: `backend "s3" {}` declared but unconfigured — supplied via `terraform init -backend-config=...` so the same code works against any account.
- **AWS provider pinned to `~> 6.42`** with `.terraform.lock.hcl` committed. Reproducible plans across machines/CI; Dependabot is the only thing that bumps the major.
- **`ignore_changes` on the ECS service** for `task_definition` and `desired_count`: Phase 8's deploy pipeline owns those; Terraform owns the cluster shape.
- **Secrets via SSM/Secrets Manager**: Plain env (e.g. `RELAYER_QUEUE_REDIS_ADDR`) goes through `environment`. API keys go through `secrets`, which the execution role is granted `GetParameters`/`GetSecretValue` on.
- **`readonlyRootFilesystem = true`** in the task def, `drop_invalid_header_fields = true` on the ALB, at-rest encryption always on for Redis. All defenses from Phase 5.5 carry forward.
- **Container Insights is opt-in (`container_insights_enabled`, default `false`)**. It is *not* in the AWS free tier (~$0.30/metric/month). CloudWatch *Logs* are always wired via the awslogs driver and stay inside the 5 GB/month free tier for this workload. Real metrics + dashboards are deferred to Phase 9.
- **Redis TLS, end-to-end**: prod sets `transit_encryption_enabled = true` and the env composition passes `RELAYER_QUEUE_REDIS_TLS=true` to the task. The relayer's Redis client (`internal/queue/redis.go`) honors that flag with a TLS 1.2+ config. Dev keeps TLS off.

### Bootstrap

Remote state lives in S3 + DynamoDB. Create the bucket and lock table once per AWS account, then `terraform init` each env with `-backend-config=...`. See `infra/terraform/README.md` for the exact commands.

### Local Verification

```sh
make tf-fmt        # formats every .tf under infra/terraform
make tf-validate   # validates dev + prod compositions
make tf-plan-dev   # requires terraform.tfvars + initialized backend
```

### What Phase 7 Does *Not* Do

- **Build or push images.** `image` is a string the env passes in. Phase 8 wires ECR + GitHub Actions OIDC and supplies a real digest.
- **Provision DNS / ACM.** `certificate_arn` is required for prod and assumed to already exist. Route 53 records are out of scope for this phase.
- **Apply against an account.** This phase ships *code*. The first real `apply` happens after Phase 8 has an image to point at.

---

## 12. Phase 8: Continuous Deployment 🚧

**Goal:** Automate deployments from GitHub to ECS.

**Status:** DEV PIPELINE SHIPPED. Prod is intentionally deferred — see §12.6.

### 12.1 What Phase 8 Adds

```
infra/terraform/modules/
├── ecr/          # private repo + lifecycle (keep last 10 tagged, expire untagged 7d)
└── github_oidc/  # OIDC provider + IAM role scoped to ECR push + ECS deploy

.github/workflows/deploy.yml   # build -> push -> render task def -> deploy
```

The dev env composition wires both modules and exposes two outputs you'll feed to GitHub:

```sh
terraform -chdir=infra/terraform/environments/dev output ecr_repository_url
terraform -chdir=infra/terraform/environments/dev output github_actions_role_arn
```

### 12.2 The Pipeline, End-to-End

```
push to main
  │
  ▼
[checkout] -> [configure-aws-credentials via OIDC, no secrets]
  │
  ▼
[ECR login] -> [docker build + push]
  │              tags: :sha-<commit>, :latest
  ▼
[describe-task-definition]   pulls current task def JSON
  │
  ▼
[render-task-definition]     swaps the container image to :sha-<commit>
  │
  ▼
[deploy-task-definition]     registers new revision + UpdateService
  │                          waits for service stability
  ▼
[done]                       new tasks healthy on the ALB target group
```

Two things let this work without Terraform fighting back:

- The ECS module's `lifecycle.ignore_changes = [task_definition, desired_count]` means the deploy can register a new revision without `terraform plan` showing drift later.
- The deploy IAM role can `RegisterTaskDefinition` and `UpdateService` on *only* the dev cluster + service, plus push to *only* the dev ECR repo, plus `PassRole` only to the task + execution roles created by the ECS module. Anything else is denied.

### 12.3 OIDC, Concretely

GitHub Actions issues a signed JWT identifying the workflow run (repo, ref, event, etc.). AWS STS validates that JWT against the OIDC provider, then the role's trust policy decides whether to issue temporary credentials.

The trust policy in `modules/github_oidc` requires:

- `aud == sts.amazonaws.com` (a literal AWS audience claim — anti-confused-deputy)
- `sub` matches `repo:ricardo1slas/private_relayer:ref:refs/heads/main`

That last line is the load-bearing one. A fork, a feature branch, or a pull request from elsewhere cannot assume the role even if they somehow run the workflow file.

The OIDC thumbprint hard-coded in the module is the historical Actions OIDC cert thumbprint. AWS no longer validates against it (it validates the cert chain itself), but the field is still required by the API.

### 12.4 ECR Lifecycle ≠ Access Control

Two separate things, frequently conflated:

| Mechanism | Controls | Default |
|---|---|---|
| **Lifecycle policy** | How long old images live before deletion (cost/storage) | Keep last 10 tagged + expire untagged after 7 days |
| **IAM** | *Who* can pull/push (security) | Only the ECS task execution role (pull) and the GitHub deploy role (push) |

The lifecycle policy doesn't make the repo "more private" — it just deletes obsolete images so storage stays free-tier-friendly. Privacy is purely IAM.

### 12.5 Bootstrap (One-Time, Per Account)

Before the first push to `main` can deploy:

1. `terraform apply` the dev env (creates ECR repo, OIDC provider, IAM role, ECS service).
2. Push a placeholder image so the service can start at least once:
   ```sh
   ECR_URL=$(terraform -chdir=infra/terraform/environments/dev output -raw ecr_repository_url)
   aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin "$ECR_URL"
   docker build -t "$ECR_URL:bootstrap" -f infra/docker/Dockerfile .
   docker push "$ECR_URL:bootstrap"
   ```
   Then `terraform apply` again with `image = "<url>:bootstrap"` so the task def points somewhere real. After this, the service exists and the GitHub workflow can update it.
3. In GitHub: **Settings → Secrets and variables → Actions → Variables** → add repository variable `AWS_DEPLOY_ROLE_ARN` = the `github_actions_role_arn` output. (It's a *variable*, not a secret — it's not sensitive on its own; what makes it useless to attackers is the OIDC trust policy.)

After step 3, every push to `main` triggers `deploy-dev`.

### 12.6 What Phase 8 Does *Not* Do (Yet)

- **No prod pipeline.** The prod env composition exists from Phase 7 but has no deploy workflow, no ECR repo, no OIDC role. When prod is added, it'll get its own GitHub Environment with a required-reviewer protection rule (free on private repos for select branches/environments).
- **No tag-based releases.** Every push to `main` deploys to dev. There is no `:v1.2.3` workflow. Adding one is a small follow-up.
- **No automatic rollback past ECS-native rolling.** ECS will refuse to replace healthy tasks with unhealthy new ones, and the workflow waits for service stability — so a bad deploy fails the workflow and leaves the previous revision live. Smarter rollback (e.g. "if 5xx rate spikes, redeploy previous SHA") is Phase 9 / 10 territory.
- **No Apply against an account.** The Terraform code is correct; the actual `apply` (and the resulting AWS bill) is your call.

---

## 13. Phase 9: Observability & Alerting ⬜

**Goal:** Know when things break before users tell you.

**Status:** NOT STARTED

### The Three Pillars of Observability

```
┌─────────────────────────────────────────────────────┐
│                 Observability                       │
│                                                     │
│  ┌─────────┐    ┌──────────┐    ┌───────────────┐  │
│  │  Logs   │    │ Metrics  │    │   Traces      │  │
│  │         │    │          │    │  (future)     │  │
│  │ zerolog │    │Prometheus│    │ OpenTelemetry │  │
│  │ -> CW   │    │-> Grafana│    │               │  │
│  └─────────┘    └──────────┘    └───────────────┘  │
└─────────────────────────────────────────────────────┘
```

- **Logs**: "What happened?" -- individual events with context
- **Metrics**: "How much?" -- aggregated counters, histograms, gauges
- **Traces**: "What path did this request take?" -- future enhancement

---

## 14. Phase 10: Hardening & Documentation ⬜

**Goal:** Production-ready polish.

**Status:** NOT STARTED

---

## 15. Glossary

| Term | Definition |
|---|---|
| **Relay** | Forwarding a request from one service to another, potentially adding auth, retries, or transformation |
| **Middleware** | A function that wraps an HTTP handler to add behavior (logging, auth, rate limiting) before/after the handler runs |
| **chi** | A lightweight Go HTTP router that's compatible with stdlib `net/http` |
| **koanf** | A Go configuration library that loads from multiple sources (YAML, env, flags) and merges them |
| **zerolog** | A zero-allocation structured JSON logger for Go |
| **Exponential backoff** | Retry strategy where wait time doubles each attempt (1s, 2s, 4s, 8s...) to avoid overwhelming a recovering service |
| **Jitter** | Random variation added to backoff delays to prevent multiple retriers from hitting the target at the same time |
| **Token bucket** | Rate limiting algorithm: a bucket fills with tokens at a fixed rate; each request consumes one token; empty bucket = rejected |
| **Dead-letter queue** | A queue where messages go after exhausting all retries, for manual inspection |
| **Liveness probe** | Health check that answers "is the process running?" -- if it fails, restart the container |
| **Readiness probe** | Health check that answers "can it serve traffic?" -- if it fails, stop routing to it |
| **12-factor app** | A methodology for building cloud-native apps. Factor III: "Store config in the environment" |
| **ECS Fargate** | AWS service that runs Docker containers without managing servers |
| **ALB** | Application Load Balancer -- distributes HTTP/HTTPS traffic across targets |
| **ElastiCache** | AWS managed Redis/Memcached service |
| **Terraform module** | A reusable, composable unit of infrastructure code |
| **OIDC federation** | A way for GitHub Actions to assume an AWS IAM role without storing credentials |
| **SSRF** | Server-Side Request Forgery -- when an attacker tricks a server into making requests to internal resources |
| **Slowloris** | An attack where a client holds connections open by sending data very slowly, exhausting server resources |

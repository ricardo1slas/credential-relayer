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
8. [Phase 5: Docker & Docker Compose](#8-phase-5-docker--docker-compose) ⬜
9. [Phase 6: CI/CD with GitHub Actions](#9-phase-6-cicd-with-github-actions) ⬜
10. [Phase 7: AWS Infrastructure with Terraform](#10-phase-7-aws-infrastructure-with-terraform) ⬜
11. [Phase 8: Continuous Deployment](#11-phase-8-continuous-deployment) ⬜
12. [Phase 9: Observability & Alerting](#12-phase-9-observability--alerting) ⬜
13. [Phase 10: Hardening & Documentation](#13-phase-10-hardening--documentation) ⬜
14. [Glossary](#14-glossary)

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
      key: "<revoked-dev-key>"

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

$ curl -H "X-API-Key: <revoked-dev-key>" -X POST localhost:8080/api/v1/relay \
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
  -H "X-API-Key: <revoked-dev-key>" \
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

## 8. Phase 5: Docker & Docker Compose ⬜

**Goal:** Containerize for consistent local and cloud environments.

**Status:** NOT STARTED

### Key Design Decisions (Upcoming)

- **Multi-stage build**: Stage 1 (builder) has Go toolchain (~1GB). Stage 2 (runtime) has only the binary (~15MB). This reduces attack surface and speeds up pulls.
- **distroless vs alpine**: distroless has no shell (more secure, harder to debug). alpine has busybox (less secure, easier to debug). We'll decide based on debugging needs.
- **Non-root user**: The container runs as UID 65534 (nobody). If the process is compromised, the attacker has no privileges.

---

## 9. Phase 6: CI/CD with GitHub Actions ⬜

**Goal:** Automate testing, building, and publishing.

**Status:** NOT STARTED

### Key Design Decisions (Upcoming)

- **Trivy for image scanning**: Checks for CVEs in the base image and Go dependencies.
- **gosec for code scanning**: Static analysis for Go security issues (SQL injection, command injection, etc.)
- **ECR over Docker Hub**: AWS-native, private by default, integrates with ECS without extra credentials.

---

## 10. Phase 7: AWS Infrastructure with Terraform ⬜

**Goal:** Define all cloud infrastructure as code.

**Status:** NOT STARTED

### Key Design Decisions (Upcoming)

- **Fargate over EC2**: No servers to manage. Pay per task. Perfect for a service that scales horizontally.
- **2 AZs minimum**: AWS requires multi-AZ for high availability. ALB distributes traffic across AZs.
- **Private subnets for ECS/Redis**: No direct internet access. Outbound traffic goes through NAT gateway. Inbound traffic comes only through the ALB.
- **Terraform modules**: Reusable components (VPC, ECS, ALB) shared between dev and prod. Different `terraform.tfvars` per environment.

---

## 11. Phase 8: Continuous Deployment ⬜

**Goal:** Automate deployments from GitHub to ECS.

**Status:** NOT STARTED

### Key Design Decisions (Upcoming)

- **OIDC over static credentials**: GitHub Actions assumes an IAM role via OIDC federation. No long-lived AWS access keys stored in GitHub secrets.
- **Rolling deployment**: ECS replaces tasks one at a time. If the new task fails health checks, the rollout stops. No downtime.
- **Manual approval for prod**: Dev deploys automatically on merge to main. Prod requires clicking "Approve" in GitHub.

---

## 12. Phase 9: Observability & Alerting ⬜

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

## 13. Phase 10: Hardening & Documentation ⬜

**Goal:** Production-ready polish.

**Status:** NOT STARTED

---

## 14. Glossary

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

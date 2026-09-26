# Private Relayer

[![CI](https://github.com/ricardo1slas/private_relayer/actions/workflows/ci.yml/badge.svg)](https://github.com/ricardo1slas/private_relayer/actions/workflows/ci.yml)
[![Security](https://github.com/ricardo1slas/private_relayer/actions/workflows/security.yml/badge.svg)](https://github.com/ricardo1slas/private_relayer/actions/workflows/security.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/ricardo1slas/private_relayer)](https://goreportcard.com/report/github.com/ricardo1slas/private_relayer)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)

A production-grade, general-purpose request relayer service written in **Go**. Inspired by [OpenZeppelin Relayer](https://github.com/openzeppelin/openzeppelin-relayer), but designed for broader use: relay HTTP requests, webhooks, and messages between services with built-in authentication, rate limiting, queue management, and observability.

Deployed on **AWS** using **Terraform**, **Docker/ECS**, **GitHub Actions CI/CD**, and **CloudWatch** for monitoring.

## Architecture

```
                ┌──────────────┐
  Clients ────> │   API Gateway │
                │  (Auth + Rate │
                │   Limiting)   │
                └──────┬───────┘
                       │
                ┌──────▼───────┐      ┌─────────────┐
                │  Relayer Core │ ───> │  Redis Queue │
                │  (Go Service) │      └──────┬──────┘
                └──────┬───────┘             │
                       │              ┌──────▼──────┐
                ┌──────▼───────┐      │   Worker     │
                │  Target       │      │  (Async Jobs)│
                │  Endpoints    │      └─────────────┘
                └──────────────┘
```

## Features

- **Request Relaying** -- Forward HTTP requests to configured target endpoints with retries and timeout handling
- **Webhook Relay** -- Receive, validate, queue, and deliver webhooks with signature verification
- **Authentication** -- API key and HMAC-based request signing
- **Rate Limiting** -- Configurable per-client and per-endpoint rate limits
- **Async Queue** -- Redis-backed job queue for reliable delivery with dead-letter support
- **Multi-Target Routing** -- Route requests to different backends based on configurable rules
- **Observability** -- Structured logging, Prometheus metrics, CloudWatch integration
- **Health Checks** -- Liveness and readiness probes for container orchestration

## Tech Stack

| Layer              | Technology                          |
|--------------------|-------------------------------------|
| Language           | Go                                  |
| Queue              | Redis                               |
| Containerization   | Docker                              |
| Orchestration      | AWS ECS (Fargate)                   |
| Infrastructure     | Terraform                           |
| CI/CD              | GitHub Actions                      |
| Monitoring         | CloudWatch, Prometheus, Grafana     |
| Secrets            | AWS Secrets Manager                 |
| Load Balancing     | AWS ALB                             |
| Networking         | AWS VPC, Security Groups            |

## Project Structure

```
private_relayer/
├── cmd/
│   └── relayer/              # Application entrypoint
├── internal/
│   ├── api/                  # HTTP handlers and middleware
│   ├── config/               # Configuration loading
│   ├── queue/                # Redis queue implementation
│   ├── relay/                # Core relay logic
│   ├── auth/                 # Authentication middleware
│   ├── ratelimit/            # Rate limiting
│   └── metrics/              # Prometheus metrics
├── pkg/
│   └── client/               # Reusable client library
├── infra/
│   ├── terraform/
│   │   ├── modules/          # VPC, ECS, ALB, Redis, IAM, CloudWatch
│   │   ├── environments/
│   │   │   ├── dev/
│   │   │   └── prod/
│   │   └── backend.tf
│   └── docker/
│       ├── Dockerfile
│       └── docker-compose.yml
├── .github/
│   └── workflows/
│       ├── ci.yml            # Lint, test, build
│       └── cd.yml            # Deploy to ECS
├── configs/
│   └── config.yaml           # Relayer configuration
├── scripts/                  # Helper scripts
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## Getting Started

### Prerequisites

- Go 1.22+
- Docker & Docker Compose
- Redis
- AWS CLI (for deployment)
- Terraform 1.5+ (for infrastructure)

### Local Development

```bash
# Clone the repo
git clone https://github.com/ricardo1slas/private_relayer.git
cd private_relayer

# Copy and edit configuration
cp configs/config.example.yaml configs/config.yaml

# Start dependencies
docker compose up -d redis

# Run the service
make run

# Run tests
make test
```

The API will be available at `http://localhost:8080`.

### Docker

```bash
make docker-up   # relayer + redis + prometheus + grafana
```

This brings up the full local stack:

| Service | URL | Notes |
|---|---|---|
| Relayer API | http://localhost:8080 | `/health`, `/ready`, `/api/v1/*` |
| Relayer metrics | http://localhost:9090/metrics | Prometheus exposition |
| Prometheus | http://localhost:9091 | scrapes the relayer |
| Grafana | http://localhost:3000 | admin/admin; "Private Relayer" dashboard auto-provisioned |

### Self-Hosting (Raspberry Pi / any Linux box)

You don't need AWS to run this. The `docker-compose.yml` above is the full
self-hosted stack — including Prometheus + Grafana, which are the same
observability the AWS CloudWatch setup provides. Every image is multi-arch, so
`make docker-up` works unchanged on an ARM Pi.

Requirements: Raspberry Pi 4 or 5, 64-bit Raspberry Pi OS, 2 GB+ RAM, Docker.
(The four services cap at ~1.5 GB combined via `mem_limit`. A Pi Zero / 32-bit
OS won't work.)

```bash
make docker-up      # same command as on any machine
```

**Pi over Tailscale (what this repo's CI does):** `make deploy-pi` rsyncs the repo
and runs compose on the Pi, binding all ports to its tailnet IP only. Every push
to `main` deploys automatically. Setup and ACL: [`docs/pi-deploy.md`](docs/pi-deploy.md).

To expose it to the internet without opening a port on your router, use a free
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
(install `cloudflared` first):

```bash
make tunnel         # prints a public HTTPS URL for the relayer on :8080
```

Only expose the relayer (`:8080`). Never expose Redis (`:6379`).

> The `infra/terraform/` and CloudWatch modules stay in the repo as a reference
> AWS deployment — they demonstrate the cloud path without you having to `apply`
> (and pay for) it.

### Performance

Measured on a Raspberry Pi 4 (4 cores) with a local upstream, load from a Mac over Tailscale
(`make bench`, details in [`benchmarks/`](benchmarks/README.md)):

| Scenario | req/s | p50 | p95 |
|---|---|---|---|
| relay, 50 concurrent | 976 | 46 ms | 98 ms |
| relay, 200 concurrent | 1048 | 168 ms | 409 ms |

A CPU profile (pprof) showed ~80% of time dialing new TCP connections: the per-request
timeout context was cancelled before the response body was read, so net/http discarded every
connection. Cancelling on `Body.Close()` instead took 200-concurrency throughput from 282 to
1048 req/s (3.7x); a regression test pins the behaviour.

### Alerting & security

Prometheus rules (5xx rate, latency, upstream failures, queue backlog, dead-lettered jobs, auth-failure
and rate-limit spikes) route through Alertmanager to email. Hardening notes, the Tailscale deny-by-default
policy, and the app-security roadmap live in [`docs/hardening.md`](docs/hardening.md).

### Deploy to AWS

```bash
cd infra/terraform/environments/dev
terraform init
terraform plan
terraform apply
```

## Configuration

Configuration is loaded from `configs/config.yaml` and environment variables.

```yaml
server:
  port: 8080
  read_timeout: 10s
  write_timeout: 10s

auth:
  api_keys:
    - name: "default"
      key: "${API_KEY}"

relay:
  targets:
    - name: "service-a"
      url: "https://api.example.com"
      timeout: 5s
      retries: 3
      rate_limit:
        requests: 100
        window: 60s

queue:
  redis:
    addr: "localhost:6379"
    db: 0
  workers: 5
  retry_delay: 30s
  max_retries: 3
  dead_letter: true

metrics:
  enabled: true
  port: 9090
```

## API

| Method | Endpoint              | Description                    |
|--------|-----------------------|--------------------------------|
| POST   | `/api/v1/relay`       | Relay a request to a target    |
| POST   | `/api/v1/webhook`     | Receive and queue a webhook    |
| GET    | `/api/v1/jobs/:id`    | Check job status               |
| GET    | `/health`             | Liveness probe                 |
| GET    | `/ready`              | Readiness probe                |
| GET    | `/metrics`            | Prometheus metrics             |

## Client SDK

A dependency-free Go client lives in [`pkg/client`](pkg/client):

```go
import "github.com/ricardo1slas/private_relayer/pkg/client"

c := client.New("https://relayer.example.com", os.Getenv("RELAYER_API_KEY"))

// Synchronous relay
resp, err := c.Relay(ctx, client.RelayRequest{
    Target: "httpbin", Method: "GET", Path: "/get",
})

// Async (queued) relay + status poll
jobID, err := c.Webhook(ctx, client.WebhookRequest{Target: "httpbin", Method: "POST", Path: "/post"})
job, err := c.JobStatus(ctx, jobID)
```

Non-2xx responses come back as a typed `*client.APIError` carrying the status
code and server message.

## Monitoring

- **Prometheus** metrics on a dedicated port (`:9090/metrics`, kept off the public ALB):
  request counts, latency histograms, relay outcomes per target, queue depth, jobs processed.
- **Grafana** dashboard (auto-provisioned in the local stack) for request rate, error rate,
  latency percentiles, and queue depth.
- **CloudWatch** dashboard + alarms (5xx rate, p95 latency, unhealthy hosts, ECS CPU/memory,
  error-log rate) wired to an **SNS** topic for email alerts — see `infra/terraform/modules/cloudwatch`.
- **Structured logging** in JSON (zerolog) for CloudWatch Logs ingestion and metric filters.

See [`docs/runbook.md`](docs/runbook.md) for deploy, rollback, alarm response, and troubleshooting.

## License

MIT

## Author

**Ricardo Islas Guerra** -- Cloud & DevOps Engineer
- Portfolio: [rickypedia](https://ricardo1slas.github.io/rickypedia/)
- GitHub: [@ricardo1slas](https://github.com/ricardo1slas)

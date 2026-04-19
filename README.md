# Private Relayer

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
docker compose up -d
```

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

## Monitoring

- **Prometheus** metrics exposed at `/metrics` (request count, latency histograms, queue depth, error rates)
- **CloudWatch** dashboards and alarms for ECS task health, CPU/memory, and custom application metrics
- **Structured logging** in JSON format for CloudWatch Logs ingestion

## License

MIT

## Author

**Ricardo Islas Guerra** -- Cloud & DevOps Engineer
- Portfolio: [rickypedia](https://ricardo1slas.github.io/rickypedia/)
- GitHub: [@ricardo1slas](https://github.com/ricardo1slas)

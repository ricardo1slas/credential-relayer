# Credential Relayer -- Operations Runbook

Operational reference for deploying, observing, and troubleshooting the relayer.
Audience: whoever is on call.

> **Scope.** Sections 1-5 describe the **AWS reference deployment** (`infra/terraform`). It is not running
> today: the live deployment is the Raspberry Pi ([`pi-deploy.md`](pi-deploy.md)), and `deploy-aws.yml` is a
> manual, dev-only workflow. Sections 6-7 apply to both.

---

## 1. Service at a glance

| | |
|---|---|
| Runtime | Go binary on AWS ECS Fargate, behind an ALB |
| State | ElastiCache Redis (job queue + rate-limit state) |
| Public port | `8080` (ALB → target group → task) |
| Metrics port | `9090` (`/metrics`, **not** exposed on the ALB) |
| Logs | CloudWatch Logs, group `/ecs/<name>` (JSON, zerolog) |
| Images | Amazon ECR, tagged with git SHA + `latest` |
| IaC | Terraform in `infra/terraform` (`dev`, `prod` envs) |

Health endpoints:
- `GET /health` — liveness, always `200` if the process is up.
- `GET /ready` — readiness, `503` if Redis is unreachable. The ALB health check
  uses `/health`; do not point it at `/ready` or a Redis blip will drain all tasks.

---

## 2. Deploy

### Workflow (manual, dev only)
1. Merge to `main`. CI (`.github/workflows/ci.yml`) runs lint + tests + build.
   On `main`, `deploy.yml` then deploys to the **Pi**, not AWS.
2. Run `deploy-aws.yml` by hand (`workflow_dispatch`): it builds the image, pushes
   to ECR, and updates the **dev** ECS service (rolling update, min healthy 50% /
   max 200%). Prod has no deploy workflow (build log §12.6); prod changes go
   through Terraform below.
3. The rollout is gated by the ALB target-group health check on `/health`.

### Manual deploy / infra change
```bash
make tf-plan-dev      # review
make tf-apply-dev     # apply
# prod:
make tf-plan-prod && make tf-apply-prod
```

### First-time setup (manual, once per AWS account)
- Create the Terraform state backend (S3 bucket + DynamoDB lock table) and fill
  in `backend` config before `terraform init`.
- Set the GitHub repo variable `AWS_DEPLOY_ROLE_ARN` to the
  `github_actions_role_arn` output (keyless OIDC auth).
- Subscribe to alerts: set `alarm_email` in the env tfvars (creates an SNS email
  subscription — **confirm the subscription email** or alarms go nowhere), or
  subscribe manually to the `alarm_sns_topic_arn` output.
- Put real API keys in Secrets Manager / SSM and reference them through the ECS
  task `secrets` map (`var.secrets`). Never bake keys into the image or config.

---

## 3. Rollback

ECS keeps prior task-definition revisions. To roll back to the last good image:

```bash
# Find the previous task definition revision
aws ecs describe-services --cluster <cluster> --services <service> \
  --query 'services[0].deployments'

# Re-point the service at the previous revision
aws ecs update-service --cluster <cluster> --service <service> \
  --task-definition <family>:<previous-revision> --force-new-deployment
```

Or revert the offending commit on `main` and let CI/CD redeploy. Prefer the
revert when the bad change is in code/config; prefer the ECS rollback when you
need to stop the bleeding *now*.

---

## 4. Observe

### Dashboards
- **CloudWatch dashboard** `<name>-overview`: ALB request/error counts, target
  latency p50/p95/p99, ECS CPU/memory, healthy/unhealthy host counts.
- **Grafana** (local stack or the Pi): app-level Prometheus metrics —
  `make docker-up` then http://localhost:3000 (admin/admin, dev only). Dashboards
  "Credential Relayer", "Security & usage", "LLM & upstreams" and "Pi host" are
  auto-provisioned.

### Key application metrics (Prometheus, `/metrics` on :9090)
| Metric | Meaning |
|---|---|
| `relayer_http_requests_total{route,status}` | HTTP traffic + status mix |
| `relayer_http_request_duration_seconds` | API latency histogram |
| `relayer_relay_requests_total{target,outcome}` | Relay success/error per target |
| `relayer_relay_errors_total{target}` | Relay failures per target |
| `relayer_jobs_processed_total{status}` | Async jobs completed/dead |
| `relayer_queue_depth` | Pending jobs waiting for a worker |

### Logs
```bash
make docker-logs                       # local
aws logs tail /ecs/<name> --follow     # CloudWatch
```
Logs are JSON. Trace one request by its `request_id`; filter errors with
`{ $.level = "error" }` (the CloudWatch error alarm uses the same pattern).

---

## 5. Alarms and responses

All alarms publish to the `<name>-alarms` SNS topic.

| Alarm | Likely cause | First moves |
|---|---|---|
| `alb-target-5xx` | App returning 5xx, or upstream targets failing | Check logs for stack traces / relay errors; check `relayer_relay_errors_total`; is a target down? |
| `alb-target-latency` | Slow upstreams, CPU saturation, Redis slowness | Check ECS CPU alarm + target latency panel; inspect slow targets |
| `alb-unhealthy-hosts` | Tasks failing `/health`, crash loop, bad deploy | Check ECS task events + logs; consider rollback (§3) |
| `ecs-cpu-high` / `ecs-memory-high` | Under-provisioned or load spike | No auto-scaling is configured: `aws ecs update-service --desired-count N` (Terraform ignores `desired_count` after create); task size via Terraform |
| `error-log-rate` | Spike in error-level logs | Tail logs, group by error message, identify the failing path |

### Dead-letter queue growing
Jobs that exhaust retries land in the dead-letter list (`relayer:jobs:dead`).
```bash
redis-cli LLEN relayer:jobs:dead         # how many
redis-cli LRANGE relayer:jobs:dead 0 9   # sample job IDs
redis-cli GET relayer:job:<id>           # inspect a job (body retained for debugging)
```
Root-cause the target (auth? wrong URL? consistently 5xx?), fix, then optionally
re-enqueue by `LPUSH`-ing the IDs back onto `relayer:jobs:pending`.

---

## 6. Common scenarios

**Redis down** — `/ready` returns 503 and webhooks/jobs fail. With the default
`rate_limit.backend: redis` and `fail_open: false`, every `/api/v1` request also
returns `503 rate limiter unavailable` (watch
`relayer_ratelimit_backend_errors_total`); set `rate_limit.fail_open: true` to keep
`/relay` and `/proxy` serving during an outage. Check Redis health (ElastiCache and
the ECS↔Redis security group, or the `redis` container on the Pi). The relayer
reconnects automatically.

**Rate-limit false positives (429s)** — limits are per API-key (`rate_limit` in
config). Raise `requests`/`window` or issue the client its own key. With
`rate_limit.backend: redis` (default) state is in Redis and shared across tasks;
with `memory` each task has its own budget, so the effective limit is
limit × task count.

**SSRF / blocked relay** — by design the relay refuses targets resolving to
private IPs and non-http(s) schemes. If a *legitimate* internal target is
blocked, set `allow_private_ips: true` (and `allow_http` if needed) on that
specific target — never globally.

**Load test** — `make loadtest URL=<alb-url> API_KEY=<key>` (uses `hey`/`k6` if
installed). Watch the CPU alarm; there is no auto-scaling, so capacity is `desired_count`.

---

## 7. Useful commands

```bash
make test            # unit + integration
make test-e2e        # end-to-end (needs Redis for the async cases)
make cover           # coverage summary
make docker-up       # full local stack: relayer + redis + prometheus + grafana
make tf-validate     # validate both terraform environments
curl -s localhost:8080/health
curl -s localhost:8080/ready
curl -s localhost:9090/metrics | grep relayer_
```

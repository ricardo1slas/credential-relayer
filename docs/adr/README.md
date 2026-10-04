# Architecture Decision Records

One record per entry in the README's [Design decisions](../../README.md#design-decisions). Each gives the
context, the decision, and its consequences, with sources in the [build log](../build-log.md),
[security roadmap](../security-roadmap.md) and [hardening notes](../hardening.md).

| # | Decision | Status | Date |
|---|---|---|---|
| [0001](0001-targets-by-name.md) | Targets by name, never by URL | Accepted | TODO(<pi-user>): date |
| [0002](0002-one-authorize-gate.md) | One `Authorize` gate for every entry point | Accepted | 2026-09-26 |
| [0003](0003-secrets-by-env-var-name.md) | Secrets by env-var name, fail fast | Accepted | 2026-09-26 |
| [0004](0004-no-retries-on-proxy.md) | No retries on the streaming proxy | Accepted | 2026-09-26 |
| [0005](0005-stream-redaction-minimal-holdback.md) | Redact streams with a minimal hold-back | Accepted | 2026-09-26 |
| [0006](0006-metric-labels-from-config.md) | Metric labels come from config, not clients | Accepted | 2026-09-26 |
| [0007](0007-slo-excludes-502.md) | The SLO excludes 502 | Accepted | 2026-09-26 |
| [0008](0008-no-public-ingress.md) | No public ingress: tailnet-only | Accepted | 2026-09-24 |

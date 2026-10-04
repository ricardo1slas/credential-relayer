# 0006. Metric labels come from config, not clients

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The security and usage dashboards label refusals and upstream outcomes by target and key
(`relayer_rejections_total{target,key,reason}`). A client can send any target name, including ones that
don't exist. Each distinct label value creates a new Prometheus series, so recording client input
verbatim lets anyone create unbounded series and exhaust Prometheus on a Pi with an SD card. Found while
building the dashboards. (Sources: [build log §13](../build-log.md#13-phase-9-observability--alerting),
`internal/relay/relay.go`.)

## Decision

Label values come only from config: target names the registry knows and key names from the auth config.
A target name the registry doesn't know is recorded as `unknown`. Request logs use the route pattern
(`/api/v1/jobs/{id}`), not the concrete path.

## Alternatives considered

- TODO(<pi-user>): Why not drop the target label on rejections altogether?
- TODO(<pi-user>): Why not cap cardinality in Prometheus (`sample_limit`, relabel rules) instead of in the app?
- TODO(<pi-user>): Why not hash or truncate unknown names so probing patterns stay visible?

## Consequences

- Series count is bounded by config size, whatever clients send.
- Probing with invented target names is still visible, as a rise in `unknown` refusals, but the
  individual names are not.
- Any new label must come from a closed set; reviewers check this for every metric change.

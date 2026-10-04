# 0007. The SLO excludes 502

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The relayer sits in front of upstreams it doesn't control (Gemini, Anthropic, any API). When an upstream
fails or can't be reached, the relayer returns 502. An availability SLO that counts those would page the
operator for other people's outages and burn the error budget on things no relayer change can fix.
(Sources: [build log §13](../build-log.md#13-phase-9-observability--alerting),
`infra/docker/prometheus/alerts.yml`.)

## Decision

SLI: the share of API requests not failed *by the relayer*: 5xx except 502, health probes excluded.
Target: 99.5% over 30 days (error budget 0.5%). Alert: multi-window burn rate (Google SRE workbook),
firing when both the 1-hour and 5-minute error ratios exceed 14.4x the budget rate. Upstream health is
tracked separately (`relayer_upstream_responses_total`, the `UpstreamFailing` and `UpstreamQuotaExhausted`
alerts, and the LLM & upstreams dashboard).

## Alternatives considered

- TODO(<pi-user>): Why not count 502 and accept that upstream outages spend the budget? (what users actually experience?)
- TODO(<pi-user>): Why not separate SLOs per upstream?
- TODO(<pi-user>): Could a relayer bug ever surface as 502 (e.g. the SSRF/URL-build error path), and would the SLO then miss it?

## Consequences

- The SLO measures what the relayer controls; upstream problems alert through their own rules.
- Prometheus retention went from 7 to 35 days (1 GB cap) so a 30-day window always exists.
- The 502 exclusion depends on the relayer only returning 502 for upstream failure; any new code path that
  returns 502 for its own errors would hide from the SLO.

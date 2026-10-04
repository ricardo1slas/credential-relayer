# 0004. No retries on the streaming proxy

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

`/relay` retries 5xx and transport errors with exponential backoff and jitter (never 4xx), which suits
buffered JSON calls. The streaming proxy (`/api/v1/proxy/{target}/...`) forwards raw requests from SDKs and
agents, mostly LLM calls, and streams the response back as it arrives. (Sources:
[build log §5, §16](../build-log.md#16-phase-12-credential-broker-secrets-policy-streaming),
[hardening.md §4 item 3d](../hardening.md#4-app-level-security-roadmap-mapped-to-owasp-api-top-10-2023).)

## Decision

The proxy never retries. Upstream errors and failures are returned to the caller as they happen (502 when
the upstream can't be reached). `/relay` keeps its retry policy unchanged.

## Alternatives considered

- TODO(<pi-user>): Why not retry only before the first byte has been flushed to the client?
- TODO(<pi-user>): Why not retry idempotent methods (GET) on the proxy, or honour an `Idempotency-Key`?
- TODO(<pi-user>): Why leave retries to the SDK instead of the relayer? (SDKs already retry; double retries multiply load and cost?)

## Consequences

- A stream that has started can't be replayed, and LLM POSTs aren't idempotent: a retry could bill twice
  or produce a different answer. Not retrying avoids both.
- Callers keep their own retry logic; the SDKs that use the proxy already have it.
- Proxy failures show up directly in upstream metrics, not hidden behind retries.
- The proxy needs its own timeout handling: the server write deadline is lifted for that response to the
  target timeout plus a 5 s grace, so a 502 can still be written.

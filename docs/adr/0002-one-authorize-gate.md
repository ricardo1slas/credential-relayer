# 0002. One `Authorize` gate for every entry point

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Once the relayer injected real credentials (ADR 0003), any key allowed to use a target could call every
endpoint of the upstream with the operator's credential (threat T16). There are four ways a request reaches
an upstream: `/relay`, `/webhook`, `/proxy` and the queue worker. Checks duplicated per entry point drift,
and the worker runs jobs that were accepted under an earlier config. (Sources:
[build log §16](../build-log.md#16-phase-12-credential-broker-secrets-policy-streaming),
[hardening.md §4 item 3c](../hardening.md#4-app-level-security-roadmap-mapped-to-owasp-api-top-10-2023),
security-roadmap change log 2026-09-26.)

## Decision

`Handler.Authorize` is the single gate. It checks, in order: key scope, method, target, path policy
(`allow_paths`, matched after `../` resolution on segment boundaries, refusing encoded dots, slashes,
backslashes and `;`), and body policy (`max_body_bytes`). `/relay`, `/webhook` (before enqueue), `/proxy`
and the worker (before each delivery) all call it, and every refusal is counted in one place with a reason.

## Alternatives considered

- TODO(<pi-user>): Why not chi middleware per route instead of a function each handler calls? (the worker has no HTTP request)
- TODO(<pi-user>): Why check scope before target existence? (a key can't tell a forbidden target from a missing one)
- TODO(<pi-user>): Why re-check in the worker instead of trusting the enqueue-time decision?

## Consequences

- Adding an entry point can't silently skip a check, and `relayer_rejections_total{target,key,reason}` sees all refusals.
- A bad webhook gets a 4xx at enqueue instead of dead-lettering later; a job queued before a policy change
  can't bypass it (policy failures dead-letter without retries).
- `FuzzPathPolicy` (4.4M inputs) models an upstream that decodes and cleans paths and found no bypass.
- Known limit: redirects the upstream issues within the same host are followed without a policy re-check.

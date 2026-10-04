# 0001. Targets by name, never by URL

- **Status:** Accepted
- **Date:** TODO(<pi-user>): date (Phase 2, before the 2026-04-18 Phase 5.5 audit, which already lists "resolves target URL by name" as a strength)

## Context

A relay that forwards to a client-supplied destination is an SSRF engine: anything the relayer can reach
(cloud metadata at `169.254.169.254`, internal services) becomes reachable by any caller. Threat T1 in the
security roadmap. (Sources: [build log §5, §9.5](../build-log.md#5-phase-2-core-relay-logic),
[security-roadmap.md T1](../security-roadmap.md#t1-ssrf--url-confusion----high).)

## Decision

Clients name a configured target (`"target": "gemini"` or `/api/v1/proxy/{target}/...`); the base URL comes
only from operator config, validated at startup (https only unless `allow_http`, no userinfo). The client
controls just the path, which is resolved against the base URL and must stay on the same scheme and host.
Dials to private, loopback and link-local addresses are refused at connect time, redirects included.

## Alternatives considered

- TODO(<pi-user>): Why not let clients pass a full URL checked against an allowlist? (allowlist bypasses via redirects, DNS rebinding, parser differentials?)
- TODO(<pi-user>): Why not a per-target host allowlist instead of a single base URL per target?
- TODO(<pi-user>): Why resolve paths with `ResolveReference` instead of string concatenation plus input filtering?

## Consequences

- The SSRF surface is the path alone. Even that was a real bug once: `target.URL + req.Path` let
  `@evil.com/x` change the host (fixed 2026-04-18), and `FuzzBuildTargetURL` (2.7M executions) now guards it.
- Adding an upstream is a config change and redeploy, not a client change.
- Target names are a closed set, which also lets metrics record unknown names as `unknown` (ADR 0006).
- Per-target policy (methods, paths, body size) has a natural home: the target entry (ADR 0002).

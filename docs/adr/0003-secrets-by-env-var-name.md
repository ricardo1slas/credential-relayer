# 0003. Secrets by env-var name, fail fast

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The point of the broker is that clients never hold upstream secrets (threat T15). The secrets have to live
somewhere on the relayer without ending up in tracked YAML (the project already leaked a live dev key once
through a committed file, §9.4 of the build log), in Redis with queued jobs, or in logs. A missing secret must
not degrade into unauthenticated upstream calls. (Sources:
[build log §16](../build-log.md#16-phase-12-credential-broker-secrets-policy-streaming),
[hardening.md §4 item 3b](../hardening.md#4-app-level-security-roadmap-mapped-to-owasp-api-top-10-2023),
[security-roadmap.md](../security-roadmap.md) T15.)

## Decision

A target's `secret_headers` maps a header to the *name* of an environment variable
(`X-Goog-Api-Key: GEMINI_API_KEY`). Values are read once at startup; a missing or empty variable stops
startup, and errors name the variable, never the value. Secrets are injected at send time, after client
headers, dropping any client header that folds to the same name. On the Pi, the values live in
`infra/docker/.env.relayer`, given only to the relayer container.

## Alternatives considered

- TODO(<pi-user>): Why not secret values directly in `config.yaml` (gitignored)? (one file to leak, backups, rsync modes?)
- TODO(<pi-user>): Why not a secrets manager (Vault, AWS Secrets Manager, SOPS) from day one?
- TODO(<pi-user>): Why fail at startup instead of failing only requests to the affected target?

## Consequences

- Config files are safe to share and review; they only name variables.
- Rotating an upstream secret needs a restart, since values are read once.
- Secrets never reach Redis, are stripped on cross-host or scheme-changing redirects, and are redacted
  when echoed (ADR 0005). An upstream that re-encodes the secret (e.g. base64) defeats redaction.
- A `SecretProvider` interface seam is being added, with an env-only implementation, so Vault or a cloud
  secrets manager could plug in later without changing how targets reference secrets.

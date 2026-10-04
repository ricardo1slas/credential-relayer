# 0005. Redact streams with a minimal hold-back

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Some upstreams echo request headers (httpbin's `/headers` does), which would return an injected secret to
the client. On `/relay` the whole body is buffered, so redaction is a simple replace. On the streaming
proxy a secret can be split across two chunks, and buffering the whole response would break streaming (SSE
tokens must arrive as they are produced). (Sources:
[build log §16](../build-log.md#16-phase-12-credential-broker-secrets-policy-streaming),
[hardening.md §4 items 3b, 3d](../hardening.md#4-app-level-security-roadmap-mapped-to-owasp-api-top-10-2023),
[security-roadmap.md](../security-roadmap.md) T17.)

## Decision

`streamRedactor` holds back only the longest tail of the stream that could still grow into one of the
target's secrets; everything before it is redacted and flushed immediately. If the stream is truncated,
the held tail is dropped rather than emitted. `Accept-Encoding` is stripped from proxied requests so the
transport decompresses and redaction sees plaintext.

## Alternatives considered

- TODO(<pi-user>): Why not buffer the whole response (or each SSE event) before redacting?
- TODO(<pi-user>): Why not a fixed hold-back of the longest secret's length?
- TODO(<pi-user>): Why not refuse to proxy responses that contain a secret at all, instead of redacting?

## Consequences

- Ordinary SSE events flush with no added latency; only bytes that match a secret prefix wait.
- A test checks every three-way split of a stream; truncation can't leak half a secret.
- Responses are never compressed between upstream and relayer, which costs some bandwidth.
- Limit: an upstream that re-encodes the secret (base64, JSON escaping) defeats literal redaction.

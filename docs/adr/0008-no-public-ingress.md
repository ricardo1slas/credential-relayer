# 0008. No public ingress: tailnet-only

- **Status:** Accepted
- **Date:** 2026-09-24 (Pi audit in hardening.md); CI-gated deploys added 2026-09-26

## Context

After the AWS credits ran out, the live deployment moved to a Raspberry Pi at home. The relayer holds real
upstream credentials, and the Pi also runs Prometheus, Grafana and Alertmanager. The 2026-09-24 audit found
RDP and SSH listening on the LAN, Redis published unauthenticated on the tailnet IP, and an allow-all
tailnet policy. CI still needs a way in to deploy. (Sources: [hardening.md §1-3](../hardening.md),
[build log §14, §15](../build-log.md#15-phase-11-raspberry-pi-tailnet-only-deployment),
[pi-deploy.md](../pi-deploy.md).)

## Decision

No public ingress: the Pi is tailnet-only, with deny-by-default Tailscale ACLs. Every Compose port binds to
the Pi's tailnet address (`BIND_IP`), never `0.0.0.0`; Redis binds to `127.0.0.1`; ufw allows inbound only
on `tailscale0`. Access is granted by tag (`tag:pi`, `tag:ci`) with policy tests, and SSH is Tailscale SSH.
CI joins the tailnet as an ephemeral node tagged `tag:ci`, which can reach only SSH and the API port, and
deploys only a commit whose `ci` run passed, only on a push to this repository.

## Alternatives considered

- TODO(<pi-user>): Why not a public endpoint behind TLS and the relayer's own auth (Cloudflare Tunnel, Tailscale Funnel)?
- TODO(<pi-user>): Why not a self-hosted runner on the Pi instead of CI joining the tailnet?
- TODO(<pi-user>): Why a Tailscale OAuth client for CI rather than workload identity federation (listed as a next step)?

## Consequences

- Only tailnet members can reach the relayer; clients outside the tailnet (and HTTPS-only tools such as
  Gemini CLI) need a tailnet device or an SSH port-forward.
- Docker publishes ports through FORWARD, which ufw's INPUT rules don't cover, so the `BIND_IP` binding is
  the real guard; publishing with `0.0.0.0` would bypass the firewall.
- A policy test asserts that a compromised Pi cannot reach the operator's laptop.
- CI holds tailnet credentials, which is why the deploy refuses fork `workflow_run` events.

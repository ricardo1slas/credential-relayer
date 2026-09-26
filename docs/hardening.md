# Hardening: tailnet, Pi host, and app roadmap

Audit of the Pi on 2026-09-24 plus prioritised next steps. Sources at the bottom.

## 1. Audit: where we are

| Area | Finding | Risk | Fix |
|---|---|---|---|
| Pi network | `:3389` (RDP) and `:22` listen on `0.0.0.0`, reachable from the LAN | High | `scripts/harden-pi.sh` (ufw: inbound only on `tailscale0`) |
| Pi network | Redis was published on the tailnet IP, unauthenticated | High | Fixed: now `127.0.0.1` only; the relayer reaches it over the compose network |
| Pi docker | No log rotation (`json-file`, no `daemon.json`): disk fill + SD wear | Med | `harden-pi.sh` step 2 |
| Pi OS | 49 pending upgrades; unattended-upgrades enabled | Med | `harden-pi.sh` step 3-4, then reboot |
| Tailscale | Pi is user-owned, untagged, so it inherits your identity/access; key expires 2027-03-23 | Med | Tag it `tag:pi` (below) |
| Tailscale | Policy unknown; new tailnets default to allow-all (`*` -> `*`) | High | Deny-by-default grants (below) |
| Containers | non-root, read-only rootfs, `cap_drop: ALL`, `no-new-privileges`, mem/pid limits, seccomp+AppArmor default | OK | none |
| Alerts | `AuthFailureSpike` / `RateLimitSpike` rules added | OK | set SMTP creds (see pi-deploy.md) |

## 2. Tailscale: deny-by-default policy

Key points from Tailscale's hardening guide: least privilege via tags + grants, `check` mode for
humans on SSH, key expiry, device approval, policy tests, audit-log review, remove unused OAuth
clients/auth keys, keep clients updated, MFA on the identity provider (hardware key/passkey).

Tagged devices are **not** owned by a user, so `autogroup:self` no longer covers them: the
explicit `autogroup:member -> tag:pi` grant is required. A Tailscale SSH rule also needs a
matching network grant for port 22 (a common gotcha). CI must use `accept`, not `check`
(`check` waits for a human to re-authenticate in a browser).

Ready-to-paste, complete file: [`tailscale-policy.hujson`](tailscale-policy.hujson) (paste over the JSON editor). The snippet below is the same policy, abbreviated:

```json
{
  "tagOwners": { "tag:pi": ["autogroup:admin"], "tag:ci": ["autogroup:admin"] },

  "grants": [
    { "src": ["autogroup:member"], "dst": ["autogroup:self"], "ip": ["*"] },
    { "src": ["autogroup:member"], "dst": ["tag:pi"],
      "ip": ["tcp:22", "tcp:8080", "tcp:3000", "tcp:9091", "tcp:9093"] },
    { "src": ["tag:ci"], "dst": ["tag:pi"], "ip": ["tcp:22", "tcp:8080"] }
  ],

  "ssh": [
    { "action": "check",  "src": ["autogroup:member"], "dst": ["tag:pi"], "users": ["<pi-user>"], "checkPeriod": "12h" },
    { "action": "accept", "src": ["tag:ci"],            "dst": ["tag:pi"], "users": ["<pi-user>"] }
  ],

  "tests": [
    { "src": "tag:ci", "accept": ["tag:pi:22", "tag:pi:8080"], "deny": ["tag:pi:3000", "tag:pi:9091"] },
    { "src": "tag:pi", "deny": ["<device-tailnet-ip>:22"] }
  ]
}
```
The last test encodes the important property: a compromised Pi cannot reach your laptop. Keep
your existing rules for your other devices; this only shows the Pi/CI parts. Then, on the Pi:

```bash
sudo tailscale up --ssh --advertise-tags=tag:pi   # or: admin console -> Machines -> Edit ACL tags   # needs tagOwners above first
```
Tagging also disables node-key expiry for the Pi, which is what you want for a server.

More (optional, in order of value): device approval on; **Tailnet Lock** (nodes must be signed
by a key you hold, so even a compromised control plane can't add a node; store the disablement
secrets safely); Tailscale audit/flow logs; delete unused auth keys and OAuth clients; replace
the CI OAuth secret with **workload identity federation** (`audience:` input, `id-token: write`),
which removes the long-lived secret from GitHub the same way OIDC did for AWS.

## 3. Pi host

`sudo bash scripts/harden-pi.sh` on the Pi (read it first). It:
1. `ufw`: default deny inbound, allow only `tailscale0`. This closes LAN `:22` and RDP `:3389`.
   Tailscale SSH is served by tailscaled on the tailnet interface, so it is unaffected. Docker
   publishes ports through the FORWARD chain, which ufw's INPUT rules do not cover, so the real
   guard is the `BIND_IP` binding (verified: LAN requests are refused). Never publish with `0.0.0.0`.
2. Caps Docker logs at 3 x 10 MB.
3. Enables unattended security updates, applies the pending ones (reboot if asked).

Not in the script (need your call):
- Disable RDP/desktop services entirely if the Pi is headless: `systemctl disable --now xrdp` /
  `gnome-remote-desktop`, or boot to `multi-user.target`.
- `PasswordAuthentication no` in `/etc/ssh/sshd_config.d/` once key or Tailscale SSH access is proven.
  `fail2ban` adds little when sshd is unreachable from the LAN.
- A dedicated `deploy` user for CI instead of `<pi-user>`. Note that docker-group membership is
  effectively root, so this is defence in depth, not isolation.
- Audit tools: `lynis audit system` (host) and `docker-bench-security` (CIS Docker Benchmark).
- SD-card durability: boot from USB SSD if this becomes long-lived.

## 4. App-level security roadmap (mapped to OWASP API Top 10, 2023)

| # | Feature | Addresses | Size |
|---|---|---|---|
| 1 | ✅ **Per-key target allow-list** (`targets:` per key; fails closed; 403 identical for forbidden vs unknown targets) **+ job ownership** (a key can only read jobs it enqueued; others get 404) | API1/API5 (object/function-level authz) | done 2026-09-25 |
| 2 | **Hash API keys at rest** (SHA-256 of a high-entropy key; compare hashes in constant time) + `expires_at` and multiple active keys for rotation | API2 (broken auth), key hygiene | small |
| 3 | **Fuzz tests** for `buildTargetURL` and the header filter (`go test -fuzz`) | API7 (SSRF), regression safety | small |
| 4 | **Auth-failure throttling** per source IP + a structured **audit log** (key name, target, allow/deny, never payloads) | API2, API4, detection | small |
| 5 | **Supply chain in CI**: SBOM (syft), image signing (cosign keyless), build provenance attestation, pin Actions by SHA, Trivy IaC scan | API10 (unsafe consumption), SLSA | medium |
| 6 | **Distroless/static runtime image** and a `-healthcheck` flag (drops `curl` + alpine from the image) | attack surface | small |
| 7 | **HMAC-signed outbound webhooks** (`t=` timestamp + `v1=` SHA-256, replay window, dual-signature rotation) | integrity/replay for receivers | medium |
| 8 | **Encrypt queued payloads at rest** (AES-GCM, key from env) and Redis `requirepass` + TLS | roadmap T9 | medium |
| 9 | Security headers + `trust_proxy` flag (roadmap T6) | API8 (misconfiguration) | small |

Recommended order: 1, 2, 3, 4 (one focused week, all small, big story: authz + auth + SSRF
fuzzing + detection), then 5-6, then 7-8.

## Sources
- [Tailscale: best practices to secure your tailnet](https://tailscale.com/kb/1196/security-hardening)
- [Tailscale SSH access rules](https://tailscale.com/docs/features/tailscale-ssh) and [grants](https://tailscale.com/docs/features/access-control/grants)
- [Tailscale GitHub Action (v4)](https://github.com/tailscale/github-action)
- [Tailscale key expiry](https://tailscale.com/docs/features/access-control/key-expiry) and [Tailnet Lock](https://tailscale.com/docs/features/tailnet-lock)
- [Why Docker bypasses UFW](https://dev.to/alanwest/why-docker-bypasses-ufw-and-how-to-actually-lock-it-down-26ep) and [Baeldung](https://www.baeldung.com/linux/docker-container-published-port-ignoring-ufw-rules)
- [OWASP API Security Top 10 (2023) overview](https://www.wiz.io/academy/api-security/owasp-api-security)
- [Webhook HMAC + key rotation](https://didit.me/blog/api-security-webhooks-hmac-key-rotation/)

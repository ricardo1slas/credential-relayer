# Security Policy

## Reporting a vulnerability

Please report security issues **privately** through GitHub's
[private vulnerability reporting](https://github.com/ricardo1slas/credential-relayer/security/advisories/new)
(Security tab -> "Report a vulnerability"). Do not open a public issue.

Include what you found, how to reproduce it, and the impact you expect. You should get an
acknowledgement within a few days. This is a personal portfolio project maintained on a
best-effort basis, so there is no bug bounty and no SLA.

## Scope and design

The threat model, the findings that were fixed, and the open backlog live in
[`docs/security-roadmap.md`](docs/security-roadmap.md). Deployment hardening (Tailscale
policy, host firewall, container settings) is in [`docs/hardening.md`](docs/hardening.md).

Automated checks run on every push and nightly: `govulncheck`, `gosec`, `gitleaks`,
Trivy (image and config), and Dependabot for dependencies.

## Supported versions

Only the latest commit on `main` is supported.

# Raspberry Pi deploy (Tailscale)

The default deployment target since AWS credits ran out. The stack is the same
`infra/docker/docker-compose.yml` used locally; every published port binds to
the Pi's **tailnet IP only** (`BIND_IP`), never the LAN or the internet.

## One-time Pi setup
```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER      # then log out/in
sudo apt install -y rsync
sudo tailscale set --ssh           # identity-based SSH, no keys to manage
echo GRAFANA_PASSWORD=<strong-pw> > ~/relayer/infra/docker/.env   # after first deploy
```
`configs/config.yaml` (gitignored, holds API keys) is copied by the first
manual `make deploy-pi` and is protected from `--delete` afterwards.

## Manual deploy
```bash
make deploy-pi                 # PI=<user>@<pi-host> by default
make deploy-pi PI=user@host
```

## CI deploy (`.github/workflows/deploy.yml`)
When `ci` succeeds on a push to `main`, the deploy joins the tailnet as an ephemeral `tag:ci`
node, checks out exactly the commit `ci` tested, runs `make deploy-pi`, then curls `/ready`.
A failing test never deploys. If `main` has already moved on, the older run skips so the Pi
never rolls back. `security` doesn't block deploys. Actions → deploy-pi → "Run workflow"
deploys manually without waiting for `ci`. Fork PRs can't trigger it: the job requires a `push` event from
this repo.

Tailscale admin setup (once), full policy JSON in [`hardening.md`](hardening.md#2-tailscale-deny-by-default-policy):
1. Add `tag:pi` / `tag:ci` to `tagOwners`, the grants and ssh rules, then on the Pi:
   `sudo tailscale up --ssh --advertise-tags=tag:pi   # or: admin console -> Machines -> Edit ACL tags`.
2. **OAuth client** (Settings -> Trust credentials): scope `auth_keys` (write), tag `tag:ci`.
3. GitHub repo secrets: `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_SECRET`.
   Optional repo variable `PI_HOST` (defaults to the Pi's tailnet IP).

## Email alerts
Prometheus rules (`infra/docker/prometheus/alerts.yml`) -> Alertmanager -> email. Create a Gmail
[app password](https://myaccount.google.com/apppasswords) (needs 2-step verification), then on the Pi:
```bash
cat >> ~/relayer/infra/docker/.env <<EOT
SMTP_USER=you@gmail.com
SMTP_PASS=<16-char app password>
ALERT_EMAIL=you@gmail.com
EOT
cd ~/relayer/infra/docker && BIND_IP=$(tailscale ip -4) docker compose up -d alertmanager
```
Test: `curl -XPOST http://<pi>:9093/api/v2/alerts -H 'Content-Type: application/json' -d '[{"labels":{"alertname":"TestAlert"}}]'`
(mail arrives after `group_wait`, 30s). Alertmanager UI: `http://<pi>:9093`.

## Access (from any tailnet device)
| What | URL |
|---|---|
| Relayer | `http://<pi>:8080` |
| Prometheus | `http://<pi>:9091` |
| Grafana | `http://<pi>:3000` |

`.github/workflows/deploy-aws.yml` is kept as a manual-only reference deploy.

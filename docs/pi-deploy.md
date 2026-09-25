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
Every push to `main` joins the tailnet as an ephemeral `tag:ci` node and runs
`make deploy-pi`, then curls `/ready`.

Tailscale admin setup (once):
1. **ACL** (`tagOwners` + ssh rule):
   ```json
   "tagOwners": {"tag:ci": ["autogroup:admin"]},
   "ssh": [{"action": "accept", "src": ["tag:ci"], "dst": ["autogroup:self", "<pi-tag-or-user>"], "users": ["<pi-user>"]}]
   ```
   Least privilege: tag:ci should reach the Pi only. Tagging the Pi (`tag:pi`)
   and using `dst: ["tag:pi"]` is tighter than user-owned devices.
2. **OAuth client** (Settings → OAuth clients): scope `auth_keys` (write), tag `tag:ci`.
3. GitHub repo secrets: `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_SECRET`.
   Optional repo variable `PI_HOST` (defaults to MagicDNS name `<pi-user>`).

## Access (from any tailnet device)
| What | URL |
|---|---|
| Relayer | `http://<pi>:8080` |
| Prometheus | `http://<pi>:9091` |
| Grafana | `http://<pi>:3000` |

`.github/workflows/deploy-aws.yml` is kept as a manual-only reference deploy.

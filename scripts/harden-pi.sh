#!/usr/bin/env bash
# Host hardening for the Pi (Ubuntu). Run ON the Pi, after reading it:
#   sudo bash harden-pi.sh
# Idempotent. Tailscale SSH (handled by tailscaled on the tailnet interface) keeps
# working; only LAN/internet-facing access is closed.
set -euo pipefail
[ "$EUID" -eq 0 ] || { echo "run with sudo"; exit 1; }
ip link show tailscale0 >/dev/null || { echo "tailscale0 not found; refusing (would lock you out)"; exit 1; }

echo "[1/4] firewall: deny inbound except on tailscale0 (closes LAN :22 and :3389/RDP)"
apt-get install -y -qq ufw >/dev/null
ufw default deny incoming >/dev/null
ufw default allow outgoing >/dev/null
ufw allow in on tailscale0 >/dev/null   # allow BEFORE enabling, so the tailnet session survives
ufw --force enable >/dev/null

echo "[2/4] docker: cap container log size (SD-card wear + disk-fill)"
if [ ! -f /etc/docker/daemon.json ]; then
  cat > /etc/docker/daemon.json <<'JSON'
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "3" } }
JSON
  systemctl restart docker
  # Existing containers keep their old log config; recreate them once:
  #   cd ~/relayer/infra/docker && BIND_IP=$(tailscale ip -4) docker compose up -d --force-recreate
else
  echo "  /etc/docker/daemon.json exists; merge log-opts by hand"
fi

echo "[3/4] automatic security updates"
apt-get install -y -qq unattended-upgrades >/dev/null
systemctl enable --now unattended-upgrades >/dev/null 2>&1

echo "[4/4] apply pending updates now"
apt-get update -qq && apt-get -y -qq upgrade
[ -f /var/run/reboot-required ] && echo "REBOOT REQUIRED: sudo reboot" || true

echo; ufw status verbose | head -8

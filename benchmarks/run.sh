#!/usr/bin/env bash
# Benchmark the relayer on the Pi. Load is generated from this machine over Tailscale.
# Usage: PI=<user>@<pi-host> benchmarks/run.sh   (or `make bench` with PI in local.mk; env: DUR=20s)
# Needs: hey (brew install hey), ssh access to the Pi, Docker on the Pi.
set -euo pipefail
cd "$(dirname "$0")/.."

PI=${PI:?set PI=<user>@<pi-host>}; HOST=${PI#*@}; DUR=${DUR:-20s}
KEY=bench-key-not-a-secret
OUT=benchmarks/results/$(date +%Y-%m-%d_%H%M)_$(git rev-parse --short HEAD).txt
BODY='{"target":"mock","method":"GET","path":"/"}'
COMPOSE='cd relayer/benchmarks && BIND_IP=$(tailscale ip -4) docker compose -f docker-compose.bench.yml'

trap 'ssh "$PI" "$COMPOSE down -v" >/dev/null 2>&1 || true' EXIT
command -v hey >/dev/null || { echo "install hey: brew install hey"; exit 1; }

make sync-pi >/dev/null
ssh "$PI" "$COMPOSE up -d --build --wait" >/dev/null

run() { # name, hey args...
  echo -e "\n=== $1 ===" | tee -a "$OUT"; shift
  hey -z "$DUR" "$@" | grep -E 'Requests/sec|Average|Slowest|50%|95%|99%|^ +\[[0-9]{3}\]|Error' | tee -a "$OUT"
}

{ echo "commit: $(git rev-parse --short HEAD)  date: $(date -u +%FT%TZ)  duration/scenario: $DUR"
  ssh "$PI" 'echo "pi: $(tr -d "\0" </proc/device-tree/model 2>/dev/null) | $(nproc) cores | $(uname -m)"'
  echo "load generator: $(uname -sm) over tailscale"; } | grep -v '^#' | tee "$OUT"

hey -z 3s -c 10 -m POST -H "X-API-Key: $KEY" -d "$BODY" "http://$HOST:8081/api/v1/relay" >/dev/null  # warmup
run "baseline: mock direct, c=50 (no relayer)"    -c 50  "http://$HOST:8082/"
run "relay via relayer, c=50"                     -c 50  -m POST -H "X-API-Key: $KEY" -H "Content-Type: application/json" -d "$BODY" "http://$HOST:8081/api/v1/relay"
# CPU profile (15s) captured mid-run; inspect: go tool pprof -top <file>
( sleep 3; curl -s -o "${OUT%.txt}.c200.pprof" "http://$HOST:9090/debug/pprof/profile?seconds=15" ) &
run "relay via relayer, c=200"                    -c 200 -m POST -H "X-API-Key: $KEY" -H "Content-Type: application/json" -d "$BODY" "http://$HOST:8081/api/v1/relay"
wait
echo -e "\nsaved: $OUT (+ .c200.pprof)"

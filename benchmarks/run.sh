#!/usr/bin/env bash
# Benchmark the relayer on the Pi. Load is generated from this machine over Tailscale.
# Usage: PI=<user>@<pi-host> benchmarks/run.sh   (or `make bench` with PI in local.mk; env: DUR=20s)
# Needs: hey (brew install hey), ssh access to the Pi, Docker on the Pi.
set -euo pipefail
cd "$(dirname "$0")/.."

PI=${PI:?set PI=<user>@<pi-host>}; HOST=${PI#*@}; DUR=${DUR:-20s}
KEY=bench-key-not-a-secret-000000000000
OUT=benchmarks/results/$(date +%Y-%m-%d_%H%M)_$(git rev-parse --short HEAD).txt
BODY='{"target":"mock","method":"GET","path":"/"}'
COMPOSE='cd relayer/benchmarks && BIND_IP=$(tailscale ip -4) docker compose -f docker-compose.bench.yml'
RAW=$(mktemp -d)  # full hey output per scenario, for the overhead calc

trap 'rm -rf "$RAW"; ssh "$PI" "$COMPOSE down -v" >/dev/null 2>&1 || true' EXIT
command -v hey >/dev/null || { echo "install hey: brew install hey"; exit 1; }

make sync-pi >/dev/null
ssh "$PI" "$COMPOSE up -d --build --wait" >/dev/null
for port in 8081 8082 9090; do  # the tailnet ACL must allow these from the load generator
  curl -s -o /dev/null -m 5 "http://$HOST:$port/" ||
    { echo "cannot reach $HOST:$port: allow tcp:8081,8082,9090 to tag:pi in the tailnet policy (see benchmarks/README.md)"; exit 1; }
done

run() { # tag, name, hey args...
  local tag=$1; echo -e "\n=== $2 ===" | tee -a "$OUT"; shift 2
  hey -z "$DUR" "$@" >"$RAW/$tag"
  grep -E 'Requests/sec|Average|Slowest|50%|95%|99%|^ +\[[0-9]{3}\]|Error' "$RAW/$tag" | tee -a "$OUT"
}

overhead() { # concurrency -> relay minus direct (ms) and relay/direct throughput, from saved hey output
  awk -v c="$1" '
    /Requests\/sec/ { rps[FILENAME] = $2 }
    /^ +50%/        { p50[FILENAME] = $3 }   # hey prints "  50%% in 0.0286 secs"
    /^ +95%/        { p95[FILENAME] = $3 }
    /Error distribution/ { err[FILENAME] = 1 }
    END {
      d = ARGV[1]; r = ARGV[2]
      # hey counts failed requests in req/s, so require latencies (only printed for responses) on both sides.
      if (p50[d] == "" || p50[r] == "") { printf "c=%s: n/a (no successful responses)\n", c; exit }
      printf "c=%s: delta p50 %+.1f ms | delta p95 %+.1f ms | throughput ratio %.2f%s\n",
        c, (p50[r] - p50[d]) * 1000, (p95[r] - p95[d]) * 1000, rps[r] / rps[d],
        (err[d] || err[r]) ? "  (WARNING: errors in this run, see above)" : ""
    }' "$RAW/direct-c$1" "$RAW/relay-c$1"
}

{ echo "commit: $(git rev-parse --short HEAD)  date: $(date -u +%FT%TZ)  duration/scenario: $DUR"
  ssh "$PI" 'echo "pi: $(tr -d "\0" </proc/device-tree/model 2>/dev/null) | $(nproc) cores | $(uname -m)"'
  echo "load generator: $(uname -sm) over tailscale"; } | grep -v '^#' | tee "$OUT"

hey -z 3s -c 10 -m POST -H "X-API-Key: $KEY" -d "$BODY" "http://$HOST:8081/api/v1/relay" >/dev/null  # warmup
# Each relay scenario has a direct baseline at the same concurrency: a plain GET / to the mock,
# i.e. the request the relayer itself makes upstream for $BODY.
run direct-c50  "baseline: mock direct, c=50 (no relayer)"   -c 50  "http://$HOST:8082/"
run relay-c50   "relay via relayer, c=50"                    -c 50  -m POST -H "X-API-Key: $KEY" -H "Content-Type: application/json" -d "$BODY" "http://$HOST:8081/api/v1/relay"
run direct-c200 "baseline: mock direct, c=200 (no relayer)"  -c 200 "http://$HOST:8082/"
# CPU profile (15s) captured mid-run; inspect: go tool pprof -top <file>
( sleep 3; curl -s -o "${OUT%.txt}.c200.pprof" "http://$HOST:9090/debug/pprof/profile?seconds=15" ) &
run relay-c200  "relay via relayer, c=200"                   -c 200 -m POST -H "X-API-Key: $KEY" -H "Content-Type: application/json" -d "$BODY" "http://$HOST:8081/api/v1/relay"
wait
{ echo -e "\n=== relayer overhead (relay minus direct; ratio = relay req/s / direct req/s) ==="
  overhead 50; overhead 200; } | tee -a "$OUT"
echo -e "\nsaved: $OUT (+ .c200.pprof)"

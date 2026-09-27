#!/usr/bin/env bash
# Steady mixed traffic so the Grafana dashboard has something to show. Ctrl-C to stop.
#   scripts/traffic.sh            ~1 req/s, mostly healthy with a little of everything
#   scripts/traffic.sh --burst    also fires a 150-request burst each minute (rate limiter -> 429s)
#   scripts/traffic.sh --chaos    ~30% upstream 5xx: WILL trigger the HighServerErrorRate email after 5 min
# env: RELAYER_URL (default http://localhost:8080), RELAYER_API_KEY or RELAYER_API_KEY_FILE (a key allowed
#      to use the httpbin target), RATE (req/s). `make traffic` reads these from local.mk if present.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
URL=${RELAYER_URL:-http://localhost:8080}
KEY=${RELAYER_API_KEY:-}
[ -z "$KEY" ] && [ -n "${RELAYER_API_KEY_FILE:-}" ] && KEY=$(tr -d '[:space:]' < "${RELAYER_API_KEY_FILE/#\~/$HOME}")
[ -n "$KEY" ] || { echo "set RELAYER_API_KEY or RELAYER_API_KEY_FILE"; exit 1; }
RATE=${RATE:-1}; BURST=0; CHAOS=0
for a in "$@"; do case $a in --burst) BURST=1;; --chaos) CHAOS=1;; esac; done
E503=2; [ $CHAOS = 1 ] && E503=30   # % of requests that hit an upstream 503

call() { # label, key, endpoint, json
  local code; code=$(curl -s -m 20 -o /dev/null -w '%{http_code}' -X POST "$URL/api/v1/$3" \
    -H "X-API-Key: $2" -H 'Content-Type: application/json' -d "$4")
  printf '%s  %-22s %s\n' "$(date +%T)" "$1" "$code"
}
relay() { call "$1" "$KEY" relay "{\"target\":\"httpbin\",\"method\":\"GET\",\"path\":\"$2\"}"; }
stream() { # streamed proxy response: 5 bytes over ~2s (stream TTFB/duration panels)
  local code; code=$(curl -s -m 20 -o /dev/null -w '%{http_code}' -H "X-API-Key: $KEY" "$URL/api/v1/proxy/httpbin/drip?duration=2&numbytes=5&delay=0")
  printf '%s  %-22s %s\n' "$(date +%T)" "proxy stream" "$code"
}

echo "traffic -> $URL  rate=${RATE}/s burst=$BURST chaos=$CHAOS  (Ctrl-C to stop)"
last_burst=$SECONDS
while true; do
  r=$((RANDOM % 100))
  if   [ $r -lt $E503 ];        then relay "upstream 503 (retries)" /status/503 &
  elif [ $r -lt $((E503+4)) ];  then relay "upstream 404" /status/404 &
  elif [ $r -lt $((E503+8)) ];  then call  "bad key (401)" wrong-key relay '{"target":"httpbin","method":"GET","path":"/get"}' &
  elif [ $r -lt $((E503+11)) ]; then call  "out of scope (403)" "$KEY" relay '{"target":"prod-db","method":"GET","path":"/"}' &
  elif [ $r -lt $((E503+17)) ]; then call  "async webhook (202)" "$KEY" webhook '{"target":"httpbin","method":"POST","path":"/post","body":{"n":1}}' &
  elif [ $r -lt $((E503+25)) ]; then relay "slow upstream (1s)" /delay/1 &
  elif [ $r -lt $((E503+30)) ]; then stream &
  elif [ $r -lt $((E503+32)) ]; then call  "bad path (400)" "$KEY" relay '{"target":"httpbin","method":"GET","path":"no-slash"}' &
  else                               relay "relay ok" /get &
  fi
  if [ $BURST = 1 ] && [ $((SECONDS - last_burst)) -ge 60 ]; then
    echo "--- burst: 150 requests (expect 429s) ---"; last_burst=$SECONDS
    for _ in $(seq 150); do relay "burst" /get >/dev/null & done
  fi
  sleep "$(awk "BEGIN{print 1/$RATE}")"
done

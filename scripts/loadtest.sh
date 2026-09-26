#!/usr/bin/env bash
#
# loadtest.sh -- drive synthetic load at a running relayer to observe latency,
# throughput, and auto-scaling behaviour. Prefers `hey`, falls back to `k6`,
# then to a portable curl loop so it runs anywhere.
#
# Usage:
#   ./scripts/loadtest.sh [BASE_URL] [API_KEY] [REQUESTS] [CONCURRENCY] [TARGET]
#
# Examples:
#   ./scripts/loadtest.sh http://localhost:8080 "$API_KEY"
#   ./scripts/loadtest.sh https://relayer.example.com "$API_KEY" 5000 50 httpbin
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
API_KEY="${2:?usage: loadtest.sh [BASE_URL] API_KEY [REQUESTS] [CONCURRENCY] [TARGET]}"
REQUESTS="${3:-1000}"
CONCURRENCY="${4:-20}"
TARGET="${5:-httpbin}"

ENDPOINT="${BASE_URL%/}/api/v1/relay"
BODY=$(printf '{"target":"%s","method":"GET","path":"/get"}' "$TARGET")

echo "Load test -> $ENDPOINT"
echo "  target=$TARGET requests=$REQUESTS concurrency=$CONCURRENCY"
echo

if command -v hey >/dev/null 2>&1; then
  echo "Using hey..."
  exec hey -n "$REQUESTS" -c "$CONCURRENCY" \
    -m POST \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${API_KEY}" \
    -d "$BODY" \
    "$ENDPOINT"
fi

if command -v k6 >/dev/null 2>&1; then
  echo "Using k6..."
  K6_VUS="$CONCURRENCY" K6_ITERS="$REQUESTS" \
  ENDPOINT="$ENDPOINT" API_KEY="$API_KEY" BODY="$BODY" \
  k6 run --vus "$CONCURRENCY" --iterations "$REQUESTS" - <<'EOF'
import http from 'k6/http';
import { check } from 'k6';
const endpoint = __ENV.ENDPOINT;
const apiKey = __ENV.API_KEY;
const body = __ENV.BODY;
export default function () {
  const res = http.post(endpoint, body, {
    headers: { 'Content-Type': 'application/json', 'X-API-Key': apiKey },
  });
  check(res, { 'status is 200': (r) => r.status === 200 });
}
EOF
  exit 0
fi

echo "Neither 'hey' nor 'k6' found -- falling back to a sequential curl loop."
echo "Install hey (go install github.com/rakyll/hey@latest) or k6 for real load testing."
echo
start=$(date +%s)
ok=0
fail=0
for ((i = 1; i <= REQUESTS; i++)); do
  code=$(curl -s -o /dev/null -w '%{http_code}' \
    -X POST "$ENDPOINT" \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${API_KEY}" \
    -d "$BODY" || echo "000")
  if [[ "$code" == "200" ]]; then ok=$((ok + 1)); else fail=$((fail + 1)); fi
done
end=$(date +%s)
elapsed=$((end - start))
[[ "$elapsed" -eq 0 ]] && elapsed=1
echo "Done in ${elapsed}s -- ok=$ok fail=$fail rps=$((REQUESTS / elapsed))"

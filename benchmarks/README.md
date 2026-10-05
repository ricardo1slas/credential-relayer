# Benchmarks

Measures relay throughput/latency on the Raspberry Pi, isolated from the live stack.

```bash
brew install hey      # load generator
make bench            # or: DUR=30s ./benchmarks/run.sh
```

**Tailnet policy:** the load generator reaches the bench stack on `tcp:8081` (relayer), `tcp:8082`
(mock) and `tcp:9090` (pprof), which the deny-by-default policy in
[`docs/tailscale-policy.hujson`](../docs/tailscale-policy.hujson) does not allow. Add them to the
member -> `tag:pi` rule for the run and remove them afterwards; `run.sh` checks all three and exits
early if any is unreachable.

`run.sh` syncs the repo to the Pi, starts a throwaway stack (`docker-compose.bench.yml`:
relayer + redis + nginx mock upstream), runs the scenarios below, saves the output to
`results/<date>_<commit>.txt`, then tears the stack down. The live stack is never touched.

| Scenario | Shows |
|---|---|
| mock direct, c=50 | Ceiling: the upstream + network + load generator, no relayer |
| relay, c=50 | Steady-state relay throughput and latency |
| mock direct, c=200 | Ceiling at the overload concurrency, no relayer |
| relay, c=200 | Behaviour under overload (queuing, saturation) |

Every relay scenario has a direct baseline at the same concurrency and duration, and the run
ends with a **relayer overhead** block per concurrency level:

- **delta p50 / delta p95**: relay latency minus direct latency (ms).
- **throughput ratio**: relay req/s divided by direct req/s (1.00 = no throughput cost).

What is compared: the relay scenarios send `POST /api/v1/relay` with
`{"target":"mock","method":"GET","path":"/"}`; the relayer then does `GET /` on the mock over
the compose network. The direct scenarios do that same `GET /` straight to the mock's published
port (:8082), over the same tailnet path. So the delta is everything the relayer adds: auth,
the rate-limit check (a Redis round trip since v1.1.0), request/response handling, and the extra
upstream hop.

## Method / caveats
- Load comes from a Mac over Tailscale, so latency includes the tailnet hop (WireGuard) and
  the Mac's own limits. Compare relay vs. baseline rather than reading absolutes.
- `config.bench.yaml` sets the rate limit too high to trigger (it is still checked on every
  request) and relaxes SSRF guards (http + private IP) for the `mock` target only. Never use it outside this stack.
- Metrics + pprof are enabled in the bench stack (:9090) so profiles can be captured; overhead is small but nonzero.
- Results are one 20s run per scenario on a Raspberry Pi 4 (4 cores); expect run-to-run noise.

## Results (Pi 4, 4 cores, 2026-09-24)

| Scenario | req/s | p50 | p95 | p99 | errors |
|---|---|---|---|---|---|
| mock direct, c=50 (ceiling) | 1639 | 29 ms | 48 ms | 67 ms | 0 |
| relay, c=50 | 976 | 46 ms | 98 ms | 135 ms | 0 |
| relay, c=200 | 1048 | 168 ms | 409 ms | 580 ms | 0 |

No direct c=200 baseline was measured in that run.

### Relayer overhead

| Concurrency | delta p50 | delta p95 | throughput ratio |
|---|---|---|---|
| c=50 | TODO(<pi-user>): fill after make bench on the Pi | TODO(<pi-user>) | TODO(<pi-user>) |
| c=200 | TODO(<pi-user>): fill after make bench on the Pi | TODO(<pi-user>) | TODO(<pi-user>) |

Fill both rows from the `relayer overhead` block of the next `make bench` results file.

> **Not comparable across v1.1.0.** From v1.1.0 the rate limiter defaults to Redis, so every relay
> request makes one Redis round trip (the bench config doesn't set `rate_limit.backend`, so it
> measures that default). The 2026-09-24 numbers above predate it and used the in-memory limiter;
> compare new results only with other v1.1.0+ runs.

### Optimisation log: connection reuse (found via pprof)

Before: relay c=50 = 582 req/s, c=200 = **282 req/s** (throughput *fell* under load, p95 1.95 s).
The c=200 CPU profile (`results/before/`) had ~80% of samples in `Transport.dialConn`:
almost every relay opened a new TCP connection. Root cause: `doRelay` did
`defer cancel()` on the request context, but the response body is read *after* it returns;
cancelling mid-flight makes net/http discard the connection. Fix: cancel on `Body.Close()`
(plus a larger idle pool, `MaxIdleConnsPerHost` 10 -> 256). Regression test:
`TestRelayReusesConnections`.

After: c=50 = 976 req/s (+68%), c=200 = **1048 req/s (3.7x)**, p95 1.95 s -> 0.41 s.
Remaining CPU is network syscalls; relay c=50 is now ~60% of the direct ceiling.

Inspect a profile: `go tool pprof -top benchmarks/results/<file>.c200.pprof`.

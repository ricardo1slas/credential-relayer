# Benchmarks

Measures relay throughput/latency on the Raspberry Pi, isolated from the live stack.

```bash
brew install hey      # load generator
make bench            # or: DUR=30s ./benchmarks/run.sh
```

`run.sh` syncs the repo to the Pi, starts a throwaway stack (`docker-compose.bench.yml`:
relayer + redis + nginx mock upstream), runs the scenarios below, saves the output to
`results/<date>_<commit>.txt`, then tears the stack down. The live stack is never touched.

| Scenario | Shows |
|---|---|
| mock direct, c=50 | Ceiling: the upstream + network + load generator, no relayer |
| relay, c=50 | Steady-state relay throughput and latency |
| relay, c=200 | Behaviour under overload (queuing, saturation) |

## Method / caveats
- Load comes from a Mac over Tailscale, so latency includes the tailnet hop (WireGuard) and
  the Mac's own limits. Compare relay vs. baseline rather than reading absolutes.
- `config.bench.yaml` disables the rate limit and relaxes SSRF guards (http + private IP)
  for the `mock` target only. Never use it outside this stack.
- Metrics + pprof are enabled in the bench stack (:9090) so profiles can be captured; overhead is small but nonzero.
- Results are one 20s run per scenario on a Raspberry Pi 4 (4 cores); expect run-to-run noise.

## Results (Pi 4, 4 cores, 2026-09-24)

| Scenario | req/s | p50 | p95 | p99 | errors |
|---|---|---|---|---|---|
| mock direct, c=50 (ceiling) | 1639 | 29 ms | 48 ms | 67 ms | 0 |
| relay, c=50 | 976 | 46 ms | 98 ms | 135 ms | 0 |
| relay, c=200 | 1048 | 168 ms | 409 ms | 580 ms | 0 |

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

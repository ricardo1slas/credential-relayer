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
- Metrics are disabled in the bench stack to keep the measurement to the relay path.
- Results are one 20s run per scenario on a Raspberry Pi 4 (4 cores); expect run-to-run noise.

## Latest (Pi 4, 2026-09-24, commit 685f7f7)

| Scenario | req/s | p50 | p95 | p99 | errors |
|---|---|---|---|---|---|
| mock direct, c=50 | 1636 | 29 ms | 47 ms | 67 ms | 0 |
| relay, c=50 | 612 | 76 ms | 141 ms | 181 ms | 0 |
| relay, c=200 | 264 | 575 ms | 2.0 s | 2.9 s | 0 |

Reading: at c=50 the relayer adds ~50 ms p50 over the direct path and sustains ~600 req/s.
At c=200 throughput *falls* (CPU-bound on 4 cores, request queuing): the knee is somewhere
between 50 and 200 concurrent clients. Next: profile (pprof) to find where the time goes.

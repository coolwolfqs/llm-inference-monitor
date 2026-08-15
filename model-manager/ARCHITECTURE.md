# Runtime observability architecture

## Complexity contract

Runtime discovery is bounded by the number of processes owned by
`inference-server.service`, not the number of processes on the host. The normal
source is the unit's `cgroup.procs`; the bounded fallback reads systemd's
`MainPID`. Request handlers must not call `process_iter()` or enumerate
`/proc`.

## MECE responsibilities

1. **Acquisition** — model-manager's runtime observer is the only component
   that reads inference process argv. It samples cgroup-owned PIDs once per
   second.
2. **Caching** — synchronous consumers use single-flight TTL caches. Concurrent
   misses run one loader and cache the stopped (`None`) state as real data.
3. **Snapshots** — dashboard consumes `/api/runtime` for live state and
   `/api/models` for the slower catalog. These freshness classes are not mixed.
4. **Events** — model-manager has one producer and an asyncio condition that
   fans state changes out to all SSE clients. Client count does not multiply
   collection work.
5. **Transport** — dashboard streams model-manager SSE chunks end-to-end. It
   never buffers an unbounded response, and cancellation closes the upstream
   context.
6. **Control** — mutation routes retain serialized systemd changes and force a
   bounded runtime refresh for readiness and rollback checks.

## Release gates

- Zero `psutil.process_iter()` calls in dashboard request code.
- Zero host-wide `/proc` enumeration in model-manager.
- 100 concurrent runtime cache misses invoke one loader.
- SSE first byte through port 8081 is delivered within 100 ms on the LAN.
- Closing SSE clients returns dashboard-to-model-manager connections to the
  pre-test baseline.
- Runtime PID changes appear in `/api/runtime` and SSE within two polling
  intervals.
- API p95 remains below 250 ms during a TTL-boundary concurrency test.

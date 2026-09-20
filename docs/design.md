# Relay: initial design and verification plan

## Objective

Build a small, understandable reverse proxy whose correctness and failure behavior can be demonstrated. Optimize for explainable engineering decisions and reproducible evidence before adding features.

The implementation uses Go and its standard HTTP reverse proxy for protocol forwarding. The project implements routing, backend selection, health tracking, admission limits, and telemetry around that library. Reference: https://pkg.go.dev/net/http/httputil.

## Request lifecycle

1. Assign a proxy-generated request ID and establish a request deadline.
2. Match the longest configured path prefix at a path-segment boundary. `/api` matches `/api` and `/api/orders`, but not `/apiculture`. Preserve the path and valid query values; the forwarding library can normalize query encoding.
3. Acquire a concurrency permit without an unbounded waiting queue. Reject excess work with HTTP 503; reserve HTTP 429 for a future client rate-limiting policy.
4. Choose an eligible backend with round-robin selection. Return HTTP 503 if none are eligible.
5. Forward the method, body, and permitted headers using the HTTP library. Rebuild forwarding headers from the direct connection; do not trust client-provided forwarding metadata by default.
6. Stream the response, release the permit, and record outcome and duration on every exit path.

Initial forwarding failures produce HTTP 502; upstream deadline expiry before response headers produces HTTP 504. Once response headers have been sent, an error cannot replace the status code: terminate the stream and record the failure separately.

## Backend health

Each backend owns synchronized health state. Active probes have independent short deadlines and require configurable consecutive successes or failures before changing eligibility. Begin with backends unavailable until their first successful probe.

Passive transport failures can remove a backend from selection. Client cancellations must not mark a backend unhealthy. A failed health endpoint differs from an application returning a valid HTTP 500: document which signals change health rather than silently treating every error as an outage.

Probe workers must stop during shutdown, close response bodies, and avoid overlap for the same backend. Active probes provide the recovery path for backends excluded from normal traffic. Circuit breakers with half-open request admission are a separate potential extension.

## Retry policy

Do not introduce automatic application-level retries in the first milestone. A failure after a write reaches a backend leaves its outcome ambiguous. Later retries require an explicit policy for idempotency, replayable bodies, response state, a bounded attempt count, and a shared deadline. Document any underlying transport retry behavior separately.

## Resource and lifecycle limits

- Bound concurrent forwarded requests and configure HTTP connection pooling.
- Set client header, upstream dial, upstream response-header, and idle-connection timeouts deliberately.
- Propagate cancellation to the backend.
- Validate config at startup, including routes, backend URLs, duplicate entries, and positive limits.
- On termination, stop accepting new traffic, drain in-flight requests within a fixed grace period, cancel remaining work, and close idle upstream connections.
- Explicitly document supported streaming duration. WebSockets, CONNECT tunnels, and transparent HTTP/2 upgrades are outside the initial scope.

## Observability

Expose operational endpoints on a separate loopback-bound listener. Metrics use bounded labels such as route name, configured backend name, method class, and status class. Never use raw paths or request IDs as metric labels.

Access logs contain generated request ID, route, selected backend, status, duration, and outcome. Do not log authorization headers, cookies, bodies, or full query strings. Distinguish proxy liveness from readiness to route traffic.

## Verification

Integration tests should run real local backends and assert body, method, query, status, and header preservation; hop-by-hop header removal; forwarding-header spoof protection; route boundaries; round-robin behavior; all-backends-down behavior; slow upstream timeout; client cancellation; overload rejection; outage recovery; and graceful shutdown.

Use deterministic synchronization for concurrent tests instead of relying on arbitrary sleeps. Run the language's race detector or equivalent concurrency checks where available.

Benchmarks compare direct-backend traffic with proxied traffic under the same payload, connection-reuse policy, concurrency, duration, and host conditions. Include warm-up, multiple runs, latency percentiles, error rate, throughput, and resource consumption. Check for load-generator saturation. Keep raw results and commands; do not invent performance targets or resume numbers.

## Portfolio demo

1. Start Relay and three identifiable backends.
2. Send requests and show the distribution across backends.
3. Generate steady traffic and observe baseline latency and error rate.
4. Stop one backend and observe detection delay and routing changes.
5. Restart it and observe verified recovery.
6. Exceed the concurrency limit and demonstrate bounded work and explicit rejection.
7. Explain one measured tradeoff and one limitation.

The final case study should include the problem, architecture, decisions, failure evidence, measured performance, limitations, and exact reproduction commands. Resume bullets are written after those results exist.

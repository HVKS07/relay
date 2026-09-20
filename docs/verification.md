# Local verification

Verified on Windows amd64 using the project-local Go 1.27.1 toolchain.

- `go test -count=1 -timeout=60s ./...`: passed.
- `go vet ./...`: passed.
- Both executable builds: passed.
- `scripts/smoke.ps1`: passed initial distribution, failure exclusion, and recovery using the actual proxy and three demo backend processes.

The smoke demo exposed a selection bias when skipping an unhealthy backend. The cursor now advances past the selected backend, and a regression test checks alternating selection across the remaining healthy pool.

The integration suite covers HTTP forwarding, query values, header trust, request IDs, route boundaries, longest-prefix selection, round-robin selection, startup readiness gating, health thresholds, outage recovery, transport failure, timeouts, overload rejection, cancellation propagation, concurrent requests and metrics, probe worker cancellation, streaming, HTTP server draining, and invalid configuration.

`go test -race` could not run locally because the environment has no configured C compiler/cgo support. Linux CI is configured to run the race detector; no CI run has been observed yet. The HTTP server drain test exercises in-flight completion, not OS signal delivery or the full executable's forced-shutdown deadline.

These checks are correctness and functional smoke checks, not performance benchmarks. No throughput, latency improvement, uptime, or production-traffic claims have been established.

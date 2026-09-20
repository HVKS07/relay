# Presenting Relay

## Project description

Relay is a Go HTTP reverse proxy that routes requests across backend services, tracks their health, sheds excess concurrent work, and exposes operational metrics. It includes a local three-service demo and integration tests for forwarding correctness and failure behavior.

## Resume bullet supported by the implementation

Built a Go reverse proxy with path-based routing, round-robin load balancing, active health checks, bounded concurrency, and Prometheus-format metrics; tested upstream timeouts, outage recovery, cancellation propagation, and forwarding-header trust using local HTTP integration tests.

Add performance numbers only after recording a reproducible benchmark. Do not describe this as serving real users, improving an employer's systems, or achieving an uptime target without evidence.

## Demo narration

“A reverse proxy becomes interesting when dependencies fail. This demo runs three backends. Requests initially rotate across all three. I make one backend unhealthy, and after the configured probe threshold, Relay excludes it. When its health check succeeds again, it rejoins the pool. The detection window is deliberate: aggressive health checks react faster but cost traffic and can cause flapping. I also bound in-flight work so overload produces an explicit rejection instead of an unbounded queue.”

## LinkedIn draft

I’m building Relay, a reverse proxy in Go, to understand the engineering behind traffic routing and failure handling.

The current version includes round-robin load balancing, health checks, request deadlines, concurrency limits, and metrics. My favorite part so far is testing the failure paths: distinguishing client cancellation from backend failure, preserving forwarding-header trust, and handling a timeout after a response has already started.

Next I’m measuring proxy overhead against a direct-backend baseline and documenting the tradeoffs. I’ll share a failure demo and reproducible results with the repository.

## Release checklist

- Run CI and inspect the race detector result.
- Record the local failure demo.
- Produce direct-versus-proxy benchmark results, with hardware and commands.
- Include limitations and an architecture diagram in the public repository.
- Add the actual repository and demo links to the post and portfolio.

This draft has not been published. Hosting, benchmark results, and the recording are not yet included.

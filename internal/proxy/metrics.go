package proxy

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

var bounds = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type metrics struct {
	mu       sync.Mutex
	statuses [6]uint64
	buckets  [11]uint64
	count    uint64
	sum      float64
	failures uint64
}

func (m *metrics) record(status int, duration time.Duration, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	class := status / 100
	if class >= 1 && class <= 5 {
		m.statuses[class]++
	}
	m.count++
	m.sum += duration.Seconds()
	if outcome != "completed" {
		m.failures++
	}
	for i, b := range bounds {
		if duration.Seconds() <= b {
			m.buckets[i]++
		}
	}
}

func (p *Proxy) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, route := range p.routes {
			available := false
			for _, b := range route.backends {
				available = available || b.healthy.Load()
			}
			if !available {
				http.Error(w, "a route has no healthy backend", 503)
				return
			}
		}
		fmt.Fprintln(w, "ready")
	})
	mux.HandleFunc("GET /{$}", p.writeDashboard)
	mux.HandleFunc("GET /metrics", p.metricsPage)
	mux.HandleFunc("GET /metrics/raw", p.writeMetrics)
	mux.HandleFunc("GET /api/stats", p.writeStats)
	return mux
}

func (p *Proxy) writeMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	// Copy under the lock; a slow metrics client must not block request completion.
	p.metrics.mu.Lock()
	statuses, buckets, count, sum, failures := p.metrics.statuses, p.metrics.buckets, p.metrics.count, p.metrics.sum, p.metrics.failures
	p.metrics.mu.Unlock()
	fmt.Fprintln(w, "# HELP relay_requests_total Completed requests by HTTP status class.\n# TYPE relay_requests_total counter")
	for class := 1; class <= 5; class++ {
		fmt.Fprintf(w, "relay_requests_total{status_class=%q} %d\n", fmt.Sprintf("%dxx", class), statuses[class])
	}
	fmt.Fprintf(w, "# HELP relay_failures_total Requests with a non-completed proxy outcome.\n# TYPE relay_failures_total counter\nrelay_failures_total %d\n", failures)
	fmt.Fprintln(w, "# HELP relay_request_duration_seconds Request duration including response streaming.\n# TYPE relay_request_duration_seconds histogram")
	for i, b := range bounds {
		fmt.Fprintf(w, "relay_request_duration_seconds_bucket{le=%q} %d\n", fmt.Sprint(b), buckets[i])
	}
	fmt.Fprintf(w, "relay_request_duration_seconds_bucket{le=\"+Inf\"} %d\nrelay_request_duration_seconds_sum %g\nrelay_request_duration_seconds_count %d\n", count, sum, count)
	fmt.Fprintf(w, "# HELP relay_in_flight Current admitted requests.\n# TYPE relay_in_flight gauge\nrelay_in_flight %d\n", len(p.permits))
	fmt.Fprintln(w, "# HELP relay_backend_healthy Backend eligibility for traffic.\n# TYPE relay_backend_healthy gauge")
	for _, route := range p.routes {
		for _, b := range route.backends {
			healthy := 0
			if b.healthy.Load() {
				healthy = 1
			}
			fmt.Fprintf(w, "relay_backend_healthy{route=%q,backend=%q} %d\n", route.config.Name, b.config.Name, healthy)
		}
	}
}

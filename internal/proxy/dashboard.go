package proxy

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

//go:embed dashboard.html
var dashboardHTML string

// Browsers see the dashboard; scrapers retain the original text endpoint.
func (p *Proxy) metricsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Accept")
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		p.writeDashboard(w, r)
		return
	}
	p.writeMetrics(w, r)
}

func (p *Proxy) writeDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Write([]byte(dashboardHTML))
}

func (p *Proxy) writeStats(w http.ResponseWriter, r *http.Request) {
	p.stats(w, r, false)
}

func (p *Proxy) stats(w http.ResponseWriter, r *http.Request, public bool) {
	type backendView struct {
		Name    string `json:"name"`
		Route   string `json:"route"`
		Prefix  string `json:"prefix"`
		URL     string `json:"url"`
		Healthy bool   `json:"healthy"`
	}
	p.metrics.mu.Lock()
	count, sum, failures, statuses, buckets := p.metrics.count, p.metrics.sum, p.metrics.failures, p.metrics.statuses, p.metrics.buckets
	p.metrics.mu.Unlock()
	backends := make([]backendView, 0)
	for _, route := range p.routes {
		for _, b := range route.backends {
			origin := b.config.URL
			if public {
				origin = "Private upstream"
			}
			backends = append(backends, backendView{b.config.Name, route.config.Name, route.config.Prefix, origin, b.healthy.Load()})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		Requests    uint64        `json:"requests"`
		DurationSum float64       `json:"duration_sum_seconds"`
		Failures    uint64        `json:"failures"`
		Statuses    [6]uint64     `json:"statuses"`
		Buckets     [11]uint64    `json:"buckets"`
		Bounds      []float64     `json:"bounds_seconds"`
		InFlight    int           `json:"in_flight"`
		Capacity    int           `json:"capacity"`
		Backends    []backendView `json:"backends"`
	}{count, sum, failures, statuses, buckets, bounds, len(p.permits), p.config.MaxConcurrent, backends})
}

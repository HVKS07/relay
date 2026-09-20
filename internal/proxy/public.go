package proxy

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// The public demo accepts no visitor-selected upstreams, paths, bodies or delays.
// Limits are global per process, so spoofed forwarding headers cannot bypass them.
func (p *Proxy) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	page := strings.Replace(dashboardHTML, "<!--PUBLIC_DEMO-->", `<button id="send-demo" type="button">Send demo request</button><span id="demo-result" role="status" class="caption"></span>`, 1)
	show := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		w.Write([]byte(page))
	}
	mux.HandleFunc("GET /{$}", show)
	mux.HandleFunc("GET /metrics", show)
	mux.HandleFunc("GET /metrics/raw", p.writeMetrics)
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) { p.stats(w, r, true) })
	demoLimit := &tokenBucket{tokens: 4, burst: 4, rate: 2, last: time.Now()}
	slots := make(chan struct{}, 4)
	mux.HandleFunc("POST /demo/request", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			http.Error(w, "send an empty request without query parameters", 400)
			return
		}
		if !demoLimit.allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Demo is busy. Try again shortly.", 429)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Demo is busy. Try again shortly.", 429)
			return
		}
		// Construct a new request; never pass visitor cookies or authorization upstream.
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://relay/orders", nil)
		req.RemoteAddr = r.RemoteAddr
		p.ServeHTTP(w, req)
	})
	all := &tokenBucket{tokens: 60, burst: 60, rate: 30, last: time.Now()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if !all.allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Demo capacity reached. Try again shortly.", 429)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type tokenBucket struct {
	mu                  sync.Mutex
	tokens, burst, rate float64
	last                time.Time
}

func (b *tokenBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

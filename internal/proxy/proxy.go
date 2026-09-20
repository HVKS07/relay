package proxy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type backend struct {
	config              BackendConfig
	target              *url.URL
	healthy             atomic.Bool
	mu                  sync.Mutex
	successes, failures int
	forward             *httputil.ReverseProxy
}

type route struct {
	config   RouteConfig
	backends []*backend
	mu       sync.Mutex
	next     int
}

type Proxy struct {
	config      Config
	routes      []*route
	permits     chan struct{}
	transport   *http.Transport
	probeClient *http.Client
	logger      *slog.Logger
	metrics     metrics
}

type outcomeKey struct{}
type outcome struct{ value string }

func New(c Config, logger *slog.Logger) (*Proxy, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	t := &http.Transport{
		Proxy:        nil,
		DialContext:  (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 256, MaxIdleConnsPerHost: 32, MaxConnsPerHost: c.MaxConcurrent,
		IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 3 * time.Second,
		ResponseHeaderTimeout: time.Duration(c.RequestTimeoutMS) * time.Millisecond,
		ExpectContinueTimeout: time.Second,
	}
	pt := t.Clone()
	pt.MaxConnsPerHost = 1
	p := &Proxy{config: c, logger: logger, permits: make(chan struct{}, c.MaxConcurrent), transport: t,
		probeClient: &http.Client{Transport: pt, Timeout: time.Duration(c.HealthTimeoutMS) * time.Millisecond,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, rc := range c.Routes {
		r := &route{config: rc}
		for _, bc := range rc.Backends {
			u, _ := url.Parse(bc.URL)
			u.Path = ""
			b := &backend{config: bc, target: u}
			b.forward = &httputil.ReverseProxy{
				Transport: t,
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.SetURL(b.target)
					pr.SetXForwarded()
					pr.Out.Header.Set("X-Request-ID", pr.In.Header.Get("X-Request-ID"))
				},
				ModifyResponse: func(res *http.Response) error {
					res.Header.Set("X-Request-ID", res.Request.Header.Get("X-Request-ID"))
					return nil
				},
				ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
					o, _ := req.Context().Value(outcomeKey{}).(*outcome)
					status := http.StatusBadGateway
					deadline, hasDeadline := req.Context().Deadline()
					if hasDeadline && !time.Now().Before(deadline) {
						// Socket deadlines can fire before the context timer is scheduled.
						status = http.StatusGatewayTimeout
						o.value = "timeout"
					} else if req.Context().Err() != nil {
						if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
							status = http.StatusGatewayTimeout
							o.value = "timeout"
						} else {
							o.value = "cancelled"
						}
					} else {
						b.observe(false, c)
						o.value = "upstream_error"
						var ne net.Error
						if errors.As(err, &ne) && ne.Timeout() {
							status = http.StatusGatewayTimeout
							o.value = "timeout"
						}
					}
					http.Error(w, http.StatusText(status), status)
				},
			}
			r.backends = append(r.backends, b)
		}
		p.routes = append(p.routes, r)
	}
	sort.SliceStable(p.routes, func(i, j int) bool { return len(p.routes[i].config.Prefix) > len(p.routes[j].config.Prefix) })
	return p, nil
}

func (b *backend) observe(ok bool, c Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.failures = 0
		if b.successes < c.HealthSuccesses {
			b.successes++
		}
		if b.successes >= c.HealthSuccesses {
			b.healthy.Store(true)
		}
	} else {
		b.successes = 0
		if b.failures < c.HealthFailures {
			b.failures++
		}
		if b.failures >= c.HealthFailures {
			b.healthy.Store(false)
		}
	}
}

func (r *route) pick() *backend {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.backends {
		index := (r.next + i) % len(r.backends)
		b := r.backends[index]
		if b.healthy.Load() {
			r.next = (index + 1) % len(r.backends)
			return b
		}
	}
	return nil
}

// RunHealthChecks blocks until ctx is cancelled. Exactly one worker probes each backend.
func (p *Proxy) RunHealthChecks(ctx context.Context) {
	var wg sync.WaitGroup
	for _, r := range p.routes {
		for _, b := range r.backends {
			wg.Add(1)
			go func(b *backend) {
				defer wg.Done()
				ticker := time.NewTicker(time.Duration(p.config.HealthIntervalMS) * time.Millisecond)
				defer ticker.Stop()
				for {
					if ctx.Err() != nil {
						return
					}
					p.probe(ctx, b)
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}(b)
		}
	}
	wg.Wait()
}

func (p *Proxy) probe(ctx context.Context, b *backend) {
	u := *b.target
	u.Path = b.config.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		b.observe(false, p.config)
		return
	}
	res, err := p.probeClient.Do(req)
	ok := err == nil && res.StatusCode >= 200 && res.StatusCode < 300
	if res != nil {
		res.Body.Close()
	}
	if ctx.Err() == nil {
		b.observe(ok, p.config)
	}
}

func (p *Proxy) Close() {
	p.transport.CloseIdleConnections()
	p.probeClient.CloseIdleConnections()
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (w *recorder) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *recorder) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		http.Error(w, "request ID unavailable", 503)
		return
	}
	requestID := fmt.Sprintf("%x", id)
	w.Header().Set("X-Request-ID", requestID)
	rw := &recorder{ResponseWriter: w}
	routeName, backendName := "unmatched", "none"
	o := &outcome{value: "completed"}
	defer func() {
		panicked := recover()
		if panicked != nil {
			o.value = "stream_aborted"
		}
		status := rw.status
		if status == 0 {
			status = 500
		}
		p.metrics.record(status, time.Since(start), o.value)
		p.logger.Info("request", "request_id", requestID, "route", routeName, "backend", backendName, "status", status, "duration_ms", time.Since(start).Milliseconds(), "outcome", o.value)
		if panicked != nil {
			panic(panicked)
		}
	}()
	if req.Method == http.MethodConnect || req.Header.Get("Upgrade") != "" {
		o.value = "unsupported_protocol"
		http.Error(rw, "protocol upgrades and CONNECT are not supported", http.StatusNotImplemented)
		return
	}
	var selected *route
	for _, r := range p.routes {
		prefix := r.config.Prefix
		if prefix == "/" || req.URL.Path == prefix || strings.HasPrefix(req.URL.Path, prefix+"/") {
			selected = r
			break
		}
	}
	if selected == nil {
		http.NotFound(rw, req)
		return
	}
	routeName = selected.config.Name
	select {
	case p.permits <- struct{}{}:
		defer func() { <-p.permits }()
	default:
		o.value = "overloaded"
		http.Error(rw, "proxy at capacity", 503)
		return
	}
	b := selected.pick()
	if b == nil {
		o.value = "unavailable"
		http.Error(rw, "no healthy backends", 503)
		return
	}
	backendName = b.config.Name
	ctx, cancel := context.WithTimeout(req.Context(), time.Duration(p.config.RequestTimeoutMS)*time.Millisecond)
	defer cancel()
	// Socket deadlines also bound blocked downstream body reads and response writes.
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(rw)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline.Add(100 * time.Millisecond))
	defer controller.SetReadDeadline(time.Time{})
	defer controller.SetWriteDeadline(time.Time{})
	out := req.Clone(context.WithValue(ctx, outcomeKey{}, o))
	out.Header.Set("X-Request-ID", requestID)
	b.forward.ServeHTTP(rw, out)
}

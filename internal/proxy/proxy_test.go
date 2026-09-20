package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(urls ...string) Config {
	c := Config{Listen: "127.0.0.1:8080", AdminListen: "127.0.0.1:9090", MaxConcurrent: 8, RequestTimeoutMS: 1000, HealthIntervalMS: 10, HealthTimeoutMS: 100, HealthFailures: 2, HealthSuccesses: 1}
	r := RouteConfig{Name: "api", Prefix: "/api"}
	for i, u := range urls {
		r.Backends = append(r.Backends, BackendConfig{Name: string(rune('a' + i)), URL: u, HealthPath: "/healthz"})
	}
	c.Routes = []RouteConfig{r}
	return c
}

func newTestProxy(t *testing.T, c Config) *Proxy {
	t.Helper()
	p, err := New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func activate(p *Proxy) {
	for _, r := range p.routes {
		for _, b := range r.backends {
			b.healthy.Store(true)
		}
	}
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	r, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, string(body), r.Header
}

func TestForwardingAndHeaderTrust(t *testing.T) {
	type received struct{ Method, URI, Body, Forwarded, XFF, Hop, ID string }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Connection", "X-Internal")
		w.Header().Set("X-Internal", "secret")
		w.Header().Set("X-App", "preserved")
		w.Header().Set("X-Request-ID", "backend-controlled")
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(received{r.Method, r.RequestURI, string(body), r.Header.Get("Forwarded"), r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Hop"), r.Header.Get("X-Request-ID")})
	}))
	defer upstream.Close()
	p := newTestProxy(t, testConfig(upstream.URL))
	activate(p)
	s := httptest.NewServer(p)
	defer s.Close()
	req, _ := http.NewRequest("POST", s.URL+"/api/orders?q=a%20b", strings.NewReader("payload"))
	req.Header.Set("Forwarded", "for=evil")
	req.Header.Set("X-Forwarded-For", "spoofed")
	req.Header.Set("X-Request-ID", "client-controlled")
	req.Header.Set("Connection", "X-Hop, X-Request-ID")
	req.Header.Set("X-Hop", "secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got received
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	gotURL, err := url.ParseRequestURI(got.URI)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 || got.Method != "POST" || gotURL.Path != "/api/orders" || gotURL.Query().Get("q") != "a b" || got.Body != "payload" {
		t.Fatalf("forwarding: status=%d got=%+v", res.StatusCode, got)
	}
	if got.Forwarded != "" || got.XFF != "127.0.0.1" || got.Hop != "" || got.ID == "client-controlled" || len(got.ID) != 32 {
		t.Fatalf("header trust: %+v", got)
	}
	if res.Header.Get("X-Internal") != "" || res.Header.Get("X-App") != "preserved" || res.Header.Get("X-Request-ID") != got.ID {
		t.Fatalf("response headers: %v", res.Header)
	}
}

func TestRoutingAndRoundRobin(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "a") }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "b") }))
	defer b.Close()
	c := testConfig(a.URL, b.URL)
	c.Routes = append(c.Routes, RouteConfig{Name: "nested", Prefix: "/api/special", Backends: []BackendConfig{{Name: "b", URL: b.URL, HealthPath: "/healthz"}}})
	p := newTestProxy(t, c)
	activate(p)
	s := httptest.NewServer(p)
	defer s.Close()
	for _, want := range []string{"a", "b", "a", "b"} {
		status, body, _ := get(t, s.URL+"/api")
		if status != 200 || body != want {
			t.Fatalf("got %d %s, want %s", status, body, want)
		}
	}
	status, _, _ := get(t, s.URL+"/apiculture")
	if status != 404 {
		t.Fatalf("route boundary status=%d", status)
	}
	_, body, _ := get(t, s.URL+"/api/special/x")
	if body != "b" {
		t.Fatalf("longest prefix: %s", body)
	}
}

func TestStartupUnavailableAndHealthRecovery(t *testing.T) {
	var mu sync.Mutex
	healthStatus := 200
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(healthStatus)
	}))
	defer u.Close()
	p := newTestProxy(t, testConfig(u.URL))
	s := httptest.NewServer(p)
	defer s.Close()
	status, _, _ := get(t, s.URL+"/api")
	if status != 503 {
		t.Fatalf("unprobed status=%d", status)
	}
	b := p.routes[0].backends[0]
	p.probe(context.Background(), b)
	if !b.healthy.Load() {
		t.Fatal("successful probe did not activate")
	}
	mu.Lock()
	healthStatus = 503
	mu.Unlock()
	p.probe(context.Background(), b)
	if !b.healthy.Load() {
		t.Fatal("removed before failure threshold")
	}
	p.probe(context.Background(), b)
	if b.healthy.Load() {
		t.Fatal("backend not removed")
	}
	status, _, _ = get(t, s.URL+"/api")
	if status != 503 {
		t.Fatalf("outage status=%d", status)
	}
	mu.Lock()
	healthStatus = 200
	mu.Unlock()
	p.probe(context.Background(), b)
	status, _, _ = get(t, s.URL+"/api")
	if status != 200 {
		t.Fatalf("recovery status=%d", status)
	}
}

func TestTimeoutAndOverload(t *testing.T) {
	entered := make(chan struct{}, 1)
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	defer u.Close()
	c := testConfig(u.URL)
	c.MaxConcurrent = 1
	c.RequestTimeoutMS = 150
	p := newTestProxy(t, c)
	activate(p)
	s := httptest.NewServer(p)
	defer s.Close()
	result := make(chan int, 1)
	go func() {
		res, err := http.Get(s.URL + "/api")
		if err != nil {
			result <- 0
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		result <- res.StatusCode
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("backend not entered")
	}
	status, _, _ := get(t, s.URL+"/api")
	if status != 503 {
		t.Fatalf("overload status=%d", status)
	}
	select {
	case status = <-result:
		if status != 504 {
			t.Fatalf("timeout status=%d", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not bound request")
	}
	if len(p.permits) != 0 {
		t.Fatal("permit leak")
	}
}

func TestClientCancellationDoesNotPoisonBackend(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer u.Close()
	c := testConfig(u.URL)
	c.HealthFailures = 1
	p := newTestProxy(t, c)
	activate(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "http://relay/api", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.ServeHTTP(httptest.NewRecorder(), req) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("backend not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not cancel")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not cancel")
	}
	if !p.routes[0].backends[0].healthy.Load() || len(p.permits) != 0 {
		t.Fatal("cancellation poisoned health or leaked permit")
	}
}

func TestConcurrentDistributionAndMetrics(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer u.Close()
	c := testConfig(u.URL)
	c.MaxConcurrent = 64
	p := newTestProxy(t, c)
	activate(p)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			p.ServeHTTP(w, httptest.NewRequest("GET", "http://relay/api", nil))
			if w.Code != 204 {
				t.Errorf("status=%d", w.Code)
			}
		}()
	}
	wg.Wait()
	w := httptest.NewRecorder()
	p.AdminHandler().ServeHTTP(w, httptest.NewRequest("GET", "http://admin/metrics", nil))
	for _, want := range []string{"relay_requests_total{status_class=\"2xx\"} 40", "relay_request_duration_seconds_count 40", "relay_in_flight 0", "relay_backend_healthy{route=\"api\",backend=\"a\"} 1"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestProbeWorkersStop(t *testing.T) {
	entered := make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer u.Close()
	p := newTestProxy(t, testConfig(u.URL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.RunHealthChecks(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe worker leaked")
	}
}

func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero concurrency", func(c *Config) { c.MaxConcurrent = 0 }},
		{"public admin", func(c *Config) { c.AdminListen = "0.0.0.0:9090" }},
		{"backend credentials", func(c *Config) { c.Routes[0].Backends[0].URL = "http://user:password@localhost" }},
		{"backend path", func(c *Config) { c.Routes[0].Backends[0].URL = "http://localhost/prefix" }},
		{"route boundary", func(c *Config) { c.Routes[0].Prefix = "/api/" }},
		{"duplicate backend", func(c *Config) { c.Routes[0].Backends = append(c.Routes[0].Backends, c.Routes[0].Backends[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig("http://127.0.0.1:8081")
			tc.mutate(&c)
			if c.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestStreamingAndDrain(t *testing.T) {
	release := make(chan struct{})
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "second\n")
	}))
	defer u.Close()
	p := newTestProxy(t, testConfig(u.URL))
	activate(p)
	s := httptest.NewServer(p)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/api", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer res.Body.Close()
	first := make([]byte, 6)
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != "first\n" {
		close(release)
		t.Fatalf("first chunk: %q %v", first, err)
	}
	drained := make(chan error, 1)
	go func() { drained <- s.Config.Shutdown(ctx) }()
	close(release)
	rest, err := io.ReadAll(res.Body)
	if err != nil || string(rest) != "second\n" {
		t.Fatalf("stream did not drain: %q %v", rest, err)
	}
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if len(p.permits) != 0 {
		t.Fatal("stream leaked permit")
	}
}

func TestDeadBackendTransportFailure(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := testConfig(u.URL)
	c.HealthFailures = 1
	u.Close()
	p := newTestProxy(t, c)
	activate(p)
	s := httptest.NewServer(p)
	defer s.Close()
	status, _, _ := get(t, s.URL+"/api")
	if status != 502 {
		t.Fatalf("transport failure status=%d", status)
	}
	status, _, _ = get(t, s.URL+"/api")
	if status != 503 {
		t.Fatalf("dead backend was selected again: %d", status)
	}
}

func TestRoundRobinSkipsUnhealthyFairly(t *testing.T) {
	p := newTestProxy(t, testConfig("http://127.0.0.1:10001", "http://127.0.0.1:10002", "http://127.0.0.1:10003"))
	activate(p)
	r := p.routes[0]
	r.backends[1].healthy.Store(false)
	for i := 0; i < 30; i++ {
		want := "a"
		if i%2 == 1 {
			want = "c"
		}
		if got := r.pick().config.Name; got != want {
			t.Fatalf("selection %d = %s, want %s", i, got, want)
		}
	}
}

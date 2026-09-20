package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicIsolationAndFixedRequest(t *testing.T) {
	u := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/orders" || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("unsafe forwarded request: %v %v", r.URL, r.Header)
		}
		io.WriteString(w, `{"backend":"alpha"}`)
	}))
	defer u.Close()
	p := newTestProxy(t, testConfig(u.URL))
	p.routes[0].config.Prefix = "/"
	activate(p)
	h := p.PublicHandler()
	for _, path := range []string{"/demo/fail", "/demo/recover", "/orders", "/readyz", "/healthz"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("public %s = %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))
	if strings.Contains(w.Body.String(), u.URL) || !strings.Contains(w.Body.String(), "Private upstream") {
		t.Fatal("public stats leaked origin")
	}
	r := httptest.NewRequest("POST", "/demo/request", nil)
	r.Header.Set("Cookie", "secret")
	r.Header.Set("Authorization", "secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("demo request=%d", w.Code)
	}
	for _, target := range []string{"/demo/request?delay=30s", "/demo/request?url=http://example.com"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", target, nil))
		if w.Code != 400 {
			t.Fatal("query accepted")
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/demo/request", strings.NewReader("payload")))
	if w.Code != 400 {
		t.Fatal("body accepted")
	}
}

func TestPublicDemoRateLimit(t *testing.T) {
	p := newTestProxy(t, testConfig("http://127.0.0.1:10001"))
	h := p.PublicHandler()
	limited := false
	for i := 0; i < 8; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/demo/request", nil))
		if w.Code == 429 {
			limited = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry guidance")
			}
		}
	}
	if !limited {
		t.Fatal("demo limiter did not reject excess work")
	}
}

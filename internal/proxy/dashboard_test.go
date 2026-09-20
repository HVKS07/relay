package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardAndScraperCompatibility(t *testing.T) {
	p := newTestProxy(t, testConfig("http://127.0.0.1:8081"))
	for _, tc := range []struct{ path, accept, content string }{
		{"/", "", "text/html"},
		{"/metrics", "text/html,application/xhtml+xml", "text/html"},
		{"/metrics", "application/openmetrics-text,text/plain", "text/plain"},
		{"/metrics", "", "text/plain"},
		{"/metrics/raw", "text/html", "text/plain"},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Accept", tc.accept)
		w := httptest.NewRecorder()
		p.AdminHandler().ServeHTTP(w, r)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), tc.content) {
			t.Fatalf("%s Accept %s: %d %v", tc.path, tc.accept, w.Code, w.Header())
		}
	}
	unknown := httptest.NewRecorder()
	p.AdminHandler().ServeHTTP(unknown, httptest.NewRequest("GET", "/missing", nil))
	if unknown.Code != 404 {
		t.Fatalf("unknown path = %d", unknown.Code)
	}
}

func TestStatsSnapshot(t *testing.T) {
	p := newTestProxy(t, testConfig("http://127.0.0.1:8081"))
	activate(p)
	p.metrics.record(200, 100*time.Millisecond, "completed")
	p.metrics.record(502, 300*time.Millisecond, "upstream_error")
	w := httptest.NewRecorder()
	p.AdminHandler().ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))
	var got struct {
		Requests int     `json:"requests"`
		Sum      float64 `json:"duration_sum_seconds"`
		Failures int     `json:"failures"`
		Statuses [6]int  `json:"statuses"`
		Backends []struct {
			Healthy bool `json:"healthy"`
		} `json:"backends"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Requests != 2 || got.Sum != 0.4 || got.Failures != 1 || got.Statuses[2] != 1 || got.Statuses[5] != 1 || len(got.Backends) != 1 || !got.Backends[0].Healthy {
		t.Fatalf("bad snapshot: %+v", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("stats must not be cached")
	}
}

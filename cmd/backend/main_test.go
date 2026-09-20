package main

import (
	"net/http/httptest"
	"testing"
)

func TestControlsDisabledByDefault(t *testing.T) {
	h := newHandler("alpha", false)
	for _, path := range []string{"/demo/fail", "/demo/recover"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 404 {
			t.Fatalf("%s = %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal("disabled controls changed health")
	}
}

func TestLocalControlsOptIn(t *testing.T) {
	h := newHandler("alpha", true)
	for _, tc := range []struct {
		path   string
		health int
	}{{"/demo/fail", 503}, {"/demo/recover", 200}} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", tc.path, nil))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != tc.health {
			t.Fatalf("after %s health=%d", tc.path, w.Code)
		}
	}
}

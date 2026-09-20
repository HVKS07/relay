// backend is a local demo service. Never deploy its control endpoints publicly.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "loopback listen address")
	name := flag.String("name", "alpha", "backend identity")
	flag.Parse()
	var unhealthy atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if unhealthy.Load() {
			http.Error(w, "unhealthy", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /demo/fail", func(w http.ResponseWriter, r *http.Request) {
		unhealthy.Store(true)
		w.Write([]byte("backend unhealthy\n"))
	})
	mux.HandleFunc("POST /demo/recover", func(w http.ResponseWriter, r *http.Request) {
		unhealthy.Store(false)
		w.Write([]byte("backend healthy\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if unhealthy.Load() {
			http.Error(w, "demo backend unavailable", 503)
			return
		}
		if raw := r.URL.Query().Get("delay"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d < 0 || d > 30*time.Second {
				http.Error(w, "delay must be between 0s and 30s", 400)
				return
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend", *name)
		json.NewEncoder(w).Encode(map[string]string{"backend": *name, "method": r.Method, "path": r.URL.Path, "request_id": r.Header.Get("X-Request-ID")})
	})
	log.Printf("demo backend %s on %s", *name, *listen)
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}

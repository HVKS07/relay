package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
)

type BackendConfig struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	HealthPath string `json:"health_path"`
}

type RouteConfig struct {
	Name     string          `json:"name"`
	Prefix   string          `json:"prefix"`
	Backends []BackendConfig `json:"backends"`
}

type Config struct {
	Listen           string        `json:"listen"`
	AdminListen      string        `json:"admin_listen"`
	MaxConcurrent    int           `json:"max_concurrent"`
	RequestTimeoutMS int           `json:"request_timeout_ms"`
	HealthIntervalMS int           `json:"health_interval_ms"`
	HealthTimeoutMS  int           `json:"health_timeout_ms"`
	HealthFailures   int           `json:"health_failures"`
	HealthSuccesses  int           `json:"health_successes"`
	Routes           []RouteConfig `json:"routes"`
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	var c Config
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("config must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	for _, addr := range []string{c.Listen, c.AdminListen} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("invalid listen address %q: %w", addr, err)
		}
	}
	host, _, _ := net.SplitHostPort(c.AdminListen)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("admin_listen must use a loopback IP")
	}
	if c.Listen == c.AdminListen {
		return fmt.Errorf("data and admin listeners must differ")
	}
	for name, n := range map[string]int{"max_concurrent": c.MaxConcurrent, "request_timeout_ms": c.RequestTimeoutMS, "health_interval_ms": c.HealthIntervalMS, "health_timeout_ms": c.HealthTimeoutMS, "health_failures": c.HealthFailures, "health_successes": c.HealthSuccesses} {
		if n < 1 || n > 86400000 {
			return fmt.Errorf("%s must be between 1 and 86400000", name)
		}
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("at least one route is required")
	}
	names, prefixes := map[string]bool{}, map[string]bool{}
	for _, r := range c.Routes {
		if r.Name == "" || names[r.Name] {
			return fmt.Errorf("route names must be nonempty and unique")
		}
		names[r.Name] = true
		if !strings.HasPrefix(r.Prefix, "/") || strings.ContainsAny(r.Prefix, "?#%") || (len(r.Prefix) > 1 && strings.HasSuffix(r.Prefix, "/")) || prefixes[r.Prefix] {
			return fmt.Errorf("invalid or duplicate route prefix %q", r.Prefix)
		}
		prefixes[r.Prefix] = true
		if len(r.Backends) == 0 {
			return fmt.Errorf("route %s has no backends", r.Name)
		}
		bn, bu := map[string]bool{}, map[string]bool{}
		for _, b := range r.Backends {
			u, err := url.Parse(b.URL)
			if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return fmt.Errorf("backend %q requires an http(s) origin URL", b.Name)
			}
			if b.Name == "" || bn[b.Name] || bu[strings.TrimSuffix(b.URL, "/")] {
				return fmt.Errorf("backend names and URLs must be unique within route %s", r.Name)
			}
			bn[b.Name], bu[strings.TrimSuffix(b.URL, "/")] = true, true
			if !strings.HasPrefix(b.HealthPath, "/") || strings.HasPrefix(b.HealthPath, "//") || strings.ContainsAny(b.HealthPath, "?#") {
				return fmt.Errorf("backend %s requires an absolute health_path without query or fragment", b.Name)
			}
		}
	}
	return nil
}

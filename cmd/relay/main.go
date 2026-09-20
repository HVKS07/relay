package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"relay/internal/proxy"
)

func main() {
	configPath := flag.String("config", "config/relay.json", "JSON configuration path")
	healthcheck := flag.Bool("healthcheck", false, "check local admin readiness and exit")
	flag.Parse()
	if *healthcheck {
		c, err := proxy.LoadConfig(*configPath)
		if err != nil {
			os.Exit(1)
		}
		client := http.Client{Timeout: 2 * time.Second}
		res, err := client.Get("http://" + c.AdminListen + "/readyz")
		if err != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*configPath, logger); err != nil {
		logger.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func run(path string, logger *slog.Logger) error {
	c, err := proxy.LoadConfig(path)
	if err != nil {
		return err
	}
	p, err := proxy.New(c, logger)
	if err != nil {
		return err
	}
	defer p.Close()
	dataListener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	defer dataListener.Close()
	adminListener, err := net.Listen("tcp", c.AdminListen)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	var publicListener net.Listener
	if c.PublicListen != "" {
		publicListener, err = net.Listen("tcp", c.PublicListen)
		if err != nil {
			return err
		}
		defer publicListener.Close()
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	probeCtx, cancelProbes := context.WithCancel(context.Background())
	defer cancelProbes()
	probesDone := make(chan struct{})
	go func() { defer close(probesDone); p.RunHealthChecks(probeCtx) }()
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	data := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return requestCtx }}
	admin := &http.Server{Handler: p.AdminHandler(), ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	servers := []*http.Server{data, admin}
	errorsCh := make(chan error, 3)
	if publicListener != nil {
		public := &http.Server{Handler: p.PublicHandler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return requestCtx }}
		servers = append(servers, public)
		go func() { errorsCh <- public.Serve(publicListener) }()
	}
	go func() { errorsCh <- data.Serve(dataListener) }()
	go func() { errorsCh <- admin.Serve(adminListener) }()
	logger.Info("listening", "data", c.Listen, "admin", c.AdminListen, "public", c.PublicListen)
	select {
	case <-signalCtx.Done():
	case err = <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	logger.Info("draining", "grace_seconds", 10)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, server := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if e := s.Shutdown(shutdownCtx); e != nil {
				cancelRequests()
				_ = s.Close()
			}
		}(server)
	}
	wg.Wait()
	cancelProbes()
	<-probesDone
	return err
}

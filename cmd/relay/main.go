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
	flag.Parse()
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
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- data.Serve(dataListener) }()
	go func() { errorsCh <- admin.Serve(adminListener) }()
	logger.Info("listening", "data", c.Listen, "admin", c.AdminListen)
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
	for _, server := range []*http.Server{data, admin} {
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

package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/fjbender/zvt-to-mollie/config"
	"github.com/fjbender/zvt-to-mollie/internal/mollie"
	"github.com/fjbender/zvt-to-mollie/internal/store"
	"github.com/fjbender/zvt-to-mollie/internal/zvt"
)

func main() {
	verbose := flag.Bool("verbose", false, "log raw ZVT and Mollie API messages at debug level")
	flag.Parse()

	// Structured JSON logger; debug level only when -verbose is set.
	var logLevel slog.LevelVar
	if *verbose {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &logLevel})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.StateDBPath)
	if err != nil {
		slog.Error("failed to open state store", "path", cfg.StateDBPath, "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := st.Close(); err != nil {
			slog.Error("failed to close state store", "err", err)
		}
	}()

	mollieClient := mollie.NewClient(cfg.MollieAPIKey, cfg.MollieTerminalID, cfg.MollieAPITimeout, *verbose)
	dispatcher := zvt.NewDispatcher(mollieClient, st, cfg.ZVTPassword, cfg.ZVTCurrencyCode)
	listener := zvt.NewListener(cfg.ZVTListenAddr, dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// HTTP health/readiness server.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Store is open at this point (we'd have exited otherwise).
		w.WriteHeader(http.StatusOK)
	})
	httpServer := &http.Server{Addr: cfg.HTTPListenAddr, Handler: mux}

	wg.Add(1)
	go func() {
		defer wg.Done()
		slog.Info("http server started", "addr", cfg.HTTPListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	// ZVT TCP listener.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := listener.Listen(ctx); err != nil {
			slog.Error("zvt listener error", "err", err)
			cancel()
		}
	}()

	// Block until SIGINT or SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("shutting down", "signal", sig.String())

	cancel()
	_ = httpServer.Shutdown(context.Background())
	wg.Wait()
	slog.Info("shutdown complete")
}

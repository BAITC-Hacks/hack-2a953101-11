package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/electrokomplekt/replenishment/internal/httpapi"
	"github.com/electrokomplekt/replenishment/internal/storage"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run(ctx context.Context, logger *slog.Logger) error {
	address := env("HTTP_ADDR", "127.0.0.1:8080")
	key := os.Getenv("API_KEY")
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid HTTP_ADDR: %w", err)
	}
	ip := net.ParseIP(host)
	if key == "" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("API_KEY is required when HTTP_ADDR is not a loopback IP")
	}
	if key != "" && (len(key) < 24 || strings.TrimSpace(key) != key) {
		return errors.New("API_KEY must be at least 24 characters without surrounding whitespace")
	}
	store, err := storage.Open(env("DATA_FILE", "data/warehouse.json"))
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              address,
		Handler:           httpapi.New(store, logger, key, os.Getenv("CORS_ORIGIN")),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	logger.Info("backend listening", "address", listener.Addr().String())
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger); err != nil {
		logger.Error("backend stopped", "error", err)
		os.Exit(1)
	}
}

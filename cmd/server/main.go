package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/qishuhua/hikvision-isapi-snapshot/internal/httpapi"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/scheduler"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/service"
	"github.com/qishuhua/hikvision-isapi-snapshot/internal/store"
)

func getenv(k, fallback string) string {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	return v
}
func main() {
	key := os.Getenv("DATA_ENCRYPTION_KEY")
	if key == "" {
		slog.Error("missing DATA_ENCRYPTION_KEY")
		os.Exit(1)
	}
	db, err := store.New(getenv("DATA_PATH", "./data/state.json"), key)
	if err != nil {
		slog.Error("open state failed", "err", err)
		os.Exit(1)
	}
	max, _ := strconv.Atoi(getenv("MAX_CONCURRENT_CAPTURES", "4"))
	if max < 1 || max > 128 {
		max = 4
	}
	svc := service.New(db, max)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go scheduler.New(svc, max).Run(ctx)
	server := &http.Server{
		Addr: getenv("HTTP_ADDR", "127.0.0.1:8080"), Handler: httpapi.New(svc),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	slog.Info("ISAPI snapshot server running", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server stopped", "err", err)
		os.Exit(1)
	}
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/api"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/payment"
	"github.com/umars28/marspay/internal/txn"
	"github.com/umars28/marspay/internal/wallet"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := env("MARSPAY_ADDR", ":8080")
	dsn := env("MARSPAY_DATABASE_URL", "")
	redisAddr := env("MARSPAY_REDIS_ADDR", "127.0.0.1:6379")

	if dsn == "" {
		return errors.New("MARSPAY_DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer func() { _ = rdb.Close() }()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	ledgerRepo := ledger.NewRepo(pool)
	balances := wallet.NewRedis(rdb, "marspay:", 12*time.Hour)

	router := api.NewRouter(api.Deps{
		Payments:    payment.NewService(pool, ledgerRepo, balances),
		Txn:         txn.NewService(pool, ledgerRepo, balances),
		Idempotency: idempotency.NewStore(pool),
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

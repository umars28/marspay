package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/outbox"
	"github.com/umars28/marspay/internal/webhook"
	"github.com/umars28/marspay/internal/worker"
)

type workerConfig struct {
	outboxPublisher outbox.Publisher
	outboxInterval  time.Duration
	webhookInterval time.Duration
	webhookBatch    int
	logger          *slog.Logger
}

func startWorkers(ctx context.Context, pool *pgxpool.Pool, cfg workerConfig) (wait func()) {
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	var loops []worker.Loop

	if cfg.outboxPublisher == nil {
		logger.Warn("outbox relay disabled: MARSPAY_KAFKA_BROKERS is not set, events stay in the outbox table")
	} else {
		relay := outbox.NewRelay(pool, cfg.outboxPublisher)
		loops = append(loops, worker.Loop{
			Name:     "outbox-relay",
			Interval: cfg.outboxInterval,
			Logger:   logger,
			Step:     relay.Sweep,
		})
	}

	dispatcher := webhook.NewDispatcher(pool)
	batch := cfg.webhookBatch
	loops = append(loops, worker.Loop{
		Name:     "webhook-dispatcher",
		Interval: cfg.webhookInterval,
		Logger:   logger,
		Step: func(ctx context.Context) (int, error) {
			attempts, err := dispatcher.DispatchBatch(ctx, batch)
			for _, a := range attempts {
				if a.Status != webhook.StatusDelivered {
					logger.Warn("webhook not delivered",
						"delivery_id", a.DeliveryID,
						"attempt", a.Attempt,
						"status", a.Status,
						"response_code", a.ResponseCode,
						"error", a.Err)
				}
			}
			return len(attempts), err
		},
	})

	var wg sync.WaitGroup
	for _, l := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Run(ctx)
		}()
	}
	return wg.Wait
}

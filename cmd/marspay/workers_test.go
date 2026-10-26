package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/outbox"
	"github.com/umars28/marspay/internal/testdb"
	"github.com/umars28/marspay/internal/webhook"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writeOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i := 0; i < n; i++ {
		err := outbox.Write(ctx, tx, outbox.Message{
			Topic:        outbox.TopicPaymentEvents,
			PartitionKey: id.New("merch"),
			EventType:    "payment.succeeded",
			Payload:      []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("write outbox: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestWorkersDrainTheOutbox(t *testing.T) {
	pool, ctx := testdb.New(t)
	writeOutbox(t, ctx, pool, 250)

	pub := outbox.NewMemoryPublisher()
	relay := outbox.NewRelay(pool, pub)
	runCtx, stop := context.WithCancel(ctx)
	wait := startWorkers(runCtx, pool, workerConfig{
		outboxPublisher: pub,
		outboxInterval:  50 * time.Millisecond,
		webhookInterval: time.Hour,
		webhookBatch:    10,
		logger:          quietLogger,
	})
	defer func() { stop(); wait() }()

	eventually(t, "the outbox to drain", func() bool {
		n, err := relay.Pending(ctx)
		return err == nil && n == 0
	})
	if got := len(pub.Published()); got != 250 {
		t.Fatalf("published %d messages, want 250", got)
	}

	writeOutbox(t, ctx, pool, 3)
	eventually(t, "messages written after start-up to be published", func() bool {
		return len(pub.Published()) == 253
	})
}

func TestWorkersDeliverQueuedWebhooks(t *testing.T) {
	pool, ctx := testdb.New(t)

	var hits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)

	merchantID := id.New("merch")
	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status)
		 VALUES ($1, 'PT Worker', 'Worker Merchant', 'food', 'active')`, merchantID); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO webhook_endpoints (id, merchant_id, url, signing_secret, events, timeout_ms)
		 VALUES ($1, $2, $3, 'whsec_worker_test', ARRAY['payment.succeeded'], 2000)`,
		id.New("wep"), merchantID, receiver.URL); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}

	dispatcher := webhook.NewDispatcher(pool)
	for i := 0; i < 3; i++ {
		if _, err := dispatcher.Enqueue(ctx, webhook.Event{
			MerchantID: merchantID,
			Type:       "payment.succeeded",
			ResourceID: id.New("pay"),
			Payload:    []byte(`{"ok":true}`),
		}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	runCtx, stop := context.WithCancel(ctx)
	wait := startWorkers(runCtx, pool, workerConfig{
		webhookInterval: 50 * time.Millisecond,
		webhookBatch:    10,
		logger:          quietLogger,
	})
	defer func() { stop(); wait() }()

	eventually(t, "three webhooks delivered", func() bool {
		var delivered int
		err := pool.QueryRow(ctx,
			`SELECT count(*) FROM webhook_deliveries WHERE status = $1`, webhook.StatusDelivered).Scan(&delivered)
		return err == nil && delivered == 3
	})
	if got := hits.Load(); got != 3 {
		t.Fatalf("receiver saw %d requests, want 3", got)
	}
}

func TestWorkersStopWhenTheContextEnds(t *testing.T) {
	pool, ctx := testdb.New(t)
	runCtx, stop := context.WithCancel(ctx)
	wait := startWorkers(runCtx, pool, workerConfig{
		outboxPublisher: outbox.NewMemoryPublisher(),
		outboxInterval:  time.Hour,
		webhookInterval: time.Hour,
		webhookBatch:    10,
		logger:          quietLogger,
	})

	stop()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not stop after the context was cancelled")
	}
}

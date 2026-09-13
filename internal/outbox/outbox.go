package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TopicPaymentEvents   = "payment.events"
	TopicPaymentAttempts = "payment.attempts"
	TopicLedgerEntries   = "ledger.entries"
	TopicPayoutEvents    = "payout.events"
)

var ErrNoPartitionKey = errors.New("outbox: a message needs a partition key")

type Message struct {
	ID           int64
	Topic        string
	PartitionKey string
	EventType    string
	Payload      []byte
}

type Publisher interface {
	Publish(ctx context.Context, msgs []Message) error
}

func Write(ctx context.Context, tx pgx.Tx, msgs ...Message) error {
	for _, m := range msgs {
		if m.PartitionKey == "" {
			return ErrNoPartitionKey
		}
		if m.Topic == "" || m.EventType == "" {
			return fmt.Errorf("outbox: message needs a topic and an event type")
		}

		_, err := tx.Exec(ctx,
			`INSERT INTO outbox (topic, partition_key, event_type, payload)
			 VALUES ($1, $2, $3, $4)`,
			m.Topic, m.PartitionKey, m.EventType, m.Payload)
		if err != nil {
			return fmt.Errorf("outbox: write %s: %w", m.EventType, err)
		}
	}
	return nil
}

type Relay struct {
	pool      *pgxpool.Pool
	publisher Publisher
	batchSize int
	now       func() time.Time
}

func NewRelay(pool *pgxpool.Pool, publisher Publisher) *Relay {
	return &Relay{pool: pool, publisher: publisher, batchSize: 100, now: time.Now}
}

func (r *Relay) Sweep(ctx context.Context) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("outbox: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	msgs, err := r.claim(ctx, tx)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, tx.Commit(ctx)
	}

	if err := r.publisher.Publish(ctx, msgs); err != nil {
		if markErr := r.markFailed(ctx, tx, msgs, err); markErr != nil {
			return 0, markErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return 0, commitErr
		}
		return 0, fmt.Errorf("outbox: publish: %w", err)
	}

	if err := r.markPublished(ctx, tx, msgs); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("outbox: commit: %w", err)
	}
	return len(msgs), nil
}

func (r *Relay) claim(ctx context.Context, tx pgx.Tx) ([]Message, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, topic, partition_key, event_type, payload
		 FROM (
		   SELECT id, topic, partition_key, event_type, payload
		   FROM outbox
		   WHERE published_at IS NULL
		   ORDER BY id
		   LIMIT $1
		 ) candidates
		 WHERE pg_try_advisory_xact_lock(hashtext(partition_key))
		 ORDER BY id`,
		r.batchSize*4)
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Topic, &m.PartitionKey, &m.EventType, &m.Payload); err != nil {
			return nil, fmt.Errorf("outbox: scan: %w", err)
		}
		out = append(out, m)
		if len(out) == r.batchSize {
			break
		}
	}
	return out, rows.Err()
}

func (r *Relay) markPublished(ctx context.Context, tx pgx.Tx, msgs []Message) error {
	ids := idsOf(msgs)
	_, err := tx.Exec(ctx,
		`UPDATE outbox SET published_at = $1, attempts = attempts + 1, last_error = NULL
		 WHERE id = ANY($2)`,
		r.now(), ids)
	if err != nil {
		return fmt.Errorf("outbox: mark published: %w", err)
	}
	return nil
}

func (r *Relay) markFailed(ctx context.Context, tx pgx.Tx, msgs []Message, cause error) error {
	ids := idsOf(msgs)
	_, err := tx.Exec(ctx,
		`UPDATE outbox SET attempts = attempts + 1, last_error = $1 WHERE id = ANY($2)`,
		cause.Error(), ids)
	if err != nil {
		return fmt.Errorf("outbox: mark failed: %w", err)
	}
	return nil
}

func idsOf(msgs []Message) []int64 {
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func (r *Relay) Pending(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("outbox: pending: %w", err)
	}
	return n, nil
}

func (r *Relay) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM outbox
		 WHERE published_at IS NOT NULL AND published_at < $1`,
		r.now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("outbox: prune: %w", err)
	}
	return tag.RowsAffected(), nil
}

type MemoryPublisher struct {
	mu       sync.Mutex
	messages []Message
	failNext error
}

func NewMemoryPublisher() *MemoryPublisher {
	return &MemoryPublisher{}
}

func (p *MemoryPublisher) Publish(_ context.Context, msgs []Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.failNext != nil {
		err := p.failNext
		p.failNext = nil
		return err
	}
	p.messages = append(p.messages, msgs...)
	return nil
}

func (p *MemoryPublisher) FailNext(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failNext = err
}

func (p *MemoryPublisher) Published() []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Message, len(p.messages))
	copy(out, p.messages)
	return out
}

func (p *MemoryPublisher) PublishedFor(key string) []Message {
	var out []Message
	for _, m := range p.Published() {
		if m.PartitionKey == key {
			out = append(out, m)
		}
	}
	return out
}

package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
)

type Event struct {
	ID         string
	MerchantID string
	Type       string
	ResourceID string
	Payload    []byte
}

type Attempt struct {
	DeliveryID   string
	EventID      string
	EndpointID   string
	URL          string
	Attempt      int
	Status       string
	ResponseCode int
	Latency      time.Duration
	Err          error
}

type Dispatcher struct {
	pool   *pgxpool.Pool
	client *http.Client
	now    func() time.Time
}

func NewDispatcher(pool *pgxpool.Pool) *Dispatcher {
	return &Dispatcher{
		pool:   pool,
		client: &http.Client{},
		now:    time.Now,
	}
}

func (d *Dispatcher) Enqueue(ctx context.Context, ev Event) (string, error) {
	if ev.ID == "" {
		ev.ID = id.New("evt")
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("webhook: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`INSERT INTO webhook_events (id, merchant_id, type, resource_id, payload)
		 VALUES ($1, $2, $3, $4, $5)`,
		ev.ID, ev.MerchantID, ev.Type, ev.ResourceID, ev.Payload)
	if err != nil {
		return "", fmt.Errorf("webhook: insert event: %w", err)
	}

	rows, err := tx.Query(ctx,
		`SELECT id FROM webhook_endpoints
		 WHERE merchant_id = $1 AND disabled_at IS NULL AND $2 = ANY(events)`,
		ev.MerchantID, ev.Type)
	if err != nil {
		return "", fmt.Errorf("webhook: load endpoints: %w", err)
	}

	var endpointIDs []string
	for rows.Next() {
		var epID string
		if err := rows.Scan(&epID); err != nil {
			rows.Close()
			return "", fmt.Errorf("webhook: scan endpoint: %w", err)
		}
		endpointIDs = append(endpointIDs, epID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}

	for _, epID := range endpointIDs {
		_, err = tx.Exec(ctx,
			`INSERT INTO webhook_deliveries (id, event_id, endpoint_id, status, next_retry_at)
			 VALUES ($1, $2, $3, $4, now())
			 ON CONFLICT (event_id, endpoint_id) DO NOTHING`,
			id.New("whd"), ev.ID, epID, StatusPending)
		if err != nil {
			return "", fmt.Errorf("webhook: queue delivery: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("webhook: commit: %w", err)
	}
	return ev.ID, nil
}

type claim struct {
	deliveryID string
	eventID    string
	endpointID string
	attempt    int
	url        string
	secret     string
	timeoutMs  int
	payload    []byte
}

func (d *Dispatcher) DispatchBatch(ctx context.Context, limit int) ([]Attempt, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claims, err := d.claimDue(ctx, tx, limit)
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, tx.Commit(ctx)
	}

	attempts := make([]Attempt, 0, len(claims))
	for _, c := range claims {
		attempts = append(attempts, d.deliver(ctx, tx, c))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("webhook: commit: %w", err)
	}
	return attempts, nil
}

func (d *Dispatcher) claimDue(ctx context.Context, tx pgx.Tx, limit int) ([]claim, error) {
	rows, err := tx.Query(ctx,
		`SELECT d.id, d.event_id, d.endpoint_id, d.attempt,
		        ep.url, ep.signing_secret, ep.timeout_ms, e.payload
		 FROM webhook_deliveries d
		 JOIN webhook_events e    ON e.id  = d.event_id
		 JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
		 WHERE d.status IN ($1, $2)
		   AND (d.next_retry_at IS NULL OR d.next_retry_at <= now())
		 ORDER BY d.created_at
		 LIMIT $3
		 FOR UPDATE OF d SKIP LOCKED`,
		StatusPending, StatusRetrying, limit)
	if err != nil {
		return nil, fmt.Errorf("webhook: claim due: %w", err)
	}
	defer rows.Close()

	var out []claim
	for rows.Next() {
		var c claim
		if err := rows.Scan(&c.deliveryID, &c.eventID, &c.endpointID, &c.attempt,
			&c.url, &c.secret, &c.timeoutMs, &c.payload); err != nil {
			return nil, fmt.Errorf("webhook: scan claim: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *Dispatcher) deliver(ctx context.Context, tx pgx.Tx, c claim) Attempt {
	attemptNo := c.attempt + 1
	result := Attempt{
		DeliveryID: c.deliveryID,
		EventID:    c.eventID,
		EndpointID: c.endpointID,
		URL:        c.url,
		Attempt:    attemptNo,
	}

	sentAt := d.now()
	code, err := d.send(ctx, c, sentAt)
	result.Latency = d.now().Sub(sentAt)
	result.ResponseCode = code
	result.Err = err

	switch {
	case err == nil && code >= 200 && code < 300:
		result.Status = StatusDelivered
	case attemptNo >= MaxAttempts():
		result.Status = StatusDeadLetter
	default:
		result.Status = StatusRetrying
	}

	if updateErr := d.record(ctx, tx, result, attemptNo); updateErr != nil {
		result.Err = updateErr
	}
	return result
}

func (d *Dispatcher) send(ctx context.Context, c claim, at time.Time) (int, error) {
	timeout := time.Duration(c.timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.url, bytes.NewReader(c.payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(Header, Sign(c.secret, at, c.payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("webhook: endpoint returned %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (d *Dispatcher) record(ctx context.Context, tx pgx.Tx, a Attempt, attemptNo int) error {
	var nextRetry *time.Time
	if a.Status == StatusRetrying {
		delay, ok := NextRetry(attemptNo)
		if !ok {
			a.Status = StatusDeadLetter
		} else {
			at := d.now().Add(delay)
			nextRetry = &at
		}
	}

	var deliveredAt *time.Time
	if a.Status == StatusDelivered {
		at := d.now()
		deliveredAt = &at
	}

	var responseCode *int
	if a.ResponseCode != 0 {
		responseCode = &a.ResponseCode
	}

	var responseBody *string
	if a.Err != nil {
		msg := a.Err.Error()
		responseBody = &msg
	}

	_, err := tx.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET attempt = $1, status = $2, response_code = $3, response_body = $4,
		     latency_ms = $5, next_retry_at = $6, delivered_at = $7
		 WHERE id = $8`,
		attemptNo, a.Status, responseCode, responseBody,
		int(a.Latency/time.Millisecond), nextRetry, deliveredAt, a.DeliveryID)
	if err != nil {
		return fmt.Errorf("webhook: record attempt: %w", err)
	}
	return nil
}

func (d *Dispatcher) Replay(ctx context.Context, deliveryID, actor string) error {
	if actor == "" {
		return fmt.Errorf("webhook: replaying a dead letter needs an actor")
	}

	tag, err := d.pool.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = $1, attempt = 0, next_retry_at = now(), response_body = NULL
		 WHERE id = $2 AND status = $3`,
		StatusPending, deliveryID, StatusDeadLetter)
	if err != nil {
		return fmt.Errorf("webhook: replay: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("webhook: %s is not in the dead letter queue", deliveryID)
	}
	return nil
}

func (d *Dispatcher) DeadLetters(ctx context.Context) ([]Attempt, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT d.id, d.event_id, d.endpoint_id, d.attempt, d.response_code, ep.url
		 FROM webhook_deliveries d
		 JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
		 WHERE d.status = $1
		 ORDER BY d.created_at`,
		StatusDeadLetter)
	if err != nil {
		return nil, fmt.Errorf("webhook: dead letters: %w", err)
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		var a Attempt
		var code *int
		if err := rows.Scan(&a.DeliveryID, &a.EventID, &a.EndpointID, &a.Attempt, &code, &a.URL); err != nil {
			return nil, fmt.Errorf("webhook: scan dead letter: %w", err)
		}
		if code != nil {
			a.ResponseCode = *code
		}
		a.Status = StatusDeadLetter
		out = append(out, a)
	}
	return out, rows.Err()
}

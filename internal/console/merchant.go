package console

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("console: not found")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type Payment struct {
	ID                  string    `json:"id"`
	MerchantID          string    `json:"merchant_id"`
	OutletID            string    `json:"outlet_id,omitempty"`
	Method              string    `json:"method"`
	Amount              int64     `json:"amount"`
	Fee                 int64     `json:"fee"`
	Net                 int64     `json:"net"`
	Status              string    `json:"status"`
	FailureReason       string    `json:"failure_reason,omitempty"`
	LedgerTransactionID string    `json:"ledger_transaction_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type PaymentPage struct {
	Data    []Payment `json:"data"`
	HasMore bool      `json:"has_more"`
	Totals  Totals    `json:"totals"`
}

type Totals struct {
	Count     int64 `json:"count"`
	Gross     int64 `json:"gross"`
	Fees      int64 `json:"fees"`
	Net       int64 `json:"net"`
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
}

func clamp(limit int) int {
	if limit <= 0 {
		return 25
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func (s *Store) Payments(ctx context.Context, merchantID, status string, limit int) (*PaymentPage, error) {
	limit = clamp(limit)

	rows, err := s.pool.Query(ctx,
		`SELECT id, merchant_id, COALESCE(outlet_id, ''), method, amount_minor, fee_minor,
		        status, COALESCE(failure_reason, ''), COALESCE(ledger_transaction_id, ''), created_at
		 FROM payments
		 WHERE merchant_id = $1 AND ($2 = '' OR status = $2)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $3`,
		merchantID, status, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &PaymentPage{Data: []Payment{}}
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.MerchantID, &p.OutletID, &p.Method, &p.Amount, &p.Fee,
			&p.Status, &p.FailureReason, &p.LedgerTransactionID, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Net = p.Amount - p.Fee
		page.Data = append(page.Data, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(page.Data) > limit {
		page.HasMore = true
		page.Data = page.Data[:limit]
	}

	err = s.pool.QueryRow(ctx,
		`SELECT count(*),
		        COALESCE(SUM(amount_minor), 0),
		        COALESCE(SUM(fee_minor), 0),
		        COALESCE(SUM(amount_minor - fee_minor), 0),
		        count(*) FILTER (WHERE status = 'succeeded'),
		        count(*) FILTER (WHERE status = 'failed')
		 FROM payments WHERE merchant_id = $1`, merchantID).
		Scan(&page.Totals.Count, &page.Totals.Gross, &page.Totals.Fees, &page.Totals.Net,
			&page.Totals.Succeeded, &page.Totals.Failed)
	if err != nil {
		return nil, err
	}
	return page, nil
}

func (s *Store) Payment(ctx context.Context, merchantID, paymentID string) (*Payment, error) {
	var p Payment
	err := s.pool.QueryRow(ctx,
		`SELECT id, merchant_id, COALESCE(outlet_id, ''), method, amount_minor, fee_minor,
		        status, COALESCE(failure_reason, ''), COALESCE(ledger_transaction_id, ''), created_at
		 FROM payments
		 WHERE id = $1 AND ($2 = '' OR merchant_id = $2)`, paymentID, merchantID).
		Scan(&p.ID, &p.MerchantID, &p.OutletID, &p.Method, &p.Amount, &p.Fee,
			&p.Status, &p.FailureReason, &p.LedgerTransactionID, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Net = p.Amount - p.Fee
	return &p, nil
}

type Payout struct {
	ID            string     `json:"id"`
	MerchantID    string     `json:"merchant_id"`
	PaymentID     string     `json:"payment_id,omitempty"`
	BatchID       string     `json:"batch_id,omitempty"`
	Mode          string     `json:"mode"`
	Gross         int64      `json:"gross"`
	Holdback      int64      `json:"holdback"`
	Net           int64      `json:"net"`
	Rail          string     `json:"rail,omitempty"`
	Status        string     `json:"status"`
	BankReference string     `json:"bank_reference,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	LatencyMs     *int       `json:"latency_ms,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
}

type PayoutPage struct {
	Data    []Payout     `json:"data"`
	HasMore bool         `json:"has_more"`
	Summary PayoutTotals `json:"summary"`
}

type PayoutTotals struct {
	Count       int64 `json:"count"`
	Instant     int64 `json:"instant"`
	Batch       int64 `json:"batch"`
	Settled     int64 `json:"settled"`
	Failed      int64 `json:"failed"`
	GrossMinor  int64 `json:"gross"`
	HeldBackMin int64 `json:"holdback"`
	NetMinor    int64 `json:"net"`
	MedianMs    *int  `json:"median_latency_ms,omitempty"`
	P95Ms       *int  `json:"p95_latency_ms,omitempty"`
}

func (s *Store) Payouts(ctx context.Context, merchantID, mode string, limit int) (*PayoutPage, error) {
	limit = clamp(limit)

	rows, err := s.pool.Query(ctx,
		`SELECT id, merchant_id, COALESCE(payment_id, ''), COALESCE(batch_id, ''), mode,
		        gross_minor, holdback_minor, net_minor, COALESCE(rail, ''), status,
		        COALESCE(bank_reference, ''), COALESCE(failure_reason, ''),
		        latency_ms, created_at, settled_at
		 FROM payouts
		 WHERE ($1 = '' OR merchant_id = $1) AND ($2 = '' OR mode = $2)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $3`,
		merchantID, mode, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &PayoutPage{Data: []Payout{}}
	for rows.Next() {
		var p Payout
		if err := rows.Scan(&p.ID, &p.MerchantID, &p.PaymentID, &p.BatchID, &p.Mode,
			&p.Gross, &p.Holdback, &p.Net, &p.Rail, &p.Status,
			&p.BankReference, &p.FailureReason, &p.LatencyMs, &p.CreatedAt, &p.SettledAt); err != nil {
			return nil, err
		}
		page.Data = append(page.Data, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(page.Data) > limit {
		page.HasMore = true
		page.Data = page.Data[:limit]
	}

	err = s.pool.QueryRow(ctx,
		`SELECT count(*),
		        count(*) FILTER (WHERE mode = 'instant'),
		        count(*) FILTER (WHERE mode = 'batch'),
		        count(*) FILTER (WHERE status = 'settled'),
		        count(*) FILTER (WHERE status = 'failed'),
		        COALESCE(SUM(gross_minor), 0),
		        COALESCE(SUM(holdback_minor), 0),
		        COALESCE(SUM(net_minor), 0),
		        percentile_disc(0.5) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL),
		        percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL)
		 FROM payouts WHERE ($1 = '' OR merchant_id = $1)`, merchantID).
		Scan(&page.Summary.Count, &page.Summary.Instant, &page.Summary.Batch,
			&page.Summary.Settled, &page.Summary.Failed,
			&page.Summary.GrossMinor, &page.Summary.HeldBackMin, &page.Summary.NetMinor,
			&page.Summary.MedianMs, &page.Summary.P95Ms)
	if err != nil {
		return nil, err
	}
	return page, nil
}

type Settlement struct {
	ID          string     `json:"id"`
	MerchantID  string     `json:"merchant_id"`
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   time.Time  `json:"period_end"`
	PayoutCount int        `json:"payout_count"`
	Gross       int64      `json:"gross"`
	Fee         int64      `json:"fee"`
	Net         int64      `json:"net"`
	Status      string     `json:"status"`
	SettledAt   *time.Time `json:"settled_at,omitempty"`
}

func (s *Store) Settlements(ctx context.Context, merchantID string, limit int) ([]Settlement, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, merchant_id, period_start, period_end, payout_count,
		        gross_minor, fee_minor, net_minor, status, settled_at
		 FROM settlement_batches
		 WHERE ($1 = '' OR merchant_id = $1)
		 ORDER BY period_start DESC
		 LIMIT $2`, merchantID, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Settlement{}
	for rows.Next() {
		var b Settlement
		if err := rows.Scan(&b.ID, &b.MerchantID, &b.PeriodStart, &b.PeriodEnd, &b.PayoutCount,
			&b.Gross, &b.Fee, &b.Net, &b.Status, &b.SettledAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type Endpoint struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	Events     []string   `json:"events"`
	TimeoutMs  int        `json:"timeout_ms"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	SecretHint string     `json:"signing_secret_hint"`
}

func (s *Store) Endpoints(ctx context.Context, merchantID string) ([]Endpoint, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, url, events, timeout_ms, disabled_at, created_at,
		        left(signing_secret, 6)
		 FROM webhook_endpoints WHERE merchant_id = $1 ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Endpoint{}
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.ID, &e.URL, &e.Events, &e.TimeoutMs,
			&e.DisabledAt, &e.CreatedAt, &e.SecretHint); err != nil {
			return nil, err
		}
		e.SecretHint += "…"
		out = append(out, e)
	}
	return out, rows.Err()
}

type Delivery struct {
	ID           string     `json:"id"`
	EventID      string     `json:"event_id"`
	EventType    string     `json:"event_type"`
	EndpointURL  string     `json:"endpoint_url"`
	Attempt      int        `json:"attempt"`
	Status       string     `json:"status"`
	ResponseCode *int       `json:"response_code,omitempty"`
	LatencyMs    *int       `json:"latency_ms,omitempty"`
	NextRetryAt  *time.Time `json:"next_retry_at,omitempty"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Store) Deliveries(ctx context.Context, merchantID, status string, limit int) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id, d.event_id, e.type, ep.url, d.attempt, d.status,
		        d.response_code, d.latency_ms, d.next_retry_at, d.delivered_at, d.created_at
		 FROM webhook_deliveries d
		 JOIN webhook_endpoints ep ON ep.id = d.endpoint_id
		 JOIN webhook_events e ON e.id = d.event_id
		 WHERE ($1 = '' OR ep.merchant_id = $1) AND ($2 = '' OR d.status = $2)
		 ORDER BY d.created_at DESC
		 LIMIT $3`, merchantID, status, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventID, &d.EventType, &d.EndpointURL, &d.Attempt, &d.Status,
			&d.ResponseCode, &d.LatencyMs, &d.NextRetryAt, &d.DeliveredAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

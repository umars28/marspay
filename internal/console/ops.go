package console

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Float struct {
	Outstanding    int64     `json:"outstanding"`
	Limit          int64     `json:"limit"`
	UtilisationBps int       `json:"utilisation_bps"`
	InstantEnabled bool      `json:"instant_enabled"`
	CapturedAt     time.Time `json:"captured_at"`
	Headroom       int64     `json:"headroom"`
}

type Exposure struct {
	MerchantID  string `json:"merchant_id"`
	DisplayName string `json:"display_name"`
	Outstanding int64  `json:"outstanding"`
	Payouts     int64  `json:"payouts"`
}

type FloatView struct {
	Position Float      `json:"position"`
	Top      []Exposure `json:"top_exposure"`
	History  []Float    `json:"history"`
}

func (s *Store) Float(ctx context.Context) (*FloatView, error) {
	view := &FloatView{Top: []Exposure{}, History: []Float{}}

	rows, err := s.pool.Query(ctx,
		`SELECT outstanding_minor, limit_minor, utilisation_bps, instant_enabled, captured_at
		 FROM float_positions ORDER BY captured_at DESC LIMIT 24`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var f Float
		if err := rows.Scan(&f.Outstanding, &f.Limit, &f.UtilisationBps,
			&f.InstantEnabled, &f.CapturedAt); err != nil {
			return nil, err
		}
		f.Headroom = f.Limit - f.Outstanding
		view.History = append(view.History, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(view.History) > 0 {
		view.Position = view.History[0]
	}

	exposure, err := s.pool.Query(ctx,
		`SELECT p.merchant_id, COALESCE(m.display_name, p.merchant_id),
		        COALESCE(SUM(p.net_minor), 0), count(*)
		 FROM payouts p
		 LEFT JOIN merchants m ON m.id = p.merchant_id
		 WHERE p.mode = 'instant' AND p.status IN ('sending', 'settled')
		 GROUP BY p.merchant_id, m.display_name
		 ORDER BY 3 DESC
		 LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer exposure.Close()

	for exposure.Next() {
		var e Exposure
		if err := exposure.Scan(&e.MerchantID, &e.DisplayName, &e.Outstanding, &e.Payouts); err != nil {
			return nil, err
		}
		view.Top = append(view.Top, e)
	}
	return view, exposure.Err()
}

type RailHealth struct {
	Rail      string `json:"rail"`
	Sent      int64  `json:"sent"`
	Settled   int64  `json:"settled"`
	Failed    int64  `json:"failed"`
	Sending   int64  `json:"sending"`
	MedianMs  *int   `json:"median_latency_ms,omitempty"`
	P95Ms     *int   `json:"p95_latency_ms,omitempty"`
	SlowestMs *int   `json:"slowest_latency_ms,omitempty"`
}

type Engine struct {
	Window   string       `json:"window"`
	Instant  int64        `json:"instant"`
	Batch    int64        `json:"degraded_to_batch"`
	Settled  int64        `json:"settled"`
	Failed   int64        `json:"failed"`
	Sending  int64        `json:"sending"`
	MedianMs *int         `json:"median_latency_ms,omitempty"`
	P95Ms    *int         `json:"p95_latency_ms,omitempty"`
	P99Ms    *int         `json:"p99_latency_ms,omitempty"`
	Rails    []RailHealth `json:"rails"`
	Stuck    []Payout     `json:"needing_attention"`
}

func (s *Store) Engine(ctx context.Context, window time.Duration) (*Engine, error) {
	if window <= 0 {
		window = 24 * time.Hour
	}
	since := time.Now().Add(-window)

	e := &Engine{Window: window.String(), Rails: []RailHealth{}, Stuck: []Payout{}}
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE mode = 'instant'),
		        count(*) FILTER (WHERE status = 'degraded_to_batch'),
		        count(*) FILTER (WHERE status = 'settled'),
		        count(*) FILTER (WHERE status = 'failed'),
		        count(*) FILTER (WHERE status = 'sending'),
		        percentile_disc(0.5) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL),
		        percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL),
		        percentile_disc(0.99) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL)
		 FROM payouts WHERE created_at >= $1`, since).
		Scan(&e.Instant, &e.Batch, &e.Settled, &e.Failed, &e.Sending,
			&e.MedianMs, &e.P95Ms, &e.P99Ms)
	if err != nil {
		return nil, err
	}

	rails, err := s.pool.Query(ctx,
		`SELECT rail, count(*),
		        count(*) FILTER (WHERE status = 'settled'),
		        count(*) FILTER (WHERE status = 'failed'),
		        count(*) FILTER (WHERE status = 'sending'),
		        percentile_disc(0.5) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL),
		        percentile_disc(0.95) WITHIN GROUP (ORDER BY latency_ms)
		          FILTER (WHERE latency_ms IS NOT NULL),
		        max(latency_ms)
		 FROM payouts
		 WHERE created_at >= $1 AND rail IS NOT NULL
		 GROUP BY rail
		 ORDER BY 2 DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rails.Close()

	for rails.Next() {
		var h RailHealth
		if err := rails.Scan(&h.Rail, &h.Sent, &h.Settled, &h.Failed, &h.Sending,
			&h.MedianMs, &h.P95Ms, &h.SlowestMs); err != nil {
			return nil, err
		}
		e.Rails = append(e.Rails, h)
	}
	if err := rails.Err(); err != nil {
		return nil, err
	}

	stuck, err := s.pool.Query(ctx,
		`SELECT id, merchant_id, COALESCE(payment_id, ''), COALESCE(batch_id, ''), mode,
		        gross_minor, holdback_minor, net_minor, COALESCE(rail, ''), status,
		        COALESCE(bank_reference, ''), COALESCE(failure_reason, ''),
		        latency_ms, created_at, settled_at
		 FROM payouts
		 WHERE status IN ('sending', 'failed')
		 ORDER BY created_at DESC
		 LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer stuck.Close()

	for stuck.Next() {
		var p Payout
		if err := stuck.Scan(&p.ID, &p.MerchantID, &p.PaymentID, &p.BatchID, &p.Mode,
			&p.Gross, &p.Holdback, &p.Net, &p.Rail, &p.Status,
			&p.BankReference, &p.FailureReason, &p.LatencyMs, &p.CreatedAt, &p.SettledAt); err != nil {
			return nil, err
		}
		e.Stuck = append(e.Stuck, p)
	}
	return e, stuck.Err()
}

type Run struct {
	ID            string     `json:"id"`
	BusinessDate  time.Time  `json:"business_date"`
	ProviderCode  string     `json:"provider_code"`
	RowsCompared  int        `json:"rows_compared"`
	RowsMatched   int        `json:"rows_matched"`
	Discrepancies int        `json:"discrepancies"`
	Delta         int64      `json:"delta"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
}

type Discrepancy struct {
	ID             string  `json:"id"`
	RunID          string  `json:"run_id"`
	ExternalRef    string  `json:"external_ref"`
	InternalMinor  *int64  `json:"internal,omitempty"`
	ProviderMinor  *int64  `json:"provider,omitempty"`
	Delta          int64   `json:"delta"`
	SuspectedCause string  `json:"suspected_cause"`
	Resolution     *string `json:"resolution,omitempty"`
}

type Reconciliation struct {
	Runs []Run         `json:"runs"`
	Open []Discrepancy `json:"open"`
}

func (s *Store) Reconciliation(ctx context.Context, limit int) (*Reconciliation, error) {
	out := &Reconciliation{Runs: []Run{}, Open: []Discrepancy{}}

	rows, err := s.pool.Query(ctx,
		`SELECT id, business_date, provider_code, rows_compared, rows_matched,
		        discrepancies, delta_minor, status, started_at, finished_at
		 FROM reconciliation_runs
		 ORDER BY business_date DESC, started_at DESC
		 LIMIT $1`, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.BusinessDate, &r.ProviderCode, &r.RowsCompared,
			&r.RowsMatched, &r.Discrepancies, &r.Delta, &r.Status,
			&r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out.Runs = append(out.Runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	open, err := s.pool.Query(ctx,
		`SELECT id, run_id, external_ref, internal_minor, provider_minor,
		        delta_minor, COALESCE(suspected_cause, ''), resolution
		 FROM reconciliation_discrepancies
		 WHERE resolution IS NULL OR resolution = 'pending'
		 ORDER BY created_at DESC
		 LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer open.Close()

	for open.Next() {
		var d Discrepancy
		if err := open.Scan(&d.ID, &d.RunID, &d.ExternalRef, &d.InternalMinor,
			&d.ProviderMinor, &d.Delta, &d.SuspectedCause, &d.Resolution); err != nil {
			return nil, err
		}
		out.Open = append(out.Open, d)
	}
	return out, open.Err()
}

type Entry struct {
	ID        string    `json:"id"`
	AccountID string    `json:"account_id"`
	OwnerType string    `json:"owner_type"`
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

type LedgerView struct {
	TransactionID string  `json:"transaction_id"`
	Kind          string  `json:"kind"`
	ReferenceID   string  `json:"reference_id,omitempty"`
	Entries       []Entry `json:"entries"`
	Sum           int64   `json:"sum"`
	Balanced      bool    `json:"balanced"`
}

func (s *Store) Ledger(ctx context.Context, paymentID string) (*LedgerView, error) {
	var txID, kind, reference string
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.kind, COALESCE(t.reference_id, '')
		 FROM payments p JOIN ledger_transactions t ON t.id = p.ledger_transaction_id
		 WHERE p.id = $1`, paymentID).Scan(&txID, &kind, &reference)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT e.id, e.account_id, a.owner_type, e.amount_minor, e.created_at
		 FROM ledger_entries e JOIN accounts a ON a.id = e.account_id
		 WHERE e.transaction_id = $1
		 ORDER BY e.amount_minor`, txID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	view := &LedgerView{TransactionID: txID, Kind: kind, ReferenceID: reference, Entries: []Entry{}}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.AccountID, &e.OwnerType, &e.Amount, &e.CreatedAt); err != nil {
			return nil, err
		}
		view.Sum += e.Amount
		view.Entries = append(view.Entries, e)
	}
	view.Balanced = view.Sum == 0
	return view, rows.Err()
}

type Hit struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Detail  string `json:"detail,omitempty"`
	Amount  *int64 `json:"amount,omitempty"`
	Status  string `json:"status,omitempty"`
	Created string `json:"created_at,omitempty"`
}

func (s *Store) Search(ctx context.Context, query string) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []Hit{}, nil
	}
	pattern := "%" + query + "%"

	rows, err := s.pool.Query(ctx,
		`SELECT 'payment', p.id, COALESCE(m.display_name, p.merchant_id),
		        p.method, p.amount_minor, p.status, p.created_at
		 FROM payments p LEFT JOIN merchants m ON m.id = p.merchant_id
		 WHERE p.id ILIKE $1 OR p.ledger_transaction_id ILIKE $1 OR p.merchant_id ILIKE $1
		 UNION ALL
		 SELECT 'payout', po.id, COALESCE(m.display_name, po.merchant_id),
		        COALESCE(po.bank_reference, po.mode), po.net_minor, po.status, po.created_at
		 FROM payouts po LEFT JOIN merchants m ON m.id = po.merchant_id
		 WHERE po.id ILIKE $1 OR po.bank_reference ILIKE $1 OR po.merchant_id ILIKE $1
		 UNION ALL
		 SELECT 'user', u.id, u.full_name, u.phone, NULL, u.status, u.created_at
		 FROM users u
		 WHERE u.id ILIKE $1 OR u.phone ILIKE $1 OR u.full_name ILIKE $1
		 UNION ALL
		 SELECT 'merchant', m.id, m.display_name, m.category, NULL, m.status, m.created_at
		 FROM merchants m
		 WHERE m.id ILIKE $1 OR m.display_name ILIKE $1 OR m.legal_name ILIKE $1
		 ORDER BY 7 DESC
		 LIMIT 25`, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hit{}
	for rows.Next() {
		var h Hit
		var amount *int64
		var created time.Time
		if err := rows.Scan(&h.Kind, &h.ID, &h.Label, &h.Detail, &amount, &h.Status, &created); err != nil {
			return nil, err
		}
		h.Amount = amount
		h.Created = created.Format(time.RFC3339)
		out = append(out, h)
	}
	return out, rows.Err()
}

type AccountView struct {
	AccountID string  `json:"account_id"`
	OwnerType string  `json:"owner_type"`
	OwnerID   string  `json:"owner_id,omitempty"`
	Kind      string  `json:"account_type"`
	Balance   int64   `json:"balance"`
	Entries   []Entry `json:"entries"`
}

func (s *Store) Account(ctx context.Context, accountID string, limit int) (*AccountView, error) {
	view := &AccountView{AccountID: accountID, Entries: []Entry{}}

	err := s.pool.QueryRow(ctx,
		`SELECT a.owner_type, COALESCE(a.owner_id, ''), a.account_type,
		        COALESCE((SELECT SUM(amount_minor) FROM ledger_entries WHERE account_id = a.id), 0)
		 FROM accounts a WHERE a.id = $1`, accountID).
		Scan(&view.OwnerType, &view.OwnerID, &view.Kind, &view.Balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT e.id, e.account_id, a.owner_type, e.amount_minor, e.created_at
		 FROM ledger_entries e JOIN accounts a ON a.id = e.account_id
		 WHERE e.account_id = $1
		 ORDER BY e.created_at DESC
		 LIMIT $2`, accountID, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.AccountID, &e.OwnerType, &e.Amount, &e.CreatedAt); err != nil {
			return nil, err
		}
		view.Entries = append(view.Entries, e)
	}
	return view, rows.Err()
}

type Hour struct {
	Hour   int   `json:"hour"`
	Count  int64 `json:"count"`
	Volume int64 `json:"volume"`
}

func (s *Store) Hourly(ctx context.Context, merchantID string) ([]Hour, error) {
	rows, err := s.pool.Query(ctx,
		`WITH hours AS (SELECT generate_series(0, 23) AS hour)
		 SELECT h.hour,
		        count(p.id),
		        COALESCE(SUM(p.amount_minor), 0)
		 FROM hours h
		 LEFT JOIN payments p
		   ON EXTRACT(HOUR FROM p.created_at) = h.hour
		  AND p.created_at >= now() - interval '24 hours'
		  AND ($1 = '' OR p.merchant_id = $1)
		 GROUP BY h.hour
		 ORDER BY h.hour`, merchantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Hour{}
	for rows.Next() {
		var h Hour
		if err := rows.Scan(&h.Hour, &h.Count, &h.Volume); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

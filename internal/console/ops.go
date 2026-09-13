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

const (
	floatLimitMinor      = int64(5_000_000_000)
	floatDegradeBps      = 8_500
	floatCollectionHours = 24
)

func (s *Store) Float(ctx context.Context) (*FloatView, error) {
	view := &FloatView{Top: []Exposure{}, History: []Float{}}

	var outstanding int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(net_minor), 0) FROM payouts
		 WHERE mode = 'instant'
		   AND status IN ('queued', 'sending', 'settled')
		   AND created_at > now() - make_interval(hours => $1)`,
		floatCollectionHours).Scan(&outstanding); err != nil {
		return nil, err
	}

	live := Float{
		Outstanding: outstanding,
		Limit:       floatLimitMinor,
		CapturedAt:  time.Now(),
		Headroom:    floatLimitMinor - outstanding,
	}
	live.UtilisationBps = int(outstanding * 10_000 / floatLimitMinor)
	live.InstantEnabled = live.UtilisationBps < floatDegradeBps
	view.Position = live

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

type Queue struct {
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	Waiting  int64      `json:"waiting"`
	Failed   int64      `json:"failed"`
	Oldest   *time.Time `json:"oldest_waiting,omitempty"`
	LagSec   *float64   `json:"lag_seconds,omitempty"`
	Done     int64      `json:"done"`
	Detail   string     `json:"detail,omitempty"`
	Attempts int64      `json:"max_attempts,omitempty"`
}

type Job struct {
	Name     string     `json:"name"`
	Schedule string     `json:"schedule"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	Outcome  string     `json:"outcome"`
	Pending  int64      `json:"pending"`
}

type Queues struct {
	Queues []Queue `json:"queues"`
	Jobs   []Job   `json:"jobs"`
}

func (s *Store) Queues(ctx context.Context) (*Queues, error) {
	out := &Queues{Queues: []Queue{}, Jobs: []Job{}}

	var outbox Queue
	outbox.Name = "outbox relay"
	outbox.Kind = "relay"
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE published_at IS NULL),
		        count(*) FILTER (WHERE published_at IS NULL AND attempts > 0),
		        count(*) FILTER (WHERE published_at IS NOT NULL),
		        min(created_at) FILTER (WHERE published_at IS NULL),
		        COALESCE(max(attempts), 0)
		 FROM outbox`).
		Scan(&outbox.Waiting, &outbox.Failed, &outbox.Done, &outbox.Oldest, &outbox.Attempts)
	if err != nil {
		return nil, err
	}
	if outbox.Oldest != nil {
		lag := time.Since(*outbox.Oldest).Seconds()
		outbox.LagSec = &lag
	}
	outbox.Detail = "events written with the ledger, published to Kafka by the relay"
	out.Queues = append(out.Queues, outbox)

	var hooks Queue
	hooks.Name = "webhook dispatcher"
	hooks.Kind = "delivery"
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status IN ('pending', 'retrying')),
		        count(*) FILTER (WHERE status = 'dead_letter'),
		        count(*) FILTER (WHERE status = 'delivered'),
		        min(created_at) FILTER (WHERE status IN ('pending', 'retrying')),
		        COALESCE(max(attempt), 0)
		 FROM webhook_deliveries`).
		Scan(&hooks.Waiting, &hooks.Failed, &hooks.Done, &hooks.Oldest, &hooks.Attempts)
	if err != nil {
		return nil, err
	}
	if hooks.Oldest != nil {
		lag := time.Since(*hooks.Oldest).Seconds()
		hooks.LagSec = &lag
	}
	hooks.Detail = "at-least-once delivery; nine attempts then the dead letter queue"
	out.Queues = append(out.Queues, hooks)

	var rails Queue
	rails.Name = "payout rails"
	rails.Kind = "external"
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status = 'sending'),
		        count(*) FILTER (WHERE status = 'failed'),
		        count(*) FILTER (WHERE status = 'settled'),
		        min(created_at) FILTER (WHERE status = 'sending'),
		        COALESCE(max(attempts), 0)
		 FROM payouts`).
		Scan(&rails.Waiting, &rails.Failed, &rails.Done, &rails.Oldest, &rails.Attempts)
	if err != nil {
		return nil, err
	}
	if rails.Oldest != nil {
		lag := time.Since(*rails.Oldest).Seconds()
		rails.LagSec = &lag
	}
	rails.Detail = "money in flight at a bank; ambiguous timeouts sit here until the rail answers"
	out.Queues = append(out.Queues, rails)

	var holdbackDue int64
	var holdbackOldest *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE released_at IS NULL AND release_at <= now()),
		        min(release_at) FILTER (WHERE released_at IS NULL)
		 FROM holdbacks`).Scan(&holdbackDue, &holdbackOldest)
	if err != nil {
		return nil, err
	}
	out.Jobs = append(out.Jobs, Job{
		Name:     "holdback release",
		Schedule: "every 5 minutes",
		LastRun:  holdbackOldest,
		Outcome:  outcomeFor(holdbackDue),
		Pending:  holdbackDue,
	})

	var lastRecon *time.Time
	var reconStatus string
	err = s.pool.QueryRow(ctx,
		`SELECT max(finished_at),
		        COALESCE((SELECT status FROM reconciliation_runs
		                  ORDER BY started_at DESC LIMIT 1), 'never run')
		 FROM reconciliation_runs`).Scan(&lastRecon, &reconStatus)
	if err != nil {
		return nil, err
	}

	var openDifferences int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM reconciliation_discrepancies
		 WHERE resolution IS NULL OR resolution = 'pending'`).Scan(&openDifferences); err != nil {
		return nil, err
	}
	out.Jobs = append(out.Jobs, Job{
		Name:     "daily reconciliation",
		Schedule: "03:00 WIB",
		LastRun:  lastRecon,
		Outcome:  reconStatus,
		Pending:  openDifferences,
	})

	var staleSessions int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions
		 WHERE revoked_at IS NULL AND refresh_expires_at < now()`).Scan(&staleSessions); err != nil {
		return nil, err
	}
	out.Jobs = append(out.Jobs, Job{
		Name:     "session expiry sweep",
		Schedule: "hourly",
		Outcome:  outcomeFor(staleSessions),
		Pending:  staleSessions,
	})

	var expiredChallenges int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM otp_challenges
		 WHERE consumed_at IS NULL AND expires_at < now()`).Scan(&expiredChallenges); err != nil {
		return nil, err
	}
	out.Jobs = append(out.Jobs, Job{
		Name:     "one-time code cleanup",
		Schedule: "hourly",
		Outcome:  outcomeFor(expiredChallenges),
		Pending:  expiredChallenges,
	})

	return out, nil
}

func outcomeFor(pending int64) string {
	if pending == 0 {
		return "nothing waiting"
	}
	return "work is due"
}

func (s *Store) Transitions(ctx context.Context, kind, operationID string, limit int) ([]Transition, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, operation_kind, operation_id, COALESCE(from_status, ''), to_status,
		        actor, COALESCE(reason, ''), created_at
		 FROM operation_state_transitions
		 WHERE ($1 = '' OR operation_kind = $1) AND ($2 = '' OR operation_id = $2)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $3`, kind, operationID, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Transition{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.Kind, &t.Operation, &t.From, &t.To,
			&t.Actor, &t.Reason, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type Transition struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"operation_kind"`
	Operation string    `json:"operation_id"`
	From      string    `json:"from_status,omitempty"`
	To        string    `json:"to_status"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

package payout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/rail"
)

type Batch struct {
	ID           string     `json:"id"`
	MerchantID   string     `json:"merchant_id"`
	PeriodStart  time.Time  `json:"period_start"`
	PeriodEnd    time.Time  `json:"period_end"`
	PayoutCount  int        `json:"payout_count"`
	GrossMinor   int64      `json:"gross"`
	FeeMinor     int64      `json:"holdback"`
	NetMinor     int64      `json:"net"`
	Currency     string     `json:"currency"`
	Status       string     `json:"status"`
	BankRef      string     `json:"bank_reference,omitempty"`
	FailureCause string     `json:"failure_reason,omitempty"`
	SettledAt    *time.Time `json:"settled_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type BatchResult struct {
	BusinessDate time.Time `json:"business_date"`
	Batches      []Batch   `json:"batches"`
	TotalNet     int64     `json:"total_net"`
	Currency     string    `json:"currency"`
}

func (s *Service) RunBatch(ctx context.Context, businessDate time.Time, dest map[string]Destination) (*BatchResult, error) {
	start := businessDate.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)

	rows, err := s.pool.Query(ctx,
		`SELECT merchant_id,
		        count(*),
		        COALESCE(SUM(gross_minor), 0),
		        COALESCE(SUM(holdback_minor), 0),
		        COALESCE(SUM(net_minor), 0)
		 FROM payouts
		 WHERE mode = 'batch' AND status = 'degraded_to_batch'
		   AND created_at >= $1 AND created_at < $2
		 GROUP BY merchant_id
		 ORDER BY merchant_id`,
		start, end)
	if err != nil {
		return nil, fmt.Errorf("payout: group batch: %w", err)
	}

	type group struct {
		merchantID           string
		count                int
		gross, holdback, net int64
	}

	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.merchantID, &g.count, &g.gross, &g.holdback, &g.net); err != nil {
			rows.Close()
			return nil, fmt.Errorf("payout: scan group: %w", err)
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &BatchResult{BusinessDate: start, Currency: "IDR"}

	for _, g := range groups {
		batch := Batch{
			ID:          batchID(start, g.merchantID),
			MerchantID:  g.merchantID,
			PeriodStart: start,
			PeriodEnd:   end,
			PayoutCount: g.count,
			GrossMinor:  g.gross,
			FeeMinor:    g.holdback,
			NetMinor:    g.net,
			Currency:    "IDR",
			Status:      "open",
		}

		settled, err := s.sendBatch(ctx, &batch, dest[g.merchantID])
		if err != nil {
			return nil, err
		}
		if settled {
			result.TotalNet += batch.NetMinor
		}
		result.Batches = append(result.Batches, batch)
	}

	return result, nil
}

func (s *Service) sendBatch(ctx context.Context, batch *Batch, dest Destination) (bool, error) {
	chosen := rail.Select(s.rails, dest.BankCode, s.health)

	if chosen != nil {
		sendResult, sendErr := chosen.Send(ctx, rail.Request{
			PayoutID:    batch.ID,
			BankCode:    dest.BankCode,
			AccountNo:   dest.AccountNo,
			AccountName: dest.AccountName,
			Amount:      money.Minor(batch.NetMinor),
		})
		batch.BankRef = sendResult.BankReference

		switch {
		case sendErr == nil:
			batch.Status = "settled"
		case errors.Is(sendErr, rail.ErrAmbiguous), errors.Is(sendErr, rail.ErrTimeout):
			batch.Status = "sending"
		default:
			batch.Status = "failed"
			batch.FailureCause = sendErr.Error()
		}
	} else {
		batch.Status = "failed"
		batch.FailureCause = "no healthy rail available"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("payout: begin batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var settledAt *time.Time
	if batch.Status == "settled" {
		at := s.now()
		settledAt = &at
		batch.SettledAt = &at
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO settlement_batches
		   (id, merchant_id, period_start, period_end, payout_count,
		    gross_minor, fee_minor, net_minor, status, settled_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (merchant_id, period_start) DO UPDATE SET
		   payout_count = EXCLUDED.payout_count,
		   gross_minor  = EXCLUDED.gross_minor,
		   fee_minor    = EXCLUDED.fee_minor,
		   net_minor    = EXCLUDED.net_minor,
		   status       = EXCLUDED.status,
		   settled_at   = EXCLUDED.settled_at
		 RETURNING created_at`,
		batch.ID, batch.MerchantID, batch.PeriodStart, batch.PeriodEnd, batch.PayoutCount,
		batch.GrossMinor, batch.FeeMinor, batch.NetMinor, batch.Status, settledAt).
		Scan(&batch.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("payout: insert batch: %w", err)
	}
	batch.CreatedAt = batch.CreatedAt.UTC()

	if batch.Status == "settled" {
		if _, err := tx.Exec(ctx,
			`UPDATE payouts SET batch_id = $1, status = 'settled', settled_at = now()
			 WHERE merchant_id = $2 AND mode = 'batch' AND status = 'degraded_to_batch'
			   AND created_at >= $3 AND created_at < $4`,
			batch.ID, batch.MerchantID, batch.PeriodStart, batch.PeriodEnd); err != nil {
			return false, fmt.Errorf("payout: attach payouts: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("payout: commit batch: %w", err)
	}
	return batch.Status == "settled", nil
}

func (s *Service) Settlements(ctx context.Context, merchantID string) ([]Batch, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, merchant_id, period_start, period_end, payout_count,
		        gross_minor, fee_minor, net_minor, status, settled_at, created_at
		 FROM settlement_batches WHERE merchant_id = $1
		 ORDER BY period_start DESC`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("payout: list settlements: %w", err)
	}
	defer rows.Close()

	var out []Batch
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.MerchantID, &b.PeriodStart, &b.PeriodEnd,
			&b.PayoutCount, &b.GrossMinor, &b.FeeMinor, &b.NetMinor,
			&b.Status, &b.SettledAt, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("payout: scan batch: %w", err)
		}
		b.Currency = "IDR"
		b.PeriodStart = b.PeriodStart.UTC()
		b.PeriodEnd = b.PeriodEnd.UTC()
		b.CreatedAt = b.CreatedAt.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) Settlement(ctx context.Context, batchID string) (*Batch, error) {
	var b Batch
	err := s.pool.QueryRow(ctx,
		`SELECT id, merchant_id, period_start, period_end, payout_count,
		        gross_minor, fee_minor, net_minor, status, settled_at, created_at
		 FROM settlement_batches WHERE id = $1`, batchID).
		Scan(&b.ID, &b.MerchantID, &b.PeriodStart, &b.PeriodEnd, &b.PayoutCount,
			&b.GrossMinor, &b.FeeMinor, &b.NetMinor, &b.Status, &b.SettledAt, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Settlement batch %s was not found.", batchID)
	}
	if err != nil {
		return nil, fmt.Errorf("payout: load batch: %w", err)
	}

	b.Currency = "IDR"
	b.PeriodStart = b.PeriodStart.UTC()
	b.PeriodEnd = b.PeriodEnd.UTC()
	b.CreatedAt = b.CreatedAt.UTC()
	return &b, nil
}

func batchID(businessDate time.Time, merchantID string) string {
	return "stl_" + businessDate.Format("20060102") + "_" + id.Prefix(merchantID) + shortID(merchantID)
}

func shortID(merchantID string) string {
	if len(merchantID) <= 8 {
		return merchantID
	}
	return merchantID[len(merchantID)-8:]
}

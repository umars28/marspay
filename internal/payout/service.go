package payout

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/rail"
	"github.com/umars28/marspay/internal/risk"
)

const HoldbackWindow = 24 * time.Hour

type Payout struct {
	ID            string        `json:"id"`
	MerchantID    string        `json:"merchant_id"`
	PaymentID     string        `json:"payment_id"`
	Mode          string        `json:"mode"`
	GrossMinor    money.Minor   `json:"gross"`
	HoldbackMinor money.Minor   `json:"holdback"`
	NetMinor      money.Minor   `json:"net"`
	HoldbackBps   int           `json:"holdback_bps"`
	Rail          rail.Name     `json:"rail,omitempty"`
	Status        string        `json:"status"`
	BankReference string        `json:"bank_reference,omitempty"`
	Latency       time.Duration `json:"-"`
	LatencyMs     int           `json:"latency_ms"`
	DegradeReason string        `json:"degrade_reason,omitempty"`
}

type Destination struct {
	BankCode    string
	AccountNo   string
	AccountName string
}

type Service struct {
	pool   *pgxpool.Pool
	ledger *ledger.Repo
	float  *Float
	rails  []rail.Rail
	health map[rail.Name]rail.Health
	scores *risk.Store
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, l *ledger.Repo, f *Float, rails []rail.Rail) *Service {
	return &Service{
		pool:   pool,
		ledger: l,
		float:  f,
		rails:  rails,
		health: map[rail.Name]rail.Health{},
		scores: risk.NewStore(pool),
		now:    time.Now,
	}
}

func (s *Service) SetHealth(h map[rail.Name]rail.Health) {
	s.health = h
}

func (s *Service) Settle(ctx context.Context, paymentID, merchantID string, gross money.Minor, factors risk.Factors, dest Destination) (*Payout, error) {
	if gross <= 0 {
		return nil, fmt.Errorf("payout: gross must be positive, got %d", gross)
	}

	score, err := risk.Evaluate(factors)
	if err != nil {
		return nil, err
	}
	if _, err := s.scores.Record(ctx, merchantID, score); err != nil {
		return nil, err
	}

	position, err := s.float.Position(ctx)
	if err != nil {
		return nil, err
	}

	mode, reason := ModeFor(position, score.HoldbackBps)
	if score.Mode == "batch" {
		mode, reason = "batch", "merchant risk score below the instant floor"
	}

	exceeded, err := s.float.ExposureExceeded(ctx, merchantID, gross)
	if err != nil {
		return nil, err
	}
	if exceeded {
		mode, reason = "batch", "merchant exposure limit reached"
	}

	holdback, err := money.FeeHalfUp(gross, score.HoldbackBps)
	if err != nil {
		return nil, err
	}

	p := &Payout{
		ID:            id.New("po"),
		MerchantID:    merchantID,
		PaymentID:     paymentID,
		Mode:          mode,
		GrossMinor:    gross,
		HoldbackMinor: holdback,
		NetMinor:      gross - holdback,
		HoldbackBps:   score.HoldbackBps,
		DegradeReason: reason,
	}

	if mode == "batch" {
		p.Status = "degraded_to_batch"
		if err := s.persist(ctx, p, nil); err != nil {
			return nil, err
		}
		return p, nil
	}

	chosen := rail.Select(s.rails, dest.BankCode, s.health)
	if chosen == nil {
		p.Status = "degraded_to_batch"
		p.Mode = "batch"
		p.DegradeReason = "no healthy rail available"
		if err := s.persist(ctx, p, nil); err != nil {
			return nil, err
		}
		return p, nil
	}

	start := s.now()
	result, sendErr := chosen.Send(ctx, rail.Request{
		PayoutID:    p.ID,
		BankCode:    dest.BankCode,
		AccountNo:   dest.AccountNo,
		AccountName: dest.AccountName,
		Amount:      p.NetMinor,
	})

	p.Rail = chosen.Name()
	p.BankReference = result.BankReference
	p.Latency = s.now().Sub(start)
	if result.Latency > 0 {
		p.Latency = result.Latency
	}
	p.LatencyMs = int(p.Latency / time.Millisecond)

	switch {
	case errors.Is(sendErr, rail.ErrAmbiguous), errors.Is(sendErr, rail.ErrTimeout):
		p.Status = "sending"
	case sendErr != nil:
		p.Status = "failed"
		p.DegradeReason = sendErr.Error()
	default:
		p.Status = "settled"
	}

	posting, err := s.posting(p)
	if err != nil {
		return nil, err
	}
	if p.Status == "failed" {
		posting = nil
	}

	if err := s.persist(ctx, p, posting); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) posting(p *Payout) (*ledger.Posting, error) {
	posting, err := ledger.NewInstantPayout(ledger.InstantPayout{
		TransactionID:     id.New("txn"),
		ReferenceID:       p.ID,
		PayableAccountID:  ledger.MerchantPayable(p.MerchantID),
		HoldbackAccountID: ledger.MerchantHoldback(p.MerchantID),
		ClearingAccountID: ledger.AccountID(ledger.OwnerProvider, string(p.Rail), ledger.TypeClearing),
		Gross:             p.GrossMinor,
		HoldbackBps:       p.HoldbackBps,
	})
	if err != nil {
		return nil, err
	}
	return &posting, nil
}

func (s *Service) persist(ctx context.Context, p *Payout, posting *ledger.Posting) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("payout: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ledgerTxID *string
	if posting != nil {
		if err := s.ledger.PostTx(ctx, tx, *posting); err != nil {
			return err
		}
		ledgerTxID = &posting.TransactionID
	}

	var settledAt *time.Time
	if p.Status == "settled" {
		at := s.now()
		settledAt = &at
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO payouts
		   (id, merchant_id, payment_id, mode, gross_minor, holdback_minor, net_minor,
		    rail, status, attempts, bank_reference, failure_reason,
		    ledger_transaction_id, settled_at, latency_ms)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7,
		         NULLIF($8, ''), $9, 1, NULLIF($10, ''), NULLIF($11, ''),
		         $12, $13, $14)`,
		p.ID, p.MerchantID, p.PaymentID, p.Mode,
		int64(p.GrossMinor), int64(p.HoldbackMinor), int64(p.NetMinor),
		string(p.Rail), p.Status, p.BankReference, p.DegradeReason,
		ledgerTxID, settledAt, p.LatencyMs)
	if err != nil {
		return fmt.Errorf("payout: insert: %w", err)
	}

	if posting != nil && p.HoldbackMinor > 0 {
		_, err = tx.Exec(ctx,
			`INSERT INTO holdbacks (id, merchant_id, payout_id, amount_minor, release_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			id.New("hb"), p.MerchantID, p.ID, int64(p.HoldbackMinor),
			s.now().Add(HoldbackWindow))
		if err != nil {
			return fmt.Errorf("payout: insert holdback: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ledger.TranslateCommitError(err)
	}
	return nil
}

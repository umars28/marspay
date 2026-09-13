package compliance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/wallet"
)

const (
	DisputeSLA    = 7 * 24 * time.Hour
	DisputeWindow = 90 * 24 * time.Hour
)

var disputeOutcomes = map[string]bool{
	"resolved_user":     true,
	"resolved_merchant": true,
}

type Dispute struct {
	ID           string     `json:"id"`
	PaymentID    string     `json:"payment_id"`
	UserID       string     `json:"user_id"`
	MerchantID   string     `json:"merchant_id"`
	Amount       int64      `json:"amount"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	CoveredMinor int64      `json:"covered_by_holdback"`
	LossMinor    int64      `json:"platform_loss"`
	Currency     string     `json:"currency"`
	SLADueAt     time.Time  `json:"sla_due_at"`
	Overdue      bool       `json:"overdue"`
	CreatedAt    time.Time  `json:"created_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	LedgerTxnID  string     `json:"ledger_transaction_id,omitempty"`
}

type Disputes struct {
	pool   *pgxpool.Pool
	ledger *ledger.Repo
	wallet wallet.Reserver
	audit  *Audit
	now    func() time.Time
}

func NewDisputes(pool *pgxpool.Pool, l *ledger.Repo, w wallet.Reserver, audit *Audit) *Disputes {
	return &Disputes{pool: pool, ledger: l, wallet: w, audit: audit, now: time.Now}
}

type OpenRequest struct {
	PaymentID string `json:"payment_id"`
	Reason    string `json:"reason"`
}

func (d *Disputes) Open(ctx context.Context, userID string, req OpenRequest) (*Dispute, error) {
	if req.PaymentID == "" || req.Reason == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Fields payment_id and reason are required.")
	}

	var payerID, merchantID, status string
	var amount int64
	var paidAt time.Time

	err := d.pool.QueryRow(ctx,
		`SELECT user_id, merchant_id, status, amount_minor, created_at
		 FROM payments WHERE id = $1`,
		req.PaymentID).Scan(&payerID, &merchantID, &status, &amount, &paidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Payment %s was not found.", req.PaymentID)
	}
	if err != nil {
		return nil, fmt.Errorf("compliance: load payment: %w", err)
	}

	if payerID != userID {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Payment %s was not found.", req.PaymentID)
	}
	if status != "succeeded" {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Payment %s is %s and cannot be disputed.", req.PaymentID, status)
	}
	if d.now().Sub(paidAt) > DisputeWindow {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"The 90 day dispute window for this payment has closed.")
	}

	var existing int
	if err := d.pool.QueryRow(ctx,
		`SELECT count(*) FROM disputes WHERE payment_id = $1 AND resolved_at IS NULL`,
		req.PaymentID).Scan(&existing); err != nil {
		return nil, fmt.Errorf("compliance: count disputes: %w", err)
	}
	if existing > 0 {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment already has an open dispute.")
	}

	result := &Dispute{
		ID:         id.New("dsp"),
		PaymentID:  req.PaymentID,
		UserID:     userID,
		MerchantID: merchantID,
		Amount:     amount,
		Reason:     req.Reason,
		Status:     "open",
		Currency:   "IDR",
		SLADueAt:   d.now().UTC().Add(DisputeSLA),
	}

	err = d.pool.QueryRow(ctx,
		`INSERT INTO disputes
		   (id, payment_id, user_id, merchant_id, amount_minor, reason, status, sla_due_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'open', $7)
		 RETURNING created_at`,
		result.ID, req.PaymentID, userID, merchantID, amount, req.Reason, result.SLADueAt).
		Scan(&result.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("compliance: insert dispute: %w", err)
	}
	result.CreatedAt = result.CreatedAt.UTC()
	return result, nil
}

func (d *Disputes) Queue(ctx context.Context) ([]Dispute, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id, payment_id, user_id, merchant_id, amount_minor, reason, status,
		        covered_minor, loss_minor, sla_due_at, created_at
		 FROM disputes WHERE resolved_at IS NULL ORDER BY sla_due_at`)
	if err != nil {
		return nil, fmt.Errorf("compliance: dispute queue: %w", err)
	}
	defer rows.Close()

	now := d.now().UTC()
	var out []Dispute
	for rows.Next() {
		var v Dispute
		if err := rows.Scan(&v.ID, &v.PaymentID, &v.UserID, &v.MerchantID,
			&v.Amount, &v.Reason, &v.Status, &v.CoveredMinor, &v.LossMinor,
			&v.SLADueAt, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("compliance: scan dispute: %w", err)
		}
		v.Currency = "IDR"
		v.SLADueAt = v.SLADueAt.UTC()
		v.CreatedAt = v.CreatedAt.UTC()
		v.Overdue = now.After(v.SLADueAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

type ResolveRequest struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

func (d *Disputes) Resolve(ctx context.Context, disputeID string, req ResolveRequest, actor string) (*Dispute, error) {
	if actor == "" {
		return nil, ErrNoActor
	}
	if req.Reason == "" {
		return nil, ErrNoReason
	}
	if !disputeOutcomes[req.Outcome] {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field outcome must be resolved_user or resolved_merchant.")
	}

	var v Dispute
	err := d.pool.QueryRow(ctx,
		`SELECT id, payment_id, user_id, merchant_id, amount_minor, reason, status, created_at
		 FROM disputes WHERE id = $1`,
		disputeID).Scan(&v.ID, &v.PaymentID, &v.UserID, &v.MerchantID,
		&v.Amount, &v.Reason, &v.Status, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Dispute %s was not found.", disputeID)
	}
	if err != nil {
		return nil, fmt.Errorf("compliance: load dispute: %w", err)
	}
	if v.Status == "resolved_user" || v.Status == "resolved_merchant" {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Dispute %s is already %s.", disputeID, v.Status)
	}

	if req.Outcome == "resolved_merchant" {
		return d.close(ctx, v, req, actor, 0, 0, "")
	}

	covered, holdbackID, err := d.availableHoldback(ctx, v.MerchantID, money.Minor(v.Amount))
	if err != nil {
		return nil, err
	}
	loss := money.Minor(v.Amount) - covered

	txID := id.New("txn")
	posting, err := ledger.NewDisputeRefund(ledger.DisputeRefund{
		TransactionID:     txID,
		ReferenceID:       disputeID,
		HoldbackAccountID: ledger.MerchantHoldback(v.MerchantID),
		FloatAccountID:    ledger.PlatformFloat(),
		WalletAccountID:   ledger.UserWallet(v.UserID),
		Amount:            money.Minor(v.Amount),
		CoveredByHoldback: covered,
	})
	if err != nil {
		return nil, err
	}

	if err := d.ledger.EnsureAccount(ctx, ledger.PlatformFloat(),
		ledger.OwnerPlatform, "", ledger.TypeFloat); err != nil {
		return nil, err
	}
	if err := d.ledger.EnsureAccount(ctx, ledger.MerchantHoldback(v.MerchantID),
		ledger.OwnerMerchant, v.MerchantID, ledger.TypeMerchantHoldback); err != nil {
		return nil, err
	}

	if err := d.ledger.Post(ctx, posting); err != nil {
		return nil, err
	}

	if holdbackID != "" && covered > 0 {
		if _, err := d.pool.Exec(ctx,
			`UPDATE holdbacks SET consumed_by = $1, released_at = now() WHERE id = $2`,
			disputeID, holdbackID); err != nil {
			return nil, fmt.Errorf("compliance: consume holdback: %w", err)
		}
	}

	if d.wallet != nil {
		if err := d.wallet.Invalidate(ctx, ledger.UserWallet(v.UserID)); err != nil {
			return nil, err
		}
	}

	return d.close(ctx, v, req, actor, covered, loss, txID)
}

func (d *Disputes) close(ctx context.Context, v Dispute, req ResolveRequest,
	actor string, covered, loss money.Minor, txID string) (*Dispute, error) {

	_, err := d.pool.Exec(ctx,
		`UPDATE disputes
		 SET status = $1, covered_minor = $2, loss_minor = $3, resolved_at = now()
		 WHERE id = $4`,
		req.Outcome, int64(covered), int64(loss), v.ID)
	if err != nil {
		return nil, fmt.Errorf("compliance: close dispute: %w", err)
	}

	if d.audit != nil {
		if err := d.audit.Record(ctx, Entry{
			Actor: actor, Action: "resolve_dispute",
			ObjectType: "dispute", ObjectID: v.ID,
			Before: map[string]any{"status": v.Status},
			After: map[string]any{
				"status":  req.Outcome,
				"covered": int64(covered),
				"loss":    int64(loss),
			},
			Reason: req.Reason,
		}); err != nil {
			return nil, err
		}
	}

	v.Status = req.Outcome
	v.CoveredMinor = int64(covered)
	v.LossMinor = int64(loss)
	v.LedgerTxnID = txID
	v.Currency = "IDR"
	v.CreatedAt = v.CreatedAt.UTC()

	resolved := d.now().UTC()
	v.ResolvedAt = &resolved
	return &v, nil
}

func (d *Disputes) availableHoldback(ctx context.Context, merchantID string, need money.Minor) (money.Minor, string, error) {
	var holdbackID string
	var amount int64

	err := d.pool.QueryRow(ctx,
		`SELECT id, amount_minor FROM holdbacks
		 WHERE merchant_id = $1 AND released_at IS NULL AND consumed_by IS NULL
		 ORDER BY created_at LIMIT 1`,
		merchantID).Scan(&holdbackID, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("compliance: available holdback: %w", err)
	}

	if money.Minor(amount) > need {
		return need, holdbackID, nil
	}
	return money.Minor(amount), holdbackID, nil
}

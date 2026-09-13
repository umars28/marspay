package txn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/outbox"
)

type RefundRequest struct {
	PaymentID string `json:"payment_id"`
	Amount    int64  `json:"amount,omitempty"`
	Reason    string `json:"reason"`
}

type Refund struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	PaymentID           string    `json:"payment_id"`
	MerchantID          string    `json:"merchant_id"`
	Amount              int64     `json:"amount"`
	FeeReturned         int64     `json:"fee_returned"`
	Partial             bool      `json:"partial"`
	Reason              string    `json:"reason"`
	Currency            string    `json:"currency"`
	LedgerTransactionID string    `json:"ledger_transaction_id"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) CreateRefund(ctx context.Context, callerMerchantID string, req RefundRequest) (*Refund, error) {
	if req.PaymentID == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field payment_id is required.")
	}
	if req.Reason == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field reason is required. A refund without a stated reason is not auditable.")
	}

	var payerID, merchantID, status string
	var paidAmount, paidFee int64

	err := s.pool.QueryRow(ctx,
		`SELECT user_id, merchant_id, status, amount_minor, fee_minor
		 FROM payments WHERE id = $1`,
		req.PaymentID).Scan(&payerID, &merchantID, &status, &paidAmount, &paidFee)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Payment %s was not found.", req.PaymentID)
	}
	if err != nil {
		return nil, fmt.Errorf("txn: load payment: %w", err)
	}
	if callerMerchantID != "" && callerMerchantID != merchantID {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Payment %s was not found.", req.PaymentID)
	}
	if status != "succeeded" {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Payment %s is %s and cannot be refunded.", req.PaymentID, status)
	}

	var alreadyRefunded int64
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM refunds
		 WHERE payment_id = $1 AND status = 'succeeded'`,
		req.PaymentID).Scan(&alreadyRefunded)
	if err != nil {
		return nil, fmt.Errorf("txn: sum refunds: %w", err)
	}

	amount := money.Minor(req.Amount)
	if amount == 0 {
		amount = money.Minor(paidAmount - alreadyRefunded)
	}
	if amount <= 0 {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Payment %s is already fully refunded.", req.PaymentID)
	}
	if int64(amount)+alreadyRefunded > paidAmount {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Refunding this amount would exceed the payment.").
			WithDetails(map[string]any{
				"paid":             paidAmount,
				"already_refunded": alreadyRefunded,
				"requested":        int64(amount),
			})
	}

	feeReturned, err := ledger.ProportionalFee(
		money.Minor(paidAmount), money.Minor(paidFee), amount)
	if err != nil {
		return nil, err
	}

	refundID := id.New("ref")
	txID := id.New("txn")

	posting, err := ledger.NewRefund(ledger.Refund{
		TransactionID:    txID,
		ReferenceID:      refundID,
		PayableAccountID: ledger.MerchantPayable(merchantID),
		WalletAccountID:  ledger.UserWallet(payerID),
		FeeAccountID:     ledger.PlatformFeeRevenue(),
		Amount:           amount,
		FeeReturned:      feeReturned,
	})
	if err != nil {
		return nil, err
	}

	result := &Refund{
		ID:                  refundID,
		Status:              "succeeded",
		PaymentID:           req.PaymentID,
		MerchantID:          merchantID,
		Amount:              int64(amount),
		FeeReturned:         int64(feeReturned),
		Partial:             int64(amount)+alreadyRefunded < paidAmount,
		Reason:              req.Reason,
		Currency:            "IDR",
		LedgerTransactionID: txID,
	}

	msg, err := event(outbox.TopicPaymentEvents, merchantID, "refund.created", result)
	if err != nil {
		return nil, err
	}

	err = s.commit(ctx, posting, msg, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO refunds
			   (id, payment_id, merchant_id, amount_minor, reason, status, ledger_transaction_id)
			 VALUES ($1, $2, $3, $4, $5, 'succeeded', $6)
			 RETURNING created_at`,
			refundID, req.PaymentID, merchantID, int64(amount), req.Reason, txID).
			Scan(&result.CreatedAt)
	})
	if err != nil {
		return nil, err
	}

	if err := s.wallet.Invalidate(ctx, ledger.UserWallet(payerID)); err != nil {
		return nil, fmt.Errorf("txn: refund committed but the payer cache is stale: %w", err)
	}

	result.CreatedAt = utc(result.CreatedAt)
	return result, nil
}

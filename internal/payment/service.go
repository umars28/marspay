package payment

import (
	"context"
	"encoding/json"
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
	"github.com/umars28/marspay/internal/outbox"
	"github.com/umars28/marspay/internal/velocity"
	"github.com/umars28/marspay/internal/wallet"
)

var validMethods = map[string]bool{
	"qris":            true,
	"payment_link":    true,
	"virtual_account": true,
}

type CreateRequest struct {
	MerchantID string `json:"merchant_id"`
	OutletID   string `json:"outlet_id,omitempty"`
	Method     string `json:"method"`
	Amount     int64  `json:"amount"`
	Currency   string `json:"currency"`
}

type MerchantRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Payment struct {
	ID                  string      `json:"id"`
	Status              string      `json:"status"`
	Amount              int64       `json:"amount"`
	Fee                 int64       `json:"fee"`
	Currency            string      `json:"currency"`
	Method              string      `json:"method"`
	Merchant            MerchantRef `json:"merchant"`
	LedgerTransactionID string      `json:"ledger_transaction_id"`
	BalanceAfter        int64       `json:"balance_after"`
	CreatedAt           time.Time   `json:"created_at"`
}

type Service struct {
	pool   *pgxpool.Pool
	ledger *ledger.Repo
	wallet wallet.Reserver
	guard  *velocity.Guard
}

func NewService(pool *pgxpool.Pool, l *ledger.Repo, w wallet.Reserver) *Service {
	return &Service{pool: pool, ledger: l, wallet: w}
}

func (s *Service) WithVelocity(g *velocity.Guard) *Service {
	s.guard = g
	return s
}

func (s *Service) Create(ctx context.Context, userID string, req CreateRequest) (*Payment, error) {
	if err := validate(req); err != nil {
		return nil, err
	}

	merchant, err := s.loadMerchant(ctx, req.MerchantID)
	if err != nil {
		return nil, err
	}

	amount := money.Minor(req.Amount)
	payerAccount := ledger.UserWallet(userID)

	if s.guard != nil {
		if err := s.guard.Check(ctx, velocity.Subject{
			UserID: userID, Kind: velocity.KindPayment, Amount: amount,
		}); err != nil {
			return nil, err
		}
	}

	balanceAfter, err := s.reserve(ctx, payerAccount, amount)
	if err != nil {
		return nil, err
	}

	payment, err := s.commit(ctx, userID, merchant, req, amount)
	if err != nil {
		if _, releaseErr := s.wallet.Release(ctx, payerAccount, amount); releaseErr != nil {
			return nil, fmt.Errorf("%w (and releasing the reservation failed: %v)", err, releaseErr)
		}
		return nil, err
	}

	payment.BalanceAfter = int64(balanceAfter)
	return payment, nil
}

func (s *Service) reserve(ctx context.Context, account string, amount money.Minor) (money.Minor, error) {
	remaining, err := s.wallet.Reserve(ctx, account, amount)
	if errors.Is(err, wallet.ErrAccountNotCached) {
		if warmErr := s.warmFromLedger(ctx, account); warmErr != nil {
			return 0, warmErr
		}
		remaining, err = s.wallet.Reserve(ctx, account, amount)
	}

	switch {
	case errors.Is(err, wallet.ErrInsufficientFunds):
		available, _ := s.wallet.Available(ctx, account)
		return 0, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInsufficient,
			"Wallet balance is below the requested amount.").
			WithDetails(map[string]any{
				"available": int64(available),
				"required":  int64(amount),
				"currency":  "IDR",
			})
	case err != nil:
		return 0, err
	}
	return remaining, nil
}

func (s *Service) warmFromLedger(ctx context.Context, account string) error {
	balance, err := s.ledger.Balance(ctx, account)
	if err != nil {
		return err
	}
	return s.wallet.Warm(ctx, account, balance)
}

func (s *Service) commit(ctx context.Context, userID string, m MerchantRef, req CreateRequest, amount money.Minor) (*Payment, error) {
	feeBps, err := s.merchantFeeBps(ctx, m.ID)
	if err != nil {
		return nil, err
	}

	paymentID := id.New("pay")
	txID := id.New("txn")

	posting, err := ledger.NewMerchantPayment(ledger.MerchantPayment{
		TransactionID:    txID,
		ReferenceID:      paymentID,
		PayerAccountID:   ledger.UserWallet(userID),
		PayableAccountID: ledger.MerchantPayable(m.ID),
		FeeAccountID:     ledger.PlatformFeeRevenue(),
		Amount:           amount,
		FeeBps:           feeBps,
	})
	if err != nil {
		return nil, err
	}

	fee, err := money.FeeHalfUp(amount, feeBps)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("payment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.ledger.PostTx(ctx, tx, posting); err != nil {
		return nil, err
	}

	var createdAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO payments
		   (id, user_id, merchant_id, outlet_id, method, amount_minor, fee_minor,
		    status, ledger_transaction_id)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, 'succeeded', $8)
		 RETURNING created_at`,
		paymentID, userID, m.ID, req.OutletID, req.Method,
		int64(amount), int64(fee), txID).Scan(&createdAt)
	if err != nil {
		return nil, fmt.Errorf("payment: insert: %w", err)
	}

	event := &Payment{
		ID:                  paymentID,
		Status:              "succeeded",
		Amount:              int64(amount),
		Fee:                 int64(fee),
		Currency:            "IDR",
		Method:              req.Method,
		Merchant:            m,
		LedgerTransactionID: txID,
		CreatedAt:           createdAt.UTC(),
	}

	payload, err := json.Marshal(map[string]any{
		"type": "payment.succeeded",
		"data": event,
	})
	if err != nil {
		return nil, fmt.Errorf("payment: encode event: %w", err)
	}

	err = outbox.Write(ctx, tx, outbox.Message{
		Topic:        outbox.TopicPaymentEvents,
		PartitionKey: m.ID,
		EventType:    "payment.succeeded",
		Payload:      payload,
	})
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, ledger.TranslateCommitError(err)
	}
	return event, nil
}

func (s *Service) loadMerchant(ctx context.Context, merchantID string) (MerchantRef, error) {
	var m MerchantRef
	var status string

	err := s.pool.QueryRow(ctx,
		`SELECT id, display_name, status FROM merchants WHERE id = $1`,
		merchantID).Scan(&m.ID, &m.Name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Merchant %s was not found.", merchantID)
	}
	if err != nil {
		return m, fmt.Errorf("payment: load merchant: %w", err)
	}
	if status != "active" {
		return m, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Merchant %s cannot accept payments while it is %s.", merchantID, status)
	}
	return m, nil
}

func (s *Service) merchantFeeBps(ctx context.Context, merchantID string) (int, error) {
	var bps int
	err := s.pool.QueryRow(ctx,
		`SELECT fee_bps FROM merchants WHERE id = $1`, merchantID).Scan(&bps)
	if err != nil {
		return 0, fmt.Errorf("payment: merchant fee: %w", err)
	}
	return bps, nil
}

func validate(req CreateRequest) error {
	if req.MerchantID == "" {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field merchant_id is required.")
	}
	if !validMethods[req.Method] {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field method must be one of qris, payment_link, virtual_account.")
	}
	if req.Amount <= 0 {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field amount must be a positive integer in minor units.")
	}
	if req.Currency != "IDR" {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field currency must be IDR.")
	}
	return nil
}

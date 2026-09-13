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
	"github.com/umars28/marspay/internal/velocity"
)

type TransferRequest struct {
	To       string `json:"to"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Note     string `json:"note,omitempty"`
}

type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Transfer struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	Amount              int64     `json:"amount"`
	Currency            string    `json:"currency"`
	Payee               UserRef   `json:"payee"`
	Note                string    `json:"note,omitempty"`
	LedgerTransactionID string    `json:"ledger_transaction_id"`
	BalanceAfter        int64     `json:"balance_after"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) CreateTransfer(ctx context.Context, userID string, req TransferRequest) (*Transfer, error) {
	if err := invalidAmount(req.Amount); err != nil {
		return nil, err
	}
	if err := invalidCurrency(req.Currency); err != nil {
		return nil, err
	}
	if err := s.assertActive(ctx, userID); err != nil {
		return nil, err
	}

	amount := money.Minor(req.Amount)
	limit, err := s.userLimits(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := overLimit(amount, limit); err != nil {
		return nil, err
	}

	payee, err := s.findPayee(ctx, req.To)
	if err != nil {
		return nil, err
	}
	if payee.ID == userID {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"You cannot transfer to yourself.")
	}

	newRecipient, err := s.isNewRecipient(ctx, userID, payee.ID)
	if err != nil {
		return nil, err
	}
	if err := s.checkVelocity(ctx, velocity.Subject{
		UserID: userID, Kind: velocity.KindTransfer,
		Amount: amount, NewRecipient: newRecipient,
	}); err != nil {
		return nil, err
	}

	payerAccount := ledger.UserWallet(userID)
	payeeAccount := ledger.UserWallet(payee.ID)

	balanceAfter, err := s.reserve(ctx, payerAccount, amount)
	if err != nil {
		return nil, err
	}

	transferID := id.New("trf")
	txID := id.New("txn")

	posting, err := ledger.NewTransfer(ledger.Transfer{
		TransactionID:  txID,
		ReferenceID:    transferID,
		PayerAccountID: payerAccount,
		PayeeAccountID: payeeAccount,
		Amount:         amount,
	})
	if err != nil {
		return nil, s.giveBack(ctx, payerAccount, amount, err)
	}

	result := &Transfer{
		ID:                  transferID,
		Status:              "succeeded",
		Amount:              int64(amount),
		Currency:            "IDR",
		Payee:               payee,
		Note:                req.Note,
		LedgerTransactionID: txID,
		BalanceAfter:        int64(balanceAfter),
	}

	msg, err := event(outbox.TopicPaymentEvents, userID, "transfer.succeeded", result)
	if err != nil {
		return nil, s.giveBack(ctx, payerAccount, amount, err)
	}

	err = s.commit(ctx, posting, msg, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO transfers
			   (id, payer_user_id, payee_user_id, amount_minor, note, status, ledger_transaction_id)
			 VALUES ($1, $2, $3, $4, NULLIF($5, ''), 'succeeded', $6)
			 RETURNING created_at`,
			transferID, userID, payee.ID, int64(amount), req.Note, txID).Scan(&result.CreatedAt)
	})
	if err != nil {
		return nil, s.giveBack(ctx, payerAccount, amount, err)
	}

	result.CreatedAt = utc(result.CreatedAt)

	if err := s.wallet.Invalidate(ctx, payeeAccount); err != nil {
		return nil, fmt.Errorf("txn: transfer committed but the payee cache is stale: %w", err)
	}
	return result, nil
}

func (s *Service) isNewRecipient(ctx context.Context, payerID, payeeID string) (bool, error) {
	var seen int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM transfers
		 WHERE payer_user_id = $1 AND payee_user_id = $2 AND status = 'succeeded'`,
		payerID, payeeID).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("txn: recipient history: %w", err)
	}
	return seen == 0, nil
}

func (s *Service) findPayee(ctx context.Context, to string) (UserRef, error) {
	var u UserRef
	var status string

	err := s.pool.QueryRow(ctx,
		`SELECT id, full_name, status FROM users WHERE id = $1 OR phone = $1`,
		to).Scan(&u.ID, &u.Name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No Marspay account matches %q.", to)
	}
	if err != nil {
		return u, fmt.Errorf("txn: find payee: %w", err)
	}
	if status != "active" {
		return u, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"That account is %s and cannot receive money.", status)
	}
	return u, nil
}

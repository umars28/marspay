package txn

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
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/outbox"
	"github.com/umars28/marspay/internal/rail"
	"github.com/umars28/marspay/internal/wallet"
)

type Service struct {
	pool       *pgxpool.Pool
	ledger     *ledger.Repo
	wallet     wallet.Reserver
	billerRail rail.Rail
}

func NewService(pool *pgxpool.Pool, l *ledger.Repo, w wallet.Reserver) *Service {
	return &Service{pool: pool, ledger: l, wallet: w}
}

func (s *Service) WithBillerRail(r rail.Rail) *Service {
	s.billerRail = r
	return s
}

func (s *Service) reserve(ctx context.Context, account string, amount money.Minor) (money.Minor, error) {
	remaining, err := s.wallet.Reserve(ctx, account, amount)
	if errors.Is(err, wallet.ErrAccountNotCached) {
		balance, ledgerErr := s.ledger.Balance(ctx, account)
		if ledgerErr != nil {
			return 0, ledgerErr
		}
		if warmErr := s.wallet.Warm(ctx, account, balance); warmErr != nil {
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

func (s *Service) giveBack(ctx context.Context, account string, amount money.Minor, cause error) error {
	if _, err := s.wallet.Release(ctx, account, amount); err != nil {
		return fmt.Errorf("%w (and releasing the reservation failed: %v)", cause, err)
	}
	return cause
}

type persistFunc func(ctx context.Context, tx pgx.Tx) error

func (s *Service) commit(ctx context.Context, posting ledger.Posting, event outbox.Message, persist persistFunc) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("txn: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.ledger.PostTx(ctx, tx, posting); err != nil {
		return err
	}
	if err := persist(ctx, tx); err != nil {
		return err
	}
	if err := outbox.Write(ctx, tx, event); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return ledger.TranslateCommitError(err)
	}
	return nil
}

func event(topic, key, eventType string, body any) (outbox.Message, error) {
	payload, err := json.Marshal(map[string]any{"type": eventType, "data": body})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("txn: encode %s: %w", eventType, err)
	}
	return outbox.Message{
		Topic:        topic,
		PartitionKey: key,
		EventType:    eventType,
		Payload:      payload,
	}, nil
}

func (s *Service) userLimits(ctx context.Context, userID string) (perTxn money.Minor, err error) {
	var limit int64
	err = s.pool.QueryRow(ctx,
		`SELECT max_per_txn_minor FROM user_limits WHERE user_id = $1`, userID).Scan(&limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return money.Minor(20_000_000_00), nil
	}
	if err != nil {
		return 0, fmt.Errorf("txn: load limits: %w", err)
	}
	return money.Minor(limit), nil
}

func (s *Service) assertActive(ctx context.Context, userID string) error {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound, "Account not found.")
	}
	if err != nil {
		return fmt.Errorf("txn: load user: %w", err)
	}
	if status != "active" {
		return httpx.Errorf(http.StatusForbidden, httpx.TypeForbidden,
			"This account is %s and cannot move money.", status)
	}
	return nil
}

func invalidAmount(amount int64) error {
	if amount <= 0 {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field amount must be a positive integer in minor units.")
	}
	return nil
}

func invalidCurrency(currency string) error {
	if currency != "IDR" {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field currency must be IDR.")
	}
	return nil
}

func overLimit(amount, limit money.Minor) error {
	if amount > limit {
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Amount exceeds your per-transaction limit.").
			WithDetails(map[string]any{
				"limit":    int64(limit),
				"required": int64(amount),
				"currency": "IDR",
			})
	}
	return nil
}

func utc(t time.Time) time.Time {
	return t.UTC()
}

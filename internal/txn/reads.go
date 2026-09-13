package txn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/wallet"
)

type Balance struct {
	Available     int64  `json:"available"`
	Held          int64  `json:"held"`
	Currency      string `json:"currency"`
	Cached        *int64 `json:"cached,omitempty"`
	CacheAgreed   bool   `json:"cache_agreed"`
	LedgerBalance int64  `json:"ledger_balance"`
}

func (s *Service) Balance(ctx context.Context, userID string) (*Balance, error) {
	walletAccount := ledger.UserWallet(userID)

	fromLedger, err := s.ledger.Balance(ctx, walletAccount)
	if err != nil {
		return nil, err
	}

	held, err := s.heldAmount(ctx, userID)
	if err != nil {
		return nil, err
	}

	result := &Balance{
		Available:     int64(fromLedger - held),
		Held:          int64(held),
		Currency:      "IDR",
		LedgerBalance: int64(fromLedger),
		CacheAgreed:   true,
	}

	cached, err := s.wallet.Available(ctx, walletAccount)
	switch {
	case errors.Is(err, wallet.ErrAccountNotCached):
		result.CacheAgreed = true
	case err != nil:
		return nil, err
	default:
		v := int64(cached)
		result.Cached = &v
		result.CacheAgreed = cached == fromLedger-held
	}

	return result, nil
}

func (s *Service) heldAmount(ctx context.Context, userID string) (money.Minor, error) {
	var held int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor + admin_fee_minor), 0)
		 FROM withdrawals WHERE user_id = $1 AND status = 'pending'`,
		userID).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("txn: held amount: %w", err)
	}
	return money.Minor(held), nil
}

type Activity struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Direction   string    `json:"direction"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	Counterpart string    `json:"counterpart,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Page struct {
	Data       []Activity `json:"data"`
	HasMore    bool       `json:"has_more"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

const historyQuery = `
SELECT p.id, 'payment', 'out', p.amount_minor + p.fee_minor, p.status,
       COALESCE(m.display_name, p.merchant_id), p.created_at
  FROM payments p LEFT JOIN merchants m ON m.id = p.merchant_id
 WHERE p.user_id = $1
UNION ALL
SELECT t.id, 'transfer', 'out', t.amount_minor, t.status,
       COALESCE(u.full_name, t.payee_user_id), t.created_at
  FROM transfers t LEFT JOIN users u ON u.id = t.payee_user_id
 WHERE t.payer_user_id = $1
UNION ALL
SELECT t.id, 'transfer', 'in', t.amount_minor, t.status,
       COALESCE(u.full_name, t.payer_user_id), t.created_at
  FROM transfers t LEFT JOIN users u ON u.id = t.payer_user_id
 WHERE t.payee_user_id = $1
UNION ALL
SELECT id, 'topup', 'in', amount_minor, status, provider_code, created_at
  FROM topups WHERE user_id = $1
UNION ALL
SELECT id, 'withdrawal', 'out', amount_minor + admin_fee_minor, status, bank_code, created_at
  FROM withdrawals WHERE user_id = $1
UNION ALL
SELECT id, 'bill_payment', 'out', amount_minor + admin_fee_minor, status, biller_code, created_at
  FROM bill_payments WHERE user_id = $1
UNION ALL
SELECT r.id, 'refund', 'in', r.amount_minor, r.status,
       COALESCE(m.display_name, r.merchant_id), r.created_at
  FROM refunds r
  JOIN payments p ON p.id = r.payment_id
  LEFT JOIN merchants m ON m.id = r.merchant_id
 WHERE p.user_id = $1
ORDER BY 7 DESC, 1 DESC
LIMIT $2`

func (s *Service) History(ctx context.Context, userID string, limit int) (*Page, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx, historyQuery, userID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("txn: history: %w", err)
	}
	defer rows.Close()

	page := &Page{Data: []Activity{}}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Kind, &a.Direction, &a.Amount,
			&a.Status, &a.Counterpart, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("txn: scan activity: %w", err)
		}
		a.Currency = "IDR"
		a.CreatedAt = utc(a.CreatedAt)
		page.Data = append(page.Data, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(page.Data) > limit {
		page.Data = page.Data[:limit]
		page.HasMore = true
		page.NextCursor = page.Data[len(page.Data)-1].ID
	}
	return page, nil
}

type Profile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	KYCTier   string    `json:"kyc_tier"`
	Status    string    `json:"status"`
	Limits    Limits    `json:"limits"`
	CreatedAt time.Time `json:"created_at"`
}

type Limits struct {
	MaxBalance  int64  `json:"max_balance"`
	MaxPerTxn   int64  `json:"max_per_transaction"`
	MaxMonthly  int64  `json:"max_monthly"`
	UsedMonthly int64  `json:"used_this_month"`
	Currency    string `json:"currency"`
}

func (s *Service) Profile(ctx context.Context, userID string) (*Profile, error) {
	var p Profile

	err := s.pool.QueryRow(ctx,
		`SELECT id, full_name, phone, kyc_tier, status, created_at
		 FROM users WHERE id = $1`,
		userID).Scan(&p.ID, &p.Name, &p.Phone, &p.KYCTier, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound, "Account not found.")
	}
	if err != nil {
		return nil, fmt.Errorf("txn: load profile: %w", err)
	}
	p.CreatedAt = utc(p.CreatedAt)

	p.Limits = Limits{
		MaxBalance: 20_000_000_00,
		MaxPerTxn:  20_000_000_00,
		MaxMonthly: 20_000_000_00,
		Currency:   "IDR",
	}
	if p.KYCTier == "unverified" {
		p.Limits.MaxBalance = 2_000_000_00
		p.Limits.MaxPerTxn = 2_000_000_00
		p.Limits.MaxMonthly = 2_000_000_00
	}

	var used int64
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM payments
		 WHERE user_id = $1 AND status = 'succeeded'
		   AND created_at >= date_trunc('month', now())`,
		userID).Scan(&used)
	if err != nil {
		return nil, fmt.Errorf("txn: monthly usage: %w", err)
	}
	p.Limits.UsedMonthly = used

	return &p, nil
}

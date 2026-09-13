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
	"github.com/umars28/marspay/internal/state"
	"github.com/umars28/marspay/internal/velocity"
)

const (
	MinTopup = money.Minor(10_000_00)
	MaxTopup = money.Minor(20_000_000_00)
)

var topupSources = map[string]money.Minor{
	"bank_va": 0,
	"retail":  money.Minor(250_000),
}

type TopupRequest struct {
	Source       string `json:"source"`
	ProviderCode string `json:"provider_code"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
}

type Topup struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	Source              string    `json:"source"`
	ProviderCode        string    `json:"provider_code"`
	VirtualAccount      string    `json:"virtual_account,omitempty"`
	Amount              int64     `json:"amount"`
	AdminFee            int64     `json:"admin_fee"`
	TotalPayable        int64     `json:"total_payable"`
	Currency            string    `json:"currency"`
	LedgerTransactionID string    `json:"ledger_transaction_id,omitempty"`
	Instructions        string    `json:"instructions"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) CreateTopup(ctx context.Context, userID string, req TopupRequest) (*Topup, error) {
	if err := invalidAmount(req.Amount); err != nil {
		return nil, err
	}
	if err := invalidCurrency(req.Currency); err != nil {
		return nil, err
	}
	if err := s.assertActive(ctx, userID); err != nil {
		return nil, err
	}

	adminFee, known := topupSources[req.Source]
	if !known {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field source must be bank_va or retail.")
	}
	if req.ProviderCode == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field provider_code is required.")
	}

	amount := money.Minor(req.Amount)
	if amount < MinTopup || amount > MaxTopup {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Top up must be between Rp 10,000 and Rp 20,000,000.").
			WithDetails(map[string]any{
				"minimum": int64(MinTopup),
				"maximum": int64(MaxTopup),
			})
	}

	if err := s.checkVelocity(ctx, velocity.Subject{
		UserID: userID, Kind: velocity.KindTopup, Amount: amount,
	}); err != nil {
		return nil, err
	}

	topupID := id.New("tup")
	va := virtualAccount(req.ProviderCode, userID)

	result := &Topup{
		ID:             topupID,
		Status:         "pending",
		Source:         req.Source,
		ProviderCode:   req.ProviderCode,
		VirtualAccount: va,
		Amount:         int64(amount),
		AdminFee:       int64(adminFee),
		TotalPayable:   int64(amount + adminFee),
		Currency:       "IDR",
		Instructions: fmt.Sprintf(
			"Transfer exactly Rp %s to %s virtual account %s. Your balance updates when the bank confirms.",
			(amount + adminFee).String()[3:], req.ProviderCode, va),
	}

	err := s.pool.QueryRow(ctx,
		`INSERT INTO topups
		   (id, user_id, source, provider_code, va_number, amount_minor, admin_fee_minor, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
		 RETURNING created_at`,
		topupID, userID, req.Source, req.ProviderCode, va,
		int64(amount), int64(adminFee)).Scan(&result.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("txn: insert topup: %w", err)
	}

	result.CreatedAt = utc(result.CreatedAt)
	return result, nil
}

type ProviderCallback struct {
	ProviderCode string
	ExternalRef  string
	TopupID      string
	Amount       money.Minor
	Outcome      string
}

var ErrCallbackAlreadySeen = errors.New("txn: this callback was already processed")

func (s *Service) ConfirmTopup(ctx context.Context, cb ProviderCallback) (*Topup, error) {
	recorded, err := s.recordCallback(ctx, cb)
	if err != nil {
		return nil, err
	}
	if !recorded {
		return s.loadTopup(ctx, cb.TopupID)
	}

	var userID, source, providerCode, status string
	var amount, adminFee int64

	err = s.pool.QueryRow(ctx,
		`SELECT user_id, source, provider_code, status, amount_minor, admin_fee_minor
		 FROM topups WHERE id = $1`,
		cb.TopupID).Scan(&userID, &source, &providerCode, &status, &amount, &adminFee)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Top up %s was not found.", cb.TopupID)
	}
	if err != nil {
		return nil, fmt.Errorf("txn: load topup: %w", err)
	}

	if status != "pending" {
		return s.loadTopup(ctx, cb.TopupID)
	}
	if cb.Outcome != "success" {
		return s.failTopup(ctx, cb.TopupID, cb.Outcome)
	}
	if money.Minor(amount) != cb.Amount {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"The provider settled a different amount than we recorded.").
			WithDetails(map[string]any{
				"expected": amount,
				"settled":  int64(cb.Amount),
			})
	}

	walletAccount := ledger.UserWallet(userID)
	clearingAccount := ledger.AccountID(ledger.OwnerProvider, providerCode, ledger.TypeClearing)

	if err := s.ledger.EnsureAccount(ctx, clearingAccount,
		ledger.OwnerProvider, providerCode, ledger.TypeClearing); err != nil {
		return nil, err
	}

	txID := id.New("txn")
	posting, err := ledger.NewTopup(ledger.Topup{
		TransactionID:     txID,
		ReferenceID:       cb.TopupID,
		ClearingAccountID: clearingAccount,
		WalletAccountID:   walletAccount,
		FeeAccountID:      ledger.PlatformFeeRevenue(),
		Amount:            money.Minor(amount),
		AdminFee:          money.Minor(adminFee),
	})
	if err != nil {
		return nil, err
	}

	result := &Topup{
		ID:                  cb.TopupID,
		Status:              "succeeded",
		Source:              source,
		ProviderCode:        providerCode,
		Amount:              amount,
		AdminFee:            adminFee,
		TotalPayable:        amount + adminFee,
		Currency:            "IDR",
		LedgerTransactionID: txID,
		Instructions:        "Completed.",
	}

	msg, err := event(outbox.TopicPaymentEvents, userID, "topup.succeeded", result)
	if err != nil {
		return nil, err
	}

	err = s.commit(ctx, posting, msg, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE topups SET status = 'succeeded', provider_ref = $1,
			 ledger_transaction_id = $2, updated_at = now()
			 WHERE id = $3 AND status = 'pending'`,
			cb.ExternalRef, txID, cb.TopupID); err != nil {
			return err
		}
		return state.RecordTx(ctx, tx, state.KindTopup, cb.TopupID,
			"pending", "succeeded", state.ActorProvider, cb.ExternalRef)
	})
	if err != nil {
		return nil, err
	}

	if err := s.wallet.Invalidate(ctx, walletAccount); err != nil {
		return nil, fmt.Errorf("txn: topup committed but the cache is stale: %w", err)
	}

	result.CreatedAt = utc(time.Now())
	return result, nil
}

func (s *Service) recordCallback(ctx context.Context, cb ProviderCallback) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO provider_callbacks
		   (id, provider_code, external_ref, event_type, signature_ok, payload, processed_at)
		 VALUES ($1, $2, $3, $4, true, $5, now())
		 ON CONFLICT (provider_code, external_ref, event_type) DO NOTHING`,
		id.New("cb"), cb.ProviderCode, cb.ExternalRef, "topup."+cb.Outcome,
		[]byte(fmt.Sprintf(`{"topup_id":%q,"amount":%d}`, cb.TopupID, cb.Amount)))
	if err != nil {
		return false, fmt.Errorf("txn: record callback: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Service) failTopup(ctx context.Context, topupID, reason string) (*Topup, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE topups SET status = 'failed', updated_at = now()
		 WHERE id = $1 AND status = 'pending'`, topupID)
	if err != nil {
		return nil, fmt.Errorf("txn: fail topup: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if err := state.RecordTx(ctx, tx, state.KindTopup, topupID,
			"pending", "failed", state.ActorProvider, reason); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.loadTopup(ctx, topupID)
}

func (s *Service) loadTopup(ctx context.Context, topupID string) (*Topup, error) {
	var t Topup
	var ledgerTx *string

	err := s.pool.QueryRow(ctx,
		`SELECT id, status, source, provider_code, COALESCE(va_number, ''),
		        amount_minor, admin_fee_minor, ledger_transaction_id, created_at
		 FROM topups WHERE id = $1`,
		topupID).Scan(&t.ID, &t.Status, &t.Source, &t.ProviderCode, &t.VirtualAccount,
		&t.Amount, &t.AdminFee, &ledgerTx, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Top up %s was not found.", topupID)
	}
	if err != nil {
		return nil, fmt.Errorf("txn: load topup: %w", err)
	}

	if ledgerTx != nil {
		t.LedgerTransactionID = *ledgerTx
	}
	t.TotalPayable = t.Amount + t.AdminFee
	t.Currency = "IDR"
	t.CreatedAt = utc(t.CreatedAt)
	return &t, nil
}

func virtualAccount(providerCode, userID string) string {
	suffix := userID
	if len(suffix) > 10 {
		suffix = suffix[len(suffix)-10:]
	}
	return fmt.Sprintf("%s%s", prefixFor(providerCode), digitsOnly(suffix))
}

func prefixFor(providerCode string) string {
	switch providerCode {
	case "BCA":
		return "39011"
	case "MANDIRI":
		return "88808"
	case "BNI":
		return "98810"
	case "BRI":
		return "26215"
	default:
		return "10000"
	}
}

func digitsOnly(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
			continue
		}
		out = append(out, byte('0'+int(s[i])%10))
	}
	return string(out)
}

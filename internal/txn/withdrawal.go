package txn

import (
	"context"
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

const WithdrawalAdminFee = money.Minor(250_000)

var supportedBanks = map[string]string{
	"BCA":     "Bank Central Asia",
	"MANDIRI": "Bank Mandiri",
	"BNI":     "Bank Negara Indonesia",
	"BRI":     "Bank Rakyat Indonesia",
	"CIMB":    "CIMB Niaga",
	"PERMATA": "Bank Permata",
	"DANAMON": "Bank Danamon",
	"BSI":     "Bank Syariah Indonesia",
	"JAGO":    "Bank Jago",
	"SEABANK": "SeaBank",
	"BTPN":    "BTPN Jenius",
	"OCBC":    "OCBC NISP",
}

type WithdrawalRequest struct {
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
}

type Withdrawal struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	Amount              int64     `json:"amount"`
	AdminFee            int64     `json:"admin_fee"`
	TotalDebited        int64     `json:"total_debited"`
	Currency            string    `json:"currency"`
	BankCode            string    `json:"bank_code"`
	BankName            string    `json:"bank_name"`
	AccountNumber       string    `json:"account_number"`
	LedgerTransactionID string    `json:"ledger_transaction_id"`
	BalanceAfter        int64     `json:"balance_after"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) CreateWithdrawal(ctx context.Context, userID string, req WithdrawalRequest) (*Withdrawal, error) {
	if err := invalidAmount(req.Amount); err != nil {
		return nil, err
	}
	if err := invalidCurrency(req.Currency); err != nil {
		return nil, err
	}
	if err := s.assertActive(ctx, userID); err != nil {
		return nil, err
	}

	bankName, known := supportedBanks[req.BankCode]
	if !known {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Bank code %q is not supported.", req.BankCode).
			WithDetails(map[string]any{"supported": bankCodes()})
	}
	if req.AccountNumber == "" || req.AccountName == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Fields account_number and account_name are required.")
	}

	amount := money.Minor(req.Amount)
	total := amount + WithdrawalAdminFee

	limit, err := s.userLimits(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := overLimit(amount, limit); err != nil {
		return nil, err
	}

	if err := s.checkVelocity(ctx, velocity.Subject{
		UserID: userID, Kind: velocity.KindWithdrawal, Amount: amount,
	}); err != nil {
		return nil, err
	}

	walletAccount := ledger.UserWallet(userID)
	clearingAccount := ledger.AccountID(ledger.OwnerProvider, req.BankCode, ledger.TypeClearing)

	if err := s.ledger.EnsureAccount(ctx, clearingAccount,
		ledger.OwnerProvider, req.BankCode, ledger.TypeClearing); err != nil {
		return nil, err
	}

	balanceAfter, err := s.reserve(ctx, walletAccount, total)
	if err != nil {
		return nil, err
	}

	withdrawalID := id.New("wd")
	txID := id.New("txn")

	posting, err := ledger.NewWithdrawal(ledger.Withdrawal{
		TransactionID:     txID,
		ReferenceID:       withdrawalID,
		WalletAccountID:   walletAccount,
		ClearingAccountID: clearingAccount,
		FeeAccountID:      ledger.PlatformFeeRevenue(),
		Amount:            amount,
		AdminFee:          WithdrawalAdminFee,
	})
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	result := &Withdrawal{
		ID:                  withdrawalID,
		Status:              "pending",
		Amount:              int64(amount),
		AdminFee:            int64(WithdrawalAdminFee),
		TotalDebited:        int64(total),
		Currency:            "IDR",
		BankCode:            req.BankCode,
		BankName:            bankName,
		AccountNumber:       req.AccountNumber,
		LedgerTransactionID: txID,
		BalanceAfter:        int64(balanceAfter),
	}

	msg, err := event(outbox.TopicPaymentEvents, userID, "withdrawal.created", result)
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	err = s.commit(ctx, posting, msg, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO withdrawals
			   (id, user_id, bank_code, account_number, account_name,
			    amount_minor, admin_fee_minor, status, ledger_transaction_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8)
			 RETURNING created_at`,
			withdrawalID, userID, req.BankCode, req.AccountNumber, req.AccountName,
			int64(amount), int64(WithdrawalAdminFee), txID).Scan(&result.CreatedAt)
	})
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	result.CreatedAt = utc(result.CreatedAt)
	return result, nil
}

func bankCodes() []string {
	codes := make([]string, 0, len(supportedBanks))
	for code := range supportedBanks {
		codes = append(codes, code)
	}
	return codes
}

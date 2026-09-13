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
	"github.com/umars28/marspay/internal/rail"
)

type Biller struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Category string `json:"category"`
	AdminFee int64  `json:"admin_fee"`
	Prepaid  bool   `json:"prepaid"`
}

var billers = map[string]Biller{
	"PLN_POSTPAID": {Code: "PLN_POSTPAID", Name: "PLN Postpaid", Category: "electricity", AdminFee: 200_000},
	"PLN_PREPAID":  {Code: "PLN_PREPAID", Name: "PLN Token", Category: "electricity", AdminFee: 150_000, Prepaid: true},
	"TELKOMSEL":    {Code: "TELKOMSEL", Name: "Telkomsel", Category: "airtime", AdminFee: 0, Prepaid: true},
	"INDIHOME":     {Code: "INDIHOME", Name: "IndiHome", Category: "internet", AdminFee: 200_000},
	"BPJS_KES":     {Code: "BPJS_KES", Name: "BPJS Kesehatan", Category: "insurance", AdminFee: 250_000},
	"PDAM_JAKARTA": {Code: "PDAM_JAKARTA", Name: "PDAM Jakarta", Category: "water", AdminFee: 250_000},
}

func Billers() []Biller {
	out := make([]Biller, 0, len(billers))
	for _, b := range billers {
		out = append(out, b)
	}
	return out
}

type InquiryRequest struct {
	CustomerRef string `json:"customer_ref"`
}

type Inquiry struct {
	BillerCode   string `json:"biller_code"`
	BillerName   string `json:"biller_name"`
	CustomerRef  string `json:"customer_ref"`
	CustomerName string `json:"customer_name"`
	Period       string `json:"period,omitempty"`
	Amount       int64  `json:"amount"`
	AdminFee     int64  `json:"admin_fee"`
	TotalPayable int64  `json:"total_payable"`
	Currency     string `json:"currency"`
}

func (s *Service) Inquire(ctx context.Context, billerCode string, req InquiryRequest) (*Inquiry, error) {
	biller, known := billers[billerCode]
	if !known {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Biller %s was not found.", billerCode)
	}
	if req.CustomerRef == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field customer_ref is required.")
	}

	amount := syntheticBill(biller, req.CustomerRef)

	return &Inquiry{
		BillerCode:   biller.Code,
		BillerName:   biller.Name,
		CustomerRef:  req.CustomerRef,
		CustomerName: "MARSPAY CUSTOMER",
		Period:       billingPeriod(biller),
		Amount:       int64(amount),
		AdminFee:     biller.AdminFee,
		TotalPayable: int64(amount) + biller.AdminFee,
		Currency:     "IDR",
	}, nil
}

type BillPaymentRequest struct {
	BillerCode  string `json:"biller_code"`
	CustomerRef string `json:"customer_ref"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
}

type BillPayment struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	BillerCode          string    `json:"biller_code"`
	BillerName          string    `json:"biller_name"`
	CustomerRef         string    `json:"customer_ref"`
	Amount              int64     `json:"amount"`
	AdminFee            int64     `json:"admin_fee"`
	TotalDebited        int64     `json:"total_debited"`
	Currency            string    `json:"currency"`
	ProviderRef         string    `json:"provider_ref,omitempty"`
	LedgerTransactionID string    `json:"ledger_transaction_id"`
	BalanceAfter        int64     `json:"balance_after"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) PayBill(ctx context.Context, userID string, req BillPaymentRequest) (*BillPayment, error) {
	if err := invalidAmount(req.Amount); err != nil {
		return nil, err
	}
	if err := invalidCurrency(req.Currency); err != nil {
		return nil, err
	}
	if err := s.assertActive(ctx, userID); err != nil {
		return nil, err
	}

	biller, known := billers[req.BillerCode]
	if !known {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Biller %s was not found.", req.BillerCode)
	}
	if req.CustomerRef == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field customer_ref is required.")
	}

	amount := money.Minor(req.Amount)
	adminFee := money.Minor(biller.AdminFee)
	total := amount + adminFee

	limit, err := s.userLimits(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := overLimit(amount, limit); err != nil {
		return nil, err
	}

	walletAccount := ledger.UserWallet(userID)
	clearingAccount := ledger.AccountID(ledger.OwnerProvider, biller.Code, ledger.TypeClearing)

	if err := s.ledger.EnsureAccount(ctx, clearingAccount,
		ledger.OwnerProvider, biller.Code, ledger.TypeClearing); err != nil {
		return nil, err
	}

	balanceAfter, err := s.reserve(ctx, walletAccount, total)
	if err != nil {
		return nil, err
	}

	billID := id.New("bil")
	txID := id.New("txn")

	posting, err := ledger.NewBillPayment(ledger.BillPayment{
		TransactionID:     txID,
		ReferenceID:       billID,
		WalletAccountID:   walletAccount,
		ClearingAccountID: clearingAccount,
		FeeAccountID:      ledger.PlatformFeeRevenue(),
		Amount:            amount,
		AdminFee:          adminFee,
	})
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	status, providerRef, sendErr := s.callBiller(ctx, billID, biller.Code, amount)

	result := &BillPayment{
		ID:                  billID,
		Status:              status,
		BillerCode:          biller.Code,
		BillerName:          biller.Name,
		CustomerRef:         req.CustomerRef,
		Amount:              int64(amount),
		AdminFee:            int64(adminFee),
		TotalDebited:        int64(total),
		Currency:            "IDR",
		ProviderRef:         providerRef,
		LedgerTransactionID: txID,
		BalanceAfter:        int64(balanceAfter),
	}

	if status == "failed" {
		if _, releaseErr := s.wallet.Release(ctx, walletAccount, total); releaseErr != nil {
			return nil, releaseErr
		}
		if err := s.recordFailedBill(ctx, billID, userID, biller, req, amount, adminFee, sendErr); err != nil {
			return nil, err
		}
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeConflict,
			"The biller rejected this payment: %s", sendErr)
	}

	msg, err := event(outbox.TopicPaymentEvents, userID, "bill_payment."+status, result)
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	err = s.commit(ctx, posting, msg, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO bill_payments
			   (id, user_id, biller_code, customer_ref, customer_name, period,
			    amount_minor, admin_fee_minor, status, provider_ref, ledger_transaction_id)
			 VALUES ($1, $2, $3, $4, 'MARSPAY CUSTOMER', $5, $6, $7, $8, NULLIF($9, ''), $10)
			 RETURNING created_at`,
			billID, userID, biller.Code, req.CustomerRef, billingPeriod(biller),
			int64(amount), int64(adminFee), status, providerRef, txID).Scan(&result.CreatedAt)
	})
	if err != nil {
		return nil, s.giveBack(ctx, walletAccount, total, err)
	}

	result.CreatedAt = utc(result.CreatedAt)
	return result, nil
}

func (s *Service) callBiller(ctx context.Context, billID, billerCode string, amount money.Minor) (string, string, error) {
	if s.billerRail == nil {
		return "succeeded", id.New("prov"), nil
	}

	result, err := s.billerRail.Send(ctx, rail.Request{
		PayoutID:  billID,
		BankCode:  billerCode,
		AccountNo: billerCode,
		Amount:    amount,
	})

	switch {
	case err == nil:
		return "succeeded", result.BankReference, nil
	case errors.Is(err, rail.ErrAmbiguous), errors.Is(err, rail.ErrTimeout):
		return "pending", result.BankReference, err
	default:
		return "failed", result.BankReference, err
	}
}

func (s *Service) recordFailedBill(ctx context.Context, billID, userID string, biller Biller,
	req BillPaymentRequest, amount, adminFee money.Minor, cause error) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO bill_payments
		   (id, user_id, biller_code, customer_ref, period,
		    amount_minor, admin_fee_minor, status, failure_reason)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'failed', $8)`,
		billID, userID, biller.Code, req.CustomerRef, billingPeriod(biller),
		int64(amount), int64(adminFee), cause.Error())
	if err != nil {
		return fmt.Errorf("txn: record failed bill: %w", err)
	}
	return nil
}

func syntheticBill(b Biller, customerRef string) money.Minor {
	if b.Prepaid {
		return 0
	}

	var seed int
	for i := 0; i < len(customerRef); i++ {
		seed = seed*31 + int(customerRef[i])
		if seed < 0 {
			seed = -seed
		}
	}
	return money.Minor(5_000_00 + (seed%9_500)*100_00)
}

func billingPeriod(b Biller) string {
	if b.Prepaid {
		return ""
	}
	return time.Now().AddDate(0, -1, 0).Format("2006-01")
}

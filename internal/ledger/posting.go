package ledger

import (
	"errors"
	"fmt"

	"github.com/umars28/marspay/internal/money"
)

type Kind string

const (
	KindTopup           Kind = "topup"
	KindPayment         Kind = "payment"
	KindTransfer        Kind = "transfer"
	KindWithdrawal      Kind = "withdrawal"
	KindBillPayment     Kind = "bill_payment"
	KindRefund          Kind = "refund"
	KindReversal        Kind = "reversal"
	KindPayout          Kind = "payout"
	KindHoldbackRelease Kind = "holdback_release"
	KindAdjustment      Kind = "adjustment"
)

var (
	ErrUnbalanced    = errors.New("ledger: entries do not sum to zero")
	ErrZeroEntry     = errors.New("ledger: entry amount must not be zero")
	ErrTooFewEntry   = errors.New("ledger: a posting needs at least two entries")
	ErrNoAccount     = errors.New("ledger: entry must reference an account")
	ErrNoTransaction = errors.New("ledger: posting must have a transaction id")
)

type Entry struct {
	AccountID string
	Amount    money.Minor
}

type Posting struct {
	TransactionID string
	Kind          Kind
	ReferenceID   string
	Description   string
	Entries       []Entry
}

func (p Posting) Sum() money.Minor {
	var total money.Minor
	for _, e := range p.Entries {
		total += e.Amount
	}
	return total
}

func (p Posting) Validate() error {
	if p.TransactionID == "" {
		return ErrNoTransaction
	}
	if len(p.Entries) < 2 {
		return fmt.Errorf("%w: got %d", ErrTooFewEntry, len(p.Entries))
	}
	for i, e := range p.Entries {
		if e.AccountID == "" {
			return fmt.Errorf("%w: entry %d", ErrNoAccount, i)
		}
		if e.Amount == 0 {
			return fmt.Errorf("%w: entry %d on account %s", ErrZeroEntry, i, e.AccountID)
		}
	}
	if sum := p.Sum(); sum != 0 {
		return fmt.Errorf("%w: sum = %d", ErrUnbalanced, sum)
	}
	return nil
}

type MerchantPayment struct {
	TransactionID    string
	ReferenceID      string
	PayerAccountID   string
	PayableAccountID string
	FeeAccountID     string
	Amount           money.Minor
	FeeBps           int
}

func NewMerchantPayment(p MerchantPayment) (Posting, error) {
	if p.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, p.Amount)
	}

	fee, err := money.FeeHalfUp(p.Amount, p.FeeBps)
	if err != nil {
		return Posting{}, err
	}

	entries := []Entry{
		{AccountID: p.PayerAccountID, Amount: -p.Amount},
		{AccountID: p.PayableAccountID, Amount: p.Amount - fee},
	}
	if fee > 0 {
		entries = append(entries, Entry{AccountID: p.FeeAccountID, Amount: fee})
	}

	posting := Posting{
		TransactionID: p.TransactionID,
		Kind:          KindPayment,
		ReferenceID:   p.ReferenceID,
		Entries:       entries,
	}
	if err := posting.Validate(); err != nil {
		return Posting{}, err
	}
	return posting, nil
}

type InstantPayout struct {
	TransactionID     string
	ReferenceID       string
	PayableAccountID  string
	HoldbackAccountID string
	ClearingAccountID string
	Gross             money.Minor
	HoldbackBps       int
}

func NewInstantPayout(p InstantPayout) (Posting, error) {
	if p.Gross <= 0 {
		return Posting{}, fmt.Errorf("%w: gross %d", money.ErrNegativeAmount, p.Gross)
	}

	holdback, err := money.FeeHalfUp(p.Gross, p.HoldbackBps)
	if err != nil {
		return Posting{}, err
	}

	entries := []Entry{
		{AccountID: p.PayableAccountID, Amount: -p.Gross},
		{AccountID: p.ClearingAccountID, Amount: p.Gross - holdback},
	}
	if holdback > 0 {
		entries = append(entries, Entry{AccountID: p.HoldbackAccountID, Amount: holdback})
	}

	posting := Posting{
		TransactionID: p.TransactionID,
		Kind:          KindPayout,
		ReferenceID:   p.ReferenceID,
		Entries:       entries,
	}
	if err := posting.Validate(); err != nil {
		return Posting{}, err
	}
	return posting, nil
}

func Reverse(original Posting, transactionID, reason string) (Posting, error) {
	entries := make([]Entry, 0, len(original.Entries))
	for _, e := range original.Entries {
		entries = append(entries, Entry{AccountID: e.AccountID, Amount: -e.Amount})
	}

	posting := Posting{
		TransactionID: transactionID,
		Kind:          KindReversal,
		ReferenceID:   original.TransactionID,
		Description:   reason,
		Entries:       entries,
	}
	if err := posting.Validate(); err != nil {
		return Posting{}, err
	}
	return posting, nil
}

package ledger

import (
	"fmt"

	"github.com/umars28/marspay/internal/money"
)

type Transfer struct {
	TransactionID  string
	ReferenceID    string
	PayerAccountID string
	PayeeAccountID string
	Amount         money.Minor
}

func NewTransfer(t Transfer) (Posting, error) {
	if t.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, t.Amount)
	}
	if t.PayerAccountID == t.PayeeAccountID {
		return Posting{}, fmt.Errorf("ledger: a transfer needs two different accounts")
	}

	return build(Posting{
		TransactionID: t.TransactionID,
		Kind:          KindTransfer,
		ReferenceID:   t.ReferenceID,
		Entries: []Entry{
			{AccountID: t.PayerAccountID, Amount: -t.Amount},
			{AccountID: t.PayeeAccountID, Amount: t.Amount},
		},
	})
}

type Topup struct {
	TransactionID     string
	ReferenceID       string
	ClearingAccountID string
	WalletAccountID   string
	FeeAccountID      string
	Amount            money.Minor
	AdminFee          money.Minor
}

func NewTopup(t Topup) (Posting, error) {
	if t.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, t.Amount)
	}
	if t.AdminFee < 0 {
		return Posting{}, fmt.Errorf("%w: admin fee %d", money.ErrNegativeAmount, t.AdminFee)
	}

	entries := []Entry{
		{AccountID: t.ClearingAccountID, Amount: -(t.Amount + t.AdminFee)},
		{AccountID: t.WalletAccountID, Amount: t.Amount},
	}
	if t.AdminFee > 0 {
		entries = append(entries, Entry{AccountID: t.FeeAccountID, Amount: t.AdminFee})
	}

	return build(Posting{
		TransactionID: t.TransactionID,
		Kind:          KindTopup,
		ReferenceID:   t.ReferenceID,
		Entries:       entries,
	})
}

type Withdrawal struct {
	TransactionID     string
	ReferenceID       string
	WalletAccountID   string
	ClearingAccountID string
	FeeAccountID      string
	Amount            money.Minor
	AdminFee          money.Minor
}

func NewWithdrawal(w Withdrawal) (Posting, error) {
	if w.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, w.Amount)
	}
	if w.AdminFee < 0 {
		return Posting{}, fmt.Errorf("%w: admin fee %d", money.ErrNegativeAmount, w.AdminFee)
	}

	entries := []Entry{
		{AccountID: w.WalletAccountID, Amount: -(w.Amount + w.AdminFee)},
		{AccountID: w.ClearingAccountID, Amount: w.Amount},
	}
	if w.AdminFee > 0 {
		entries = append(entries, Entry{AccountID: w.FeeAccountID, Amount: w.AdminFee})
	}

	return build(Posting{
		TransactionID: w.TransactionID,
		Kind:          KindWithdrawal,
		ReferenceID:   w.ReferenceID,
		Entries:       entries,
	})
}

type BillPayment struct {
	TransactionID     string
	ReferenceID       string
	WalletAccountID   string
	ClearingAccountID string
	FeeAccountID      string
	Amount            money.Minor
	AdminFee          money.Minor
}

func NewBillPayment(b BillPayment) (Posting, error) {
	posting, err := NewWithdrawal(Withdrawal{
		TransactionID:     b.TransactionID,
		ReferenceID:       b.ReferenceID,
		WalletAccountID:   b.WalletAccountID,
		ClearingAccountID: b.ClearingAccountID,
		FeeAccountID:      b.FeeAccountID,
		Amount:            b.Amount,
		AdminFee:          b.AdminFee,
	})
	if err != nil {
		return Posting{}, err
	}
	posting.Kind = KindBillPayment
	return posting, nil
}

type Refund struct {
	TransactionID    string
	ReferenceID      string
	PayableAccountID string
	WalletAccountID  string
	FeeAccountID     string
	Amount           money.Minor
	FeeReturned      money.Minor
}

func NewRefund(r Refund) (Posting, error) {
	if r.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, r.Amount)
	}
	if r.FeeReturned < 0 {
		return Posting{}, fmt.Errorf("%w: fee %d", money.ErrNegativeAmount, r.FeeReturned)
	}
	if r.FeeReturned > r.Amount {
		return Posting{}, fmt.Errorf("ledger: refunded fee %d exceeds the refund %d",
			r.FeeReturned, r.Amount)
	}

	entries := []Entry{
		{AccountID: r.PayableAccountID, Amount: -(r.Amount - r.FeeReturned)},
		{AccountID: r.WalletAccountID, Amount: r.Amount},
	}
	if r.FeeReturned > 0 {
		entries = append(entries, Entry{AccountID: r.FeeAccountID, Amount: -r.FeeReturned})
	}

	return build(Posting{
		TransactionID: r.TransactionID,
		Kind:          KindRefund,
		ReferenceID:   r.ReferenceID,
		Entries:       entries,
	})
}

func ProportionalFee(paidAmount, paidFee, refundAmount money.Minor) (money.Minor, error) {
	if paidAmount <= 0 {
		return 0, fmt.Errorf("%w: paid amount %d", money.ErrNegativeAmount, paidAmount)
	}
	if refundAmount <= 0 || refundAmount > paidAmount {
		return 0, fmt.Errorf("ledger: refund %d is not within the paid amount %d",
			refundAmount, paidAmount)
	}

	product := int64(paidFee) * int64(refundAmount)
	fee := product / int64(paidAmount)
	if remainder := product % int64(paidAmount); remainder*2 >= int64(paidAmount) {
		fee++
	}
	return money.Minor(fee), nil
}

func build(p Posting) (Posting, error) {
	if err := p.Validate(); err != nil {
		return Posting{}, err
	}
	return p, nil
}

type DisputeRefund struct {
	TransactionID     string
	ReferenceID       string
	HoldbackAccountID string
	FloatAccountID    string
	WalletAccountID   string
	Amount            money.Minor
	CoveredByHoldback money.Minor
}

func NewDisputeRefund(d DisputeRefund) (Posting, error) {
	if d.Amount <= 0 {
		return Posting{}, fmt.Errorf("%w: amount %d", money.ErrNegativeAmount, d.Amount)
	}
	if d.CoveredByHoldback < 0 || d.CoveredByHoldback > d.Amount {
		return Posting{}, fmt.Errorf("ledger: holdback cover %d is not within the dispute %d",
			d.CoveredByHoldback, d.Amount)
	}

	entries := []Entry{{AccountID: d.WalletAccountID, Amount: d.Amount}}
	if d.CoveredByHoldback > 0 {
		entries = append(entries, Entry{
			AccountID: d.HoldbackAccountID, Amount: -d.CoveredByHoldback,
		})
	}
	if loss := d.Amount - d.CoveredByHoldback; loss > 0 {
		entries = append(entries, Entry{AccountID: d.FloatAccountID, Amount: -loss})
	}

	return build(Posting{
		TransactionID: d.TransactionID,
		Kind:          KindAdjustment,
		ReferenceID:   d.ReferenceID,
		Description:   "dispute resolved for the user",
		Entries:       entries,
	})
}

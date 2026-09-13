package ledger

import (
	"errors"
	"testing"

	"github.com/umars28/marspay/internal/money"
)

func TestMerchantPaymentBalances(t *testing.T) {
	p, err := NewMerchantPayment(MerchantPayment{
		TransactionID:    "txn_1",
		PayerAccountID:   "acc_wallet",
		PayableAccountID: "acc_payable",
		FeeAccountID:     "acc_fee",
		Amount:           money.FromRupiah(32_000),
		FeeBps:           70,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := len(p.Entries); got != 3 {
		t.Fatalf("entries = %d, want 3", got)
	}
	if sum := p.Sum(); sum != 0 {
		t.Errorf("sum = %d, want 0", sum)
	}
	if got := p.Entries[0].Amount; got != -3_200_000 {
		t.Errorf("payer debit = %d, want -3200000", got)
	}
	if got := p.Entries[1].Amount; got != 3_177_600 {
		t.Errorf("merchant credit = %d, want 3177600", got)
	}
	if got := p.Entries[2].Amount; got != 22_400 {
		t.Errorf("fee credit = %d, want 22400", got)
	}
}

func TestMerchantPaymentAlwaysBalancesIncludingRounding(t *testing.T) {
	for amount := money.Minor(1); amount < 500_000; amount += 37 {
		for _, bps := range []int{0, 1, 70, 250, 999} {
			p, err := NewMerchantPayment(MerchantPayment{
				TransactionID:    "txn_sweep",
				PayerAccountID:   "acc_wallet",
				PayableAccountID: "acc_payable",
				FeeAccountID:     "acc_fee",
				Amount:           amount,
				FeeBps:           bps,
			})
			if err != nil {
				t.Fatalf("amount %d bps %d: %v", amount, bps, err)
			}
			if sum := p.Sum(); sum != 0 {
				t.Fatalf("amount %d bps %d: sum = %d, want 0", amount, bps, sum)
			}
		}
	}
}

func TestMerchantPaymentRejectsNonPositiveAmount(t *testing.T) {
	for _, amount := range []money.Minor{0, -1, -3_200_000} {
		_, err := NewMerchantPayment(MerchantPayment{
			TransactionID:    "txn_bad",
			PayerAccountID:   "acc_wallet",
			PayableAccountID: "acc_payable",
			FeeAccountID:     "acc_fee",
			Amount:           amount,
			FeeBps:           70,
		})
		if !errors.Is(err, money.ErrNegativeAmount) {
			t.Errorf("amount %d: got %v, want ErrNegativeAmount", amount, err)
		}
	}
}

func TestInstantPayoutSplitsHoldback(t *testing.T) {
	p, err := NewInstantPayout(InstantPayout{
		TransactionID:     "txn_po",
		PayableAccountID:  "acc_payable",
		HoldbackAccountID: "acc_holdback",
		ClearingAccountID: "acc_clearing",
		Gross:             3_177_600,
		HoldbackBps:       400,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum := p.Sum(); sum != 0 {
		t.Fatalf("sum = %d, want 0", sum)
	}

	var holdback, clearing money.Minor
	for _, e := range p.Entries {
		switch e.AccountID {
		case "acc_holdback":
			holdback = e.Amount
		case "acc_clearing":
			clearing = e.Amount
		}
	}
	if holdback != 127_104 {
		t.Errorf("holdback = %d, want 127104", holdback)
	}
	if clearing != 3_050_496 {
		t.Errorf("clearing = %d, want 3050496", clearing)
	}
	if holdback+clearing != 3_177_600 {
		t.Errorf("holdback + clearing = %d, want gross 3177600", holdback+clearing)
	}
}

func TestValidateRejectsUnbalanced(t *testing.T) {
	p := Posting{
		TransactionID: "txn_bad",
		Kind:          KindPayment,
		Entries: []Entry{
			{AccountID: "acc_wallet", Amount: -3_200_000},
			{AccountID: "acc_payable", Amount: 3_100_000},
		},
	}
	if err := p.Validate(); !errors.Is(err, ErrUnbalanced) {
		t.Errorf("got %v, want ErrUnbalanced", err)
	}
}

func TestValidateRejectsMalformedPostings(t *testing.T) {
	cases := []struct {
		name string
		in   Posting
		want error
	}{
		{
			"no transaction id",
			Posting{Entries: []Entry{{AccountID: "a", Amount: 1}, {AccountID: "b", Amount: -1}}},
			ErrNoTransaction,
		},
		{
			"single entry",
			Posting{TransactionID: "t", Entries: []Entry{{AccountID: "a", Amount: 0}}},
			ErrTooFewEntry,
		},
		{
			"missing account",
			Posting{TransactionID: "t", Entries: []Entry{{Amount: 1}, {AccountID: "b", Amount: -1}}},
			ErrNoAccount,
		},
		{
			"zero amount entry",
			Posting{TransactionID: "t", Entries: []Entry{{AccountID: "a", Amount: 0}, {AccountID: "b", Amount: 0}}},
			ErrZeroEntry,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.in.Validate(); !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestReverseUndoesOriginalExactly(t *testing.T) {
	original, err := NewMerchantPayment(MerchantPayment{
		TransactionID:    "txn_1",
		PayerAccountID:   "acc_wallet",
		PayableAccountID: "acc_payable",
		FeeAccountID:     "acc_fee",
		Amount:           money.FromRupiah(75_000),
		FeeBps:           70,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reversal, err := Reverse(original, "txn_2", "duplicate charge reported")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if reversal.Kind != KindReversal {
		t.Errorf("kind = %q, want %q", reversal.Kind, KindReversal)
	}
	if reversal.ReferenceID != original.TransactionID {
		t.Errorf("reference = %q, want %q", reversal.ReferenceID, original.TransactionID)
	}

	net := map[string]money.Minor{}
	for _, e := range original.Entries {
		net[e.AccountID] += e.Amount
	}
	for _, e := range reversal.Entries {
		net[e.AccountID] += e.Amount
	}
	for account, amount := range net {
		if amount != 0 {
			t.Errorf("account %s left at %d after reversal, want 0", account, amount)
		}
	}
}

package ledger

import (
	"errors"
	"testing"

	"github.com/umars28/marspay/internal/money"
)

func mustBalance(t *testing.T, p Posting, kind Kind) {
	t.Helper()
	if p.Kind != kind {
		t.Errorf("kind = %q, want %q", p.Kind, kind)
	}
	if sum := p.Sum(); sum != 0 {
		t.Errorf("sum = %d, want 0", sum)
	}
}

func amountOn(p Posting, account string) money.Minor {
	var total money.Minor
	for _, e := range p.Entries {
		if e.AccountID == account {
			total += e.Amount
		}
	}
	return total
}

func TestTransferMovesTheWholeAmount(t *testing.T) {
	p, err := NewTransfer(Transfer{
		TransactionID:  "txn_1",
		PayerAccountID: "acc_a",
		PayeeAccountID: "acc_b",
		Amount:         money.FromRupiah(150_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindTransfer)
	if got := amountOn(p, "acc_a"); got != -15_000_000 {
		t.Errorf("payer = %d, want -15000000", got)
	}
	if got := amountOn(p, "acc_b"); got != 15_000_000 {
		t.Errorf("payee = %d, want 15000000", got)
	}
}

func TestTransferToYourselfIsRejected(t *testing.T) {
	_, err := NewTransfer(Transfer{
		TransactionID:  "txn_1",
		PayerAccountID: "acc_a",
		PayeeAccountID: "acc_a",
		Amount:         100,
	})
	if err == nil {
		t.Error("a transfer between one account and itself was accepted")
	}
}

func TestTopupCreditsTheWalletAndChargesTheFeeToTheSource(t *testing.T) {
	p, err := NewTopup(Topup{
		TransactionID:     "txn_1",
		ClearingAccountID: "acc_clearing",
		WalletAccountID:   "acc_wallet",
		FeeAccountID:      "acc_fee",
		Amount:            money.FromRupiah(500_000),
		AdminFee:          money.FromRupiah(2_500),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindTopup)
	if got := amountOn(p, "acc_wallet"); got != 50_000_000 {
		t.Errorf("wallet credit = %d, want 50000000: the user receives the full amount", got)
	}
	if got := amountOn(p, "acc_clearing"); got != -50_250_000 {
		t.Errorf("clearing = %d, want -50250000: the source covers amount plus fee", got)
	}
	if got := amountOn(p, "acc_fee"); got != 250_000 {
		t.Errorf("fee = %d, want 250000", got)
	}
}

func TestFreeTopupHasNoFeeEntry(t *testing.T) {
	p, err := NewTopup(Topup{
		TransactionID:     "txn_1",
		ClearingAccountID: "acc_clearing",
		WalletAccountID:   "acc_wallet",
		FeeAccountID:      "acc_fee",
		Amount:            money.FromRupiah(500_000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindTopup)
	if len(p.Entries) != 2 {
		t.Errorf("entries = %d, want 2: a zero fee must not create a zero-amount entry", len(p.Entries))
	}
}

func TestWithdrawalDebitsAmountPlusFee(t *testing.T) {
	p, err := NewWithdrawal(Withdrawal{
		TransactionID:     "txn_1",
		WalletAccountID:   "acc_wallet",
		ClearingAccountID: "acc_clearing",
		FeeAccountID:      "acc_fee",
		Amount:            money.FromRupiah(1_000_000),
		AdminFee:          money.FromRupiah(2_500),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindWithdrawal)
	if got := amountOn(p, "acc_wallet"); got != -100_250_000 {
		t.Errorf("wallet = %d, want -100250000", got)
	}
	if got := amountOn(p, "acc_clearing"); got != 100_000_000 {
		t.Errorf("clearing = %d, want 100000000: the bank receives the amount, not the fee", got)
	}
}

func TestBillPaymentIsAWithdrawalWithItsOwnKind(t *testing.T) {
	p, err := NewBillPayment(BillPayment{
		TransactionID:     "txn_1",
		WalletAccountID:   "acc_wallet",
		ClearingAccountID: "acc_biller",
		FeeAccountID:      "acc_fee",
		Amount:            41_030_000,
		AdminFee:          200_000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindBillPayment)
	if got := amountOn(p, "acc_wallet"); got != -41_230_000 {
		t.Errorf("wallet = %d, want -41230000", got)
	}
}

func TestFullRefundReturnsTheFeeToo(t *testing.T) {
	p, err := NewRefund(Refund{
		TransactionID:    "txn_1",
		PayableAccountID: "acc_payable",
		WalletAccountID:  "acc_wallet",
		FeeAccountID:     "acc_fee",
		Amount:           3_200_000,
		FeeReturned:      22_400,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mustBalance(t, p, KindRefund)
	if got := amountOn(p, "acc_wallet"); got != 3_200_000 {
		t.Errorf("wallet = %d, want the full 3200000 back", got)
	}
	if got := amountOn(p, "acc_payable"); got != -3_177_600 {
		t.Errorf("merchant = %d, want -3177600", got)
	}
	if got := amountOn(p, "acc_fee"); got != -22_400 {
		t.Errorf("fee = %d, want -22400: the platform gives its fee back", got)
	}
}

func TestRefundCannotReturnMoreFeeThanTheRefundItself(t *testing.T) {
	_, err := NewRefund(Refund{
		TransactionID:    "txn_1",
		PayableAccountID: "acc_payable",
		WalletAccountID:  "acc_wallet",
		FeeAccountID:     "acc_fee",
		Amount:           1_000,
		FeeReturned:      2_000,
	})
	if err == nil {
		t.Error("a refund returning more fee than money was accepted")
	}
}

func TestProportionalFeeSplitsCorrectly(t *testing.T) {
	cases := []struct {
		name                           string
		paid, paidFee, refund, wantFee money.Minor
	}{
		{"full refund returns the whole fee", 3_200_000, 22_400, 3_200_000, 22_400},
		{"half refund returns half the fee", 3_200_000, 22_400, 1_600_000, 11_200},
		{"quarter refund", 4_000_000, 28_000, 1_000_000, 7_000},
		{"rounds half up", 3_000_000, 21_000, 1_000_001, 7_000},
		{"zero fee stays zero", 3_200_000, 0, 1_000_000, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ProportionalFee(c.paid, c.paidFee, c.refund)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.wantFee {
				t.Errorf("ProportionalFee(%d, %d, %d) = %d, want %d",
					c.paid, c.paidFee, c.refund, got, c.wantFee)
			}
		})
	}
}

func TestProportionalFeeNeverExceedsTheOriginalFee(t *testing.T) {
	const paid, paidFee = money.Minor(3_200_000), money.Minor(22_400)

	for refund := money.Minor(1); refund <= paid; refund += 7_919 {
		got, err := ProportionalFee(paid, paidFee, refund)
		if err != nil {
			t.Fatalf("refund %d: %v", refund, err)
		}
		if got > paidFee {
			t.Fatalf("refund %d returned fee %d, more than the %d originally charged",
				refund, got, paidFee)
		}
	}
}

func TestProportionalFeeRejectsARefundLargerThanThePayment(t *testing.T) {
	if _, err := ProportionalFee(1_000, 7, 1_001); err == nil {
		t.Error("a refund larger than the payment was accepted")
	}
	if _, err := ProportionalFee(0, 0, 1); err == nil {
		t.Error("a refund against a zero payment was accepted")
	}
}

func TestEveryMovementRejectsANonPositiveAmount(t *testing.T) {
	for _, amount := range []money.Minor{0, -1} {
		if _, err := NewTransfer(Transfer{TransactionID: "t", PayerAccountID: "a",
			PayeeAccountID: "b", Amount: amount}); !errors.Is(err, money.ErrNegativeAmount) {
			t.Errorf("transfer %d: got %v", amount, err)
		}
		if _, err := NewTopup(Topup{TransactionID: "t", ClearingAccountID: "c",
			WalletAccountID: "w", Amount: amount}); !errors.Is(err, money.ErrNegativeAmount) {
			t.Errorf("topup %d: got %v", amount, err)
		}
		if _, err := NewWithdrawal(Withdrawal{TransactionID: "t", WalletAccountID: "w",
			ClearingAccountID: "c", Amount: amount}); !errors.Is(err, money.ErrNegativeAmount) {
			t.Errorf("withdrawal %d: got %v", amount, err)
		}
		if _, err := NewRefund(Refund{TransactionID: "t", PayableAccountID: "p",
			WalletAccountID: "w", Amount: amount}); !errors.Is(err, money.ErrNegativeAmount) {
			t.Errorf("refund %d: got %v", amount, err)
		}
	}
}

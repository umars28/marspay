package reconcile

import (
	"testing"

	"github.com/umars28/marspay/internal/money"
)

func TestIdenticalSidesReconcileClean(t *testing.T) {
	lines := []Line{
		{Ref: "qr-1", Amount: 3_200_000},
		{Ref: "qr-2", Amount: 4_800_000},
		{Ref: "qr-3", Amount: 9_600_000},
	}

	report := Compare(lines, lines)

	if !report.Clean() {
		t.Fatalf("expected a clean report, got %d discrepancies", len(report.Discrepancies))
	}
	if report.RowsCompared != 3 || report.RowsMatched != 3 {
		t.Errorf("compared %d matched %d, want 3 and 3", report.RowsCompared, report.RowsMatched)
	}
	if report.Delta != 0 {
		t.Errorf("delta = %d, want 0", report.Delta)
	}
}

func TestLostCallbackShowsUpAsProviderOnly(t *testing.T) {
	internal := []Line{{Ref: "qr-1", Amount: 3_200_000}}
	provider := []Line{
		{Ref: "qr-1", Amount: 3_200_000},
		{Ref: "qr-8791-1c04", Amount: 9_100_000},
	}

	report := Compare(internal, provider)

	if len(report.Discrepancies) != 1 {
		t.Fatalf("discrepancies = %d, want 1", len(report.Discrepancies))
	}

	d := report.Discrepancies[0]
	if d.Ref != "qr-8791-1c04" {
		t.Errorf("ref = %q, want qr-8791-1c04", d.Ref)
	}
	if d.Cause != CauseProviderOnly {
		t.Errorf("cause = %q, want %q", d.Cause, CauseProviderOnly)
	}
	if d.Internal != nil {
		t.Error("internal side should be absent")
	}
	if d.Provider == nil || *d.Provider != 9_100_000 {
		t.Errorf("provider amount = %v, want 9100000", d.Provider)
	}
	if d.Delta != -9_100_000 {
		t.Errorf("delta = %d, want -9100000", d.Delta)
	}
	if report.RowsMatched != 1 {
		t.Errorf("matched = %d, want 1", report.RowsMatched)
	}
}

func TestPendingWithdrawalShowsUpAsInternalOnly(t *testing.T) {
	internal := []Line{{Ref: "wd_01J7P8X22K", Amount: 100_000_000}}

	report := Compare(internal, nil)

	if len(report.Discrepancies) != 1 {
		t.Fatalf("discrepancies = %d, want 1", len(report.Discrepancies))
	}
	d := report.Discrepancies[0]
	if d.Cause != CauseInternalOnly {
		t.Errorf("cause = %q, want %q", d.Cause, CauseInternalOnly)
	}
	if d.Delta != 100_000_000 {
		t.Errorf("delta = %d, want 100000000", d.Delta)
	}
	if d.Provider != nil {
		t.Error("provider side should be absent")
	}
}

func TestAmountMismatchIsItsOwnCause(t *testing.T) {
	report := Compare(
		[]Line{{Ref: "qr-1", Amount: 3_200_000}},
		[]Line{{Ref: "qr-1", Amount: 3_100_000}},
	)

	if len(report.Discrepancies) != 1 {
		t.Fatalf("discrepancies = %d, want 1", len(report.Discrepancies))
	}
	d := report.Discrepancies[0]
	if d.Cause != CauseAmountMismatch {
		t.Errorf("cause = %q, want %q", d.Cause, CauseAmountMismatch)
	}
	if d.Delta != 100_000 {
		t.Errorf("delta = %d, want 100000", d.Delta)
	}
	if d.Internal == nil || d.Provider == nil {
		t.Error("both sides should be present on an amount mismatch")
	}
}

func TestDiscrepanciesAreOrderedDeterministically(t *testing.T) {
	internal := []Line{{Ref: "c", Amount: 3}, {Ref: "a", Amount: 1}}
	provider := []Line{{Ref: "b", Amount: 2}}

	first := Compare(internal, provider)
	second := Compare(internal, provider)

	if len(first.Discrepancies) != 3 {
		t.Fatalf("discrepancies = %d, want 3", len(first.Discrepancies))
	}
	for i := range first.Discrepancies {
		if first.Discrepancies[i].Ref != second.Discrepancies[i].Ref {
			t.Fatal("two runs over the same input produced different orderings")
		}
	}
	if first.Discrepancies[0].Ref != "a" {
		t.Errorf("first ref = %q, want a", first.Discrepancies[0].Ref)
	}
}

func TestDuplicateRefsOnOneSideAreSummed(t *testing.T) {
	report := Compare(
		[]Line{{Ref: "qr-1", Amount: 1_000_000}, {Ref: "qr-1", Amount: 1_000_000}},
		[]Line{{Ref: "qr-1", Amount: 2_000_000}},
	)

	if !report.Clean() {
		t.Errorf("two internal rows summing to the provider total should reconcile, got %+v",
			report.Discrepancies)
	}
}

func TestDeltaIsTheSumOfEveryDiscrepancy(t *testing.T) {
	report := Compare(
		[]Line{{Ref: "a", Amount: 100}, {Ref: "b", Amount: 500}},
		[]Line{{Ref: "b", Amount: 400}, {Ref: "c", Amount: 900}},
	)

	var expected money.Minor
	for _, d := range report.Discrepancies {
		expected += d.Delta
	}
	if report.Delta != expected {
		t.Errorf("report delta = %d, sum of discrepancies = %d", report.Delta, expected)
	}
	if report.Delta != 100+100-900 {
		t.Errorf("delta = %d, want %d", report.Delta, 100+100-900)
	}
}

func TestEmptyInputIsCleanNotAnError(t *testing.T) {
	report := Compare(nil, nil)
	if !report.Clean() || report.RowsCompared != 0 {
		t.Errorf("empty comparison should be clean, got %+v", report)
	}
}

func TestEveryCauseExplainsItself(t *testing.T) {
	for _, c := range []Cause{CauseProviderOnly, CauseInternalOnly, CauseAmountMismatch} {
		if c.Explain() == "unknown" || c.Explain() == "" {
			t.Errorf("cause %q has no explanation", c)
		}
	}
}

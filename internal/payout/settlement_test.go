package payout

import (
	"context"
	"testing"
	"time"

	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/rail"
)

var businessDate = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)

func (h *harness) degradeToBatch(t *testing.T, ctx context.Context, count int, gross money.Minor) {
	t.Helper()

	for i := 0; i < count; i++ {
		p := h.settle(t, ctx, gross, riskyMerchant)
		if p.Mode != "batch" {
			t.Fatalf("payout %d was not degraded to batch: %s", i, p.DegradeReason)
		}
		if _, err := h.pool.Exec(ctx,
			`UPDATE payouts SET created_at = $1 WHERE id = $2`,
			businessDate.Add(time.Duration(i)*time.Hour), p.ID); err != nil {
			t.Fatalf("age payout: %v", err)
		}
	}
}

func (h *harness) destinations() map[string]Destination {
	return map[string]Destination{
		h.merchantID: {BankCode: "BCA", AccountNo: "2013", AccountName: "PT Test"},
	}
}

func TestABatchGroupsEveryDegradedPayoutForTheDay(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)
	h.degradeToBatch(t, ctx, 5, 2_000_000)

	result, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}

	if len(result.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(result.Batches))
	}

	batch := result.Batches[0]
	if batch.PayoutCount != 5 {
		t.Errorf("payout_count = %d, want 5", batch.PayoutCount)
	}
	if batch.GrossMinor != 10_000_000 {
		t.Errorf("gross = %d, want 10000000", batch.GrossMinor)
	}
	if batch.NetMinor != batch.GrossMinor-batch.FeeMinor {
		t.Errorf("net %d != gross %d minus holdback %d",
			batch.NetMinor, batch.GrossMinor, batch.FeeMinor)
	}
	if batch.Status != "settled" {
		t.Errorf("status = %q, want settled", batch.Status)
	}
}

func TestSettledPayoutsAreAttachedToTheirBatch(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)
	h.degradeToBatch(t, ctx, 3, 2_000_000)

	result, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}

	var attached, stillOpen int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM payouts WHERE batch_id = $1 AND status = 'settled'`,
		result.Batches[0].ID).Scan(&attached); err != nil {
		t.Fatalf("count attached: %v", err)
	}
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM payouts WHERE status = 'degraded_to_batch'`).Scan(&stillOpen); err != nil {
		t.Fatalf("count open: %v", err)
	}

	if attached != 3 {
		t.Errorf("attached payouts = %d, want 3", attached)
	}
	if stillOpen != 0 {
		t.Errorf("%d payouts are still waiting after a settled batch", stillOpen)
	}
}

func TestRunningTheSameDayTwiceDoesNotCreateASecondBatch(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)
	h.degradeToBatch(t, ctx, 3, 2_000_000)

	first, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if len(second.Batches) != 0 {
		t.Errorf("the second run created %d batches, want 0: everything was already settled",
			len(second.Batches))
	}

	var batches int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM settlement_batches`).Scan(&batches); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if batches != 1 {
		t.Errorf("settlement_batches rows = %d, want 1", batches)
	}
	_ = first
}

func TestAFailedBatchLeavesItsPayoutsWaiting(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000),
		map[rail.Outcome]float64{rail.OutcomeRejected: 1.0})
	h.degradeToBatch(t, ctx, 4, 2_000_000)

	result, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	if result.Batches[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Batches[0].Status)
	}

	var stillOpen int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM payouts WHERE status = 'degraded_to_batch'`).Scan(&stillOpen); err != nil {
		t.Fatalf("count open: %v", err)
	}
	if stillOpen != 4 {
		t.Errorf("waiting payouts = %d, want 4: a failed batch must not consume them", stillOpen)
	}
}

func TestARetriedBatchKeepsTheSameNumber(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000),
		map[rail.Outcome]float64{rail.OutcomeRejected: 1.0})
	h.degradeToBatch(t, ctx, 2, 2_000_000)

	failed, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	working, err := rail.NewSim(rail.Config{Rail: rail.BIFast, Seed: 5})
	if err != nil {
		t.Fatalf("new sim: %v", err)
	}
	h.svc.rails = []rail.Rail{working}

	retried, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if len(retried.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(retried.Batches))
	}
	if retried.Batches[0].ID != failed.Batches[0].ID {
		t.Errorf("the retry used a new batch number %s, want the original %s",
			retried.Batches[0].ID, failed.Batches[0].ID)
	}
	if retried.Batches[0].Status != "settled" {
		t.Errorf("status = %q, want settled", retried.Batches[0].Status)
	}
}

func TestInstantPayoutsNeverEnterABatch(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	for i := 0; i < 3; i++ {
		p := h.settle(t, ctx, 2_000_000, goodMerchant)
		if p.Mode != "instant" {
			t.Fatalf("payout %d was not instant: %s", i, p.DegradeReason)
		}
		if _, err := h.pool.Exec(ctx,
			`UPDATE payouts SET created_at = $1 WHERE id = $2`,
			businessDate, p.ID); err != nil {
			t.Fatalf("age payout: %v", err)
		}
	}

	result, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	if len(result.Batches) != 0 {
		t.Errorf("batches = %d, want 0: instant payouts are already done", len(result.Batches))
	}
}

func TestSettlementHistoryIsNewestFirst(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	for day := 0; day < 3; day++ {
		date := businessDate.AddDate(0, 0, day)
		p := h.settle(t, ctx, 2_000_000, riskyMerchant)
		if _, err := h.pool.Exec(ctx,
			`UPDATE payouts SET created_at = $1 WHERE id = $2`, date, p.ID); err != nil {
			t.Fatalf("age payout: %v", err)
		}
		if _, err := h.svc.RunBatch(ctx, date, h.destinations()); err != nil {
			t.Fatalf("run batch day %d: %v", day, err)
		}
	}

	batches, err := h.svc.Settlements(ctx, h.merchantID)
	if err != nil {
		t.Fatalf("settlements: %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(batches))
	}
	for i := 1; i < len(batches); i++ {
		if batches[i].PeriodStart.After(batches[i-1].PeriodStart) {
			t.Fatalf("batch %d is newer than the one before it", i)
		}
	}
}

func TestADayWithNothingToSettleProducesNoBatch(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	result, err := h.svc.RunBatch(ctx, businessDate, h.destinations())
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	if len(result.Batches) != 0 {
		t.Errorf("batches = %d, want 0", len(result.Batches))
	}
	if result.TotalNet != 0 {
		t.Errorf("total_net = %d, want 0", result.TotalNet)
	}
}

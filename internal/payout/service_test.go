package payout

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/rail"
	"github.com/umars28/marspay/internal/risk"
	"github.com/umars28/marspay/internal/testdb"
)

var (
	goodMerchant = risk.Factors{
		AgeDays: 400, RefundRateBps: 31, DisputeRateBps: 0,
		VolumeStability: 22, VerifiedBusiness: true,
	}
	riskyMerchant = risk.Factors{
		AgeDays: 11, RefundRateBps: 1890, DisputeRateBps: 900, VolumeStability: 2,
	}
	mediumMerchant = risk.Factors{
		AgeDays: 120, RefundRateBps: 400, DisputeRateBps: 40, VolumeStability: 15,
	}
)

type harness struct {
	pool       *pgxpool.Pool
	svc        *Service
	ledger     *ledger.Repo
	float      *Float
	sim        *rail.Sim
	merchantID string
}

func newHarness(t *testing.T, limit money.Minor, outcomes map[rail.Outcome]float64) (*harness, context.Context) {
	t.Helper()

	pool, ctx := testdb.New(t)
	repo := ledger.NewRepo(pool)

	merchantID := id.New("merch")
	seedMerchant(t, ctx, pool, repo, merchantID)

	sim, err := rail.NewSim(rail.Config{
		Rail:     rail.BIFast,
		Latency:  rail.Latency{P50: 3 * time.Second, P99: 11 * time.Second},
		Outcomes: outcomes,
		Seed:     7,
	})
	if err != nil {
		t.Fatalf("new sim: %v", err)
	}

	f := NewFloat(pool, limit)
	return &harness{
		pool:       pool,
		svc:        NewService(pool, repo, f, []rail.Rail{sim}),
		ledger:     repo,
		float:      f,
		sim:        sim,
		merchantID: merchantID,
	}, ctx
}

func seedMerchant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *ledger.Repo, merchantID string) {
	t.Helper()

	_, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
		 VALUES ($1, 'PT Test', 'Test Merchant', 'food', 'active', 70)`, merchantID)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	accounts := [][4]string{
		{ledger.MerchantPayable(merchantID), ledger.OwnerMerchant, merchantID, ledger.TypeMerchantPayable},
		{ledger.MerchantHoldback(merchantID), ledger.OwnerMerchant, merchantID, ledger.TypeMerchantHoldback},
		{ledger.AccountID(ledger.OwnerProvider, string(rail.BIFast), ledger.TypeClearing),
			ledger.OwnerProvider, string(rail.BIFast), ledger.TypeClearing},
	}
	for _, a := range accounts {
		if err := repo.EnsureAccount(ctx, a[0], a[1], a[2], a[3]); err != nil {
			t.Fatalf("seed account %s: %v", a[0], err)
		}
	}
}

func (h *harness) settle(t *testing.T, ctx context.Context, gross money.Minor, factors risk.Factors) *Payout {
	t.Helper()
	p, err := h.svc.Settle(ctx, "", h.merchantID, gross, factors,
		Destination{BankCode: "BCA", AccountNo: "4471", AccountName: "PT Test"})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	return p
}

func TestInstantPayoutSplitsGrossIntoHoldbackAndNet(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	p := h.settle(t, ctx, 3_177_600, goodMerchant)

	if p.Mode != "instant" {
		t.Fatalf("mode = %q (%s), want instant", p.Mode, p.DegradeReason)
	}
	if p.Status != "settled" {
		t.Errorf("status = %q, want settled", p.Status)
	}
	if p.HoldbackBps != risk.MinHoldbackBps {
		t.Errorf("holdback_bps = %d, want %d", p.HoldbackBps, risk.MinHoldbackBps)
	}
	if p.HoldbackMinor+p.NetMinor != p.GrossMinor {
		t.Errorf("holdback %d + net %d != gross %d",
			p.HoldbackMinor, p.NetMinor, p.GrossMinor)
	}
	if p.LatencyMs <= 0 {
		t.Error("latency_ms was not recorded")
	}

	sum, err := h.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}

	holdbackBalance, err := h.ledger.Balance(ctx, ledger.MerchantHoldback(h.merchantID))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if holdbackBalance != p.HoldbackMinor {
		t.Errorf("holdback account = %d, want %d", holdbackBalance, p.HoldbackMinor)
	}
}

func TestHoldbackRowIsScheduledForRelease(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	p := h.settle(t, ctx, 3_177_600, goodMerchant)

	var amount int64
	var releaseAt time.Time
	err := h.pool.QueryRow(ctx,
		`SELECT amount_minor, release_at FROM holdbacks WHERE payout_id = $1`,
		p.ID).Scan(&amount, &releaseAt)
	if err != nil {
		t.Fatalf("load holdback: %v", err)
	}

	if money.Minor(amount) != p.HoldbackMinor {
		t.Errorf("holdback row = %d, want %d", amount, p.HoldbackMinor)
	}
	if !releaseAt.After(time.Now()) {
		t.Error("release_at is not in the future")
	}
}

func TestRiskyMerchantIsForcedToBatchAndNothingIsSent(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	p := h.settle(t, ctx, 3_177_600, riskyMerchant)

	if p.Mode != "batch" {
		t.Fatalf("mode = %q, want batch", p.Mode)
	}
	if p.Status != "degraded_to_batch" {
		t.Errorf("status = %q, want degraded_to_batch", p.Status)
	}
	if p.DegradeReason == "" {
		t.Error("no degrade reason was recorded")
	}
	if len(h.sim.Callbacks()) != 0 {
		t.Error("a batch payout still contacted the rail")
	}
}

func TestFloatAboveEightyFivePercentDegradesEveryone(t *testing.T) {
	h, ctx := newHarness(t, 10_000_000, nil)

	for i := 0; i < 3; i++ {
		if p := h.settle(t, ctx, 3_000_000, goodMerchant); p.Mode != "instant" {
			t.Fatalf("payout %d degraded too early: %s", i, p.DegradeReason)
		}
	}

	position, err := h.float.Position(ctx)
	if err != nil {
		t.Fatalf("position: %v", err)
	}
	if position.UtilisationBps < DegradeAllBps {
		t.Fatalf("utilisation = %d bps, expected to be above %d after three payouts",
			position.UtilisationBps, DegradeAllBps)
	}
	if position.InstantEnabled {
		t.Error("instant is still enabled above the 85 percent threshold")
	}

	p := h.settle(t, ctx, 3_000_000, goodMerchant)
	if p.Mode != "batch" {
		t.Errorf("mode = %q, want batch once float is exhausted", p.Mode)
	}
	if p.DegradeReason != "platform float above 85 percent" {
		t.Errorf("reason = %q, want the float reason", p.DegradeReason)
	}
}

func TestFloatBetweenSeventyAndEightyFiveOnlyDegradesHighHoldbackMerchants(t *testing.T) {
	position := Position{UtilisationBps: 7_500}

	mode, _ := ModeFor(position, risk.MinHoldbackBps)
	if mode != "instant" {
		t.Errorf("low-holdback merchant got %q, want instant", mode)
	}

	mode, reason := ModeFor(position, 1_200)
	if mode != "batch" {
		t.Errorf("high-holdback merchant got %q, want batch", mode)
	}
	if reason == "" {
		t.Error("degrading without a reason")
	}
}

func TestRejectedTransferPostsNothingToTheLedger(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000),
		map[rail.Outcome]float64{rail.OutcomeRejected: 1.0})

	p := h.settle(t, ctx, 3_177_600, goodMerchant)

	if p.Status != "failed" {
		t.Fatalf("status = %q, want failed", p.Status)
	}

	sum, err := h.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}

	payable, err := h.ledger.Balance(ctx, ledger.MerchantPayable(h.merchantID))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if payable != 0 {
		t.Errorf("payable moved to %d on a failed payout, want 0", payable)
	}
}

func TestAmbiguousTimeoutLandsInSendingNotFailed(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000),
		map[rail.Outcome]float64{rail.OutcomeAmbiguous: 1.0})

	p := h.settle(t, ctx, 3_177_600, goodMerchant)

	if p.Status != "sending" {
		t.Fatalf("status = %q, want sending; guessing failed would double-pay on retry", p.Status)
	}

	sum, err := h.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestLostCallbackStillSettlesButLeavesNoNotification(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000),
		map[rail.Outcome]float64{rail.OutcomeCallbackLost: 1.0})

	p := h.settle(t, ctx, 3_177_600, goodMerchant)

	if p.Status != "settled" {
		t.Fatalf("status = %q, want settled", p.Status)
	}
	if len(h.sim.Callbacks()) != 0 {
		t.Error("a lost callback was delivered anyway")
	}
}

func TestMediumRiskMerchantGetsAMiddlingHoldback(t *testing.T) {
	h, ctx := newHarness(t, money.FromRupiah(5_000_000_000), nil)

	p := h.settle(t, ctx, 10_000_000, mediumMerchant)

	if p.HoldbackBps <= risk.MinHoldbackBps {
		t.Errorf("holdback_bps = %d, want above the floor for a medium merchant", p.HoldbackBps)
	}
	if p.HoldbackBps >= risk.MaxHoldbackBps {
		t.Errorf("holdback_bps = %d, want below the ceiling", p.HoldbackBps)
	}
}

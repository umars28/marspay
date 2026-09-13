package risk

import (
	"errors"
	"testing"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/testdb"
)

func seedMerchant(t *testing.T) (*Store, string, func()) {
	t.Helper()

	pool, ctx := testdb.New(t)
	merchantID := id.New("merch")

	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status)
		 VALUES ($1, 'PT Test', 'Test', 'food', 'active')`, merchantID); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	store := NewStore(pool)
	return store, merchantID, func() {}
}

func TestAnUnscoredMerchantSaysSoClearly(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	_, err := store.Latest(ctx, merchantID)
	if !errors.Is(err, ErrNoScore) {
		t.Errorf("got %v, want ErrNoScore", err)
	}
}

func TestTheFirstScoreIsAlwaysRecorded(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	score, err := Evaluate(Factors{AgeDays: 400, RefundRateBps: 31, VolumeStability: 20})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	written, err := store.Record(ctx, merchantID, score)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !written {
		t.Error("the first score was not written")
	}

	latest, err := store.Latest(ctx, merchantID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Score != score.Total || latest.HoldbackBps != score.HoldbackBps {
		t.Errorf("stored %d/%d, want %d/%d",
			latest.Score, latest.HoldbackBps, score.Total, score.HoldbackBps)
	}
	if len(latest.Components) == 0 {
		t.Error("the per-factor breakdown was lost, so a merchant cannot see why")
	}
	if latest.Mode != score.Mode {
		t.Errorf("mode = %q, want %q", latest.Mode, score.Mode)
	}
}

func TestAnUnchangedScoreIsNotWrittenAgain(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	score, err := Evaluate(Factors{AgeDays: 400, RefundRateBps: 31, VolumeStability: 20})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if _, err := store.Record(ctx, merchantID, score); err != nil {
		t.Fatalf("first record: %v", err)
	}

	for i := 0; i < 20; i++ {
		written, err := store.Record(ctx, merchantID, score)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if written {
			t.Fatalf("an unchanged score was written again on attempt %d", i)
		}
	}

	history, err := store.History(ctx, merchantID, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("history rows = %d, want 1: a stable merchant should not fill the table", len(history))
	}
}

func TestAChangedScoreAppendsToTheHistory(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	healthy, err := Evaluate(Factors{AgeDays: 400, RefundRateBps: 31, VolumeStability: 20})
	if err != nil {
		t.Fatalf("evaluate healthy: %v", err)
	}
	troubled, err := Evaluate(Factors{AgeDays: 400, RefundRateBps: 900, DisputeRateBps: 60, VolumeStability: 8})
	if err != nil {
		t.Fatalf("evaluate troubled: %v", err)
	}

	if _, err := store.Record(ctx, merchantID, healthy); err != nil {
		t.Fatalf("record healthy: %v", err)
	}
	written, err := store.Record(ctx, merchantID, troubled)
	if err != nil {
		t.Fatalf("record troubled: %v", err)
	}
	if !written {
		t.Fatal("a changed score was not written")
	}

	history, err := store.History(ctx, merchantID, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history rows = %d, want 2", len(history))
	}

	if history[0].HoldbackBps <= history[1].HoldbackBps {
		t.Errorf("newest holdback %d is not above the older %d",
			history[0].HoldbackBps, history[1].HoldbackBps)
	}
	if !history[0].ComputedAt.After(history[1].ComputedAt) {
		t.Error("history is not newest first")
	}
}

func TestTheStoredBreakdownExplainsTheScore(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	score, err := Evaluate(Factors{AgeDays: 120, RefundRateBps: 400, DisputeRateBps: 40, VolumeStability: 15})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if _, err := store.Record(ctx, merchantID, score); err != nil {
		t.Fatalf("record: %v", err)
	}

	latest, err := store.Latest(ctx, merchantID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}

	for _, factor := range []string{"age", "refund_rate", "dispute_rate", "volume_stability"} {
		if _, ok := latest.Components[factor]; !ok {
			t.Errorf("stored breakdown is missing %q", factor)
		}
	}

	var sum int
	for _, v := range latest.Components {
		sum += v
	}
	if sum > latest.Score+10 || sum < latest.Score-10 {
		t.Errorf("components sum to %d but the score is %d; the breakdown does not explain it",
			sum, latest.Score)
	}
}

func TestMustLatestTurnsAMissingScoreIntoA404(t *testing.T) {
	store, merchantID, _ := seedMerchant(t)
	ctx := t.Context()

	_, err := store.MustLatest(ctx, merchantID)
	if err == nil {
		t.Fatal("a missing score was not reported")
	}
	if errors.Is(err, ErrNoScore) {
		t.Error("MustLatest leaked the raw sentinel instead of an API error")
	}
}

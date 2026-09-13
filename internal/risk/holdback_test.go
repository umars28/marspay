package risk

import "testing"

func TestHoldbackBpsStaysInsideTheClamp(t *testing.T) {
	cases := []struct {
		name         string
		expectedLoss int
		want         int
	}{
		{"zero loss clamps to the floor", 0, MinHoldbackBps},
		{"a tenth of a bp still clamps to the floor", 10, MinHoldbackBps},
		{"one bp of loss covers 200x", 100, 200},
		{"four bps", 400, 800},
		{"twenty bps", 2000, 4000},
		{"huge loss clamps to the ceiling", 90000, MaxHoldbackBps},
		{"negative loss is treated as zero", -5000, MinHoldbackBps},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HoldbackBps(c.expectedLoss); got != c.want {
				t.Errorf("HoldbackBps(%d) = %d, want %d", c.expectedLoss, got, c.want)
			}
		})
	}
}

func TestHoldbackNeverLeavesTheAllowedRange(t *testing.T) {
	for loss := -10_000; loss < 100_000; loss += 7 {
		got := HoldbackBps(loss)
		if got < MinHoldbackBps || got > MaxHoldbackBps {
			t.Fatalf("HoldbackBps(%d) = %d, outside [%d, %d]",
				loss, got, MinHoldbackBps, MaxHoldbackBps)
		}
	}
}

func TestEstablishedMerchantGetsTheLowestRate(t *testing.T) {
	score, err := Evaluate(Factors{
		AgeDays:          1500,
		RefundRateBps:    8,
		DisputeRateBps:   0,
		VolumeStability:  25,
		VerifiedBusiness: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if score.HoldbackBps != MinHoldbackBps {
		t.Errorf("holdback = %d, want %d", score.HoldbackBps, MinHoldbackBps)
	}
	if score.Mode != "instant" {
		t.Errorf("mode = %q, want instant", score.Mode)
	}
	if score.Total < 90 {
		t.Errorf("total = %d, want at least 90", score.Total)
	}
}

func TestFreshMerchantWithDisputesIsForcedToBatch(t *testing.T) {
	score, err := Evaluate(Factors{
		AgeDays:         11,
		RefundRateBps:   1890,
		DisputeRateBps:  900,
		VolumeStability: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if score.Mode != "batch" {
		t.Errorf("mode = %q, want batch", score.Mode)
	}
	if score.HoldbackBps != MaxHoldbackBps {
		t.Errorf("holdback = %d, want the ceiling %d", score.HoldbackBps, MaxHoldbackBps)
	}
	if score.Total >= InstantScoreFloor {
		t.Errorf("total = %d, want below the instant floor %d", score.Total, InstantScoreFloor)
	}
}

func TestScoreImprovesAsRiskFactorsImprove(t *testing.T) {
	worse, err := Evaluate(Factors{AgeDays: 40, RefundRateBps: 600, DisputeRateBps: 120, VolumeStability: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	better, err := Evaluate(Factors{AgeDays: 400, RefundRateBps: 20, DisputeRateBps: 0, VolumeStability: 22})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if better.Total <= worse.Total {
		t.Errorf("better merchant scored %d, worse scored %d", better.Total, worse.Total)
	}
	if better.HoldbackBps >= worse.HoldbackBps {
		t.Errorf("better merchant holdback %d is not below worse %d",
			better.HoldbackBps, worse.HoldbackBps)
	}
}

func TestComponentsAreAlwaysReported(t *testing.T) {
	score, err := Evaluate(Factors{AgeDays: 200, RefundRateBps: 31, VolumeStability: 14})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"age", "refund_rate", "dispute_rate", "volume_stability"} {
		if _, ok := score.Components[key]; !ok {
			t.Errorf("component %q is missing; a merchant must be able to see why", key)
		}
	}
}

func TestEvaluateRejectsImpossibleInput(t *testing.T) {
	if _, err := Evaluate(Factors{RefundRateBps: -1}); err == nil {
		t.Error("negative refund rate was accepted")
	}
	if _, err := Evaluate(Factors{VolumeStability: 26}); err == nil {
		t.Error("volume stability above 25 was accepted")
	}
}

package money

import (
	"errors"
	"testing"
)

func TestFeeHalfUp(t *testing.T) {
	cases := []struct {
		name   string
		amount Minor
		bps    int
		want   Minor
	}{
		{"exact division", FromRupiah(32_000), 70, 22_400},
		{"zero amount", 0, 70, 0},
		{"zero rate", FromRupiah(32_000), 0, 0},
		{"rounds down below half", 123_457, 70, 864},
		{"rounds up at exactly half", 123_500, 70, 865},
		{"rounds up above half", 123_600, 70, 865},
		{"full rate returns amount", FromRupiah(1_000), BasisPoints, FromRupiah(1_000)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FeeHalfUp(c.amount, c.bps)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("FeeHalfUp(%d, %d) = %d, want %d", c.amount, c.bps, got, c.want)
			}
		})
	}
}

func TestFeeHalfUpRejectsBadInput(t *testing.T) {
	if _, err := FeeHalfUp(-1, 70); !errors.Is(err, ErrNegativeAmount) {
		t.Errorf("negative amount: got %v, want ErrNegativeAmount", err)
	}
	if _, err := FeeHalfUp(100, -1); !errors.Is(err, ErrInvalidBps) {
		t.Errorf("negative bps: got %v, want ErrInvalidBps", err)
	}
	if _, err := FeeHalfUp(100, BasisPoints+1); !errors.Is(err, ErrInvalidBps) {
		t.Errorf("bps above 10000: got %v, want ErrInvalidBps", err)
	}
}

func TestFeeNeverExceedsAmount(t *testing.T) {
	for amount := Minor(0); amount < 20_000; amount += 7 {
		for _, bps := range []int{1, 70, 400, 1_500, 4_500, BasisPoints} {
			fee, err := FeeHalfUp(amount, bps)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fee > amount {
				t.Fatalf("fee %d exceeds amount %d at %d bps", fee, amount, bps)
			}
			if fee < 0 {
				t.Fatalf("fee %d is negative at amount %d, %d bps", fee, amount, bps)
			}
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		in   Minor
		want string
	}{
		{0, "Rp 0"},
		{FromRupiah(1), "Rp 1"},
		{FromRupiah(999), "Rp 999"},
		{FromRupiah(1_000), "Rp 1.000"},
		{FromRupiah(2_480_500), "Rp 2.480.500"},
		{-FromRupiah(32_000), "-Rp 32.000"},
		{123_456, "Rp 1.234,56"},
	}

	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Minor(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

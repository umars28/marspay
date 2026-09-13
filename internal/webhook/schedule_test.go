package webhook

import (
	"testing"
	"time"
)

func TestBackoffMatchesThePublishedSchedule(t *testing.T) {
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second, 30 * time.Second,
		2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour,
	}

	for i, expected := range want {
		got, ok := NextRetry(i + 1)
		if !ok {
			t.Fatalf("no retry after attempt %d, want one", i+1)
		}
		if got != expected {
			t.Errorf("after attempt %d the wait is %s, want %s", i+1, got, expected)
		}
	}
}

func TestBackoffIsMonotonic(t *testing.T) {
	for i := 1; i < len(Backoff); i++ {
		if Backoff[i] <= Backoff[i-1] {
			t.Errorf("backoff step %d (%s) is not longer than step %d (%s)",
				i, Backoff[i], i-1, Backoff[i-1])
		}
	}
}

func TestTheLastAttemptHasNoRetryLeft(t *testing.T) {
	if _, ok := NextRetry(MaxAttempts()); ok {
		t.Errorf("attempt %d still offers a retry, so nothing would ever reach the dead letter queue",
			MaxAttempts())
	}
}

func TestAttemptCountsBelowOneAreRejected(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if _, ok := NextRetry(n); ok {
			t.Errorf("NextRetry(%d) offered a retry", n)
		}
	}
}

func TestTotalWindowIsAboutEightHours(t *testing.T) {
	total := TotalWindow()
	if total < 7*time.Hour || total > 9*time.Hour {
		t.Errorf("total retry window is %s, want roughly 8 hours", total)
	}
}

func TestMaxAttemptsIsOneMoreThanTheBackoffSteps(t *testing.T) {
	if MaxAttempts() != len(Backoff)+1 {
		t.Errorf("MaxAttempts = %d, want %d: one first try plus one per backoff step",
			MaxAttempts(), len(Backoff)+1)
	}
}

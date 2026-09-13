package webhook

import "time"

var Backoff = []time.Duration{
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	time.Hour,
	6 * time.Hour,
}

const (
	StatusPending    = "pending"
	StatusRetrying   = "retrying"
	StatusDelivered  = "delivered"
	StatusDeadLetter = "dead_letter"
)

func MaxAttempts() int {
	return len(Backoff) + 1
}

func NextRetry(attemptsMade int) (time.Duration, bool) {
	if attemptsMade < 1 || attemptsMade > len(Backoff) {
		return 0, false
	}
	return Backoff[attemptsMade-1], true
}

func TotalWindow() time.Duration {
	var total time.Duration
	for _, d := range Backoff {
		total += d
	}
	return total
}

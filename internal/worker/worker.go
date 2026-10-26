package worker

import (
	"context"
	"log/slog"
	"time"
)

type Step func(ctx context.Context) (int, error)

type Loop struct {
	Name        string
	Step        Step
	Interval    time.Duration
	MaxBackoff  time.Duration
	StepTimeout time.Duration
	Logger      *slog.Logger
}

func (l Loop) Run(ctx context.Context) {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("worker", l.Name)

	interval := positive(l.Interval, time.Second)
	maxBackoff := positive(l.MaxBackoff, 30*time.Second)
	stepTimeout := positive(l.StepTimeout, 30*time.Second)

	logger.Info("worker started", "interval", interval.String())
	defer logger.Info("worker stopped")

	backoff := interval
	for ctx.Err() == nil {
		n, err := l.runStep(ctx, stepTimeout)

		var wait time.Duration
		switch {
		case err != nil:
			logger.Error("worker step failed", "error", err, "retry_in", backoff.String())
			wait = backoff
			backoff = min(backoff*2, maxBackoff)
		case n > 0:
			backoff = interval
			continue
		default:
			backoff = interval
			wait = interval
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (l Loop) runStep(ctx context.Context, timeout time.Duration) (int, error) {
	stepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return l.Step(stepCtx)
}

func positive(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

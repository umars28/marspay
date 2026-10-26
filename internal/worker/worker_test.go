package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func runAsync(l Loop, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	return done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDrainsWithoutWaitingWhileThereIsWork(t *testing.T) {
	var calls atomic.Int32
	remaining := atomic.Int32{}
	remaining.Store(5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(Loop{
		Name:     "drain",
		Interval: time.Hour,
		Logger:   quiet,
		Step: func(context.Context) (int, error) {
			calls.Add(1)
			if remaining.Load() == 0 {
				return 0, nil
			}
			remaining.Add(-1)
			return 1, nil
		},
	}, ctx)

	waitFor(t, "five batches and one empty sweep", func() bool { return calls.Load() >= 6 })
	if got := calls.Load(); got != 6 {
		t.Fatalf("calls = %d, want 6: drain back-to-back, then wait the hour-long interval", got)
	}
	cancel()
	<-done
}

func TestWaitsTheIntervalWhenIdle(t *testing.T) {
	var mu sync.Mutex
	var stamps []time.Time

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(Loop{
		Name:     "idle",
		Interval: 30 * time.Millisecond,
		Logger:   quiet,
		Step: func(context.Context) (int, error) {
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
			return 0, nil
		},
	}, ctx)

	waitFor(t, "three idle sweeps", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(stamps) >= 3
	})
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < 25*time.Millisecond {
			t.Fatalf("idle sweeps %d and %d were %s apart, want about 30ms", i-1, i, gap)
		}
	}
}

func TestBacksOffOnErrorsAndResetsAfterSuccess(t *testing.T) {
	var mu sync.Mutex
	var stamps []time.Time
	results := []error{errors.New("down"), errors.New("down"), errors.New("down"), nil, errors.New("down")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(Loop{
		Name:       "backoff",
		Interval:   10 * time.Millisecond,
		MaxBackoff: 25 * time.Millisecond,
		Logger:     quiet,
		Step: func(context.Context) (int, error) {
			mu.Lock()
			defer mu.Unlock()
			i := len(stamps)
			stamps = append(stamps, time.Now())
			if i < len(results) {
				return 0, results[i]
			}
			return 0, nil
		},
	}, ctx)

	waitFor(t, "six steps", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(stamps) >= 6
	})
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	gap := func(i int) time.Duration { return stamps[i].Sub(stamps[i-1]) }
	if gap(1) < 8*time.Millisecond || gap(2) < 18*time.Millisecond || gap(3) < 23*time.Millisecond {
		t.Fatalf("gaps %s, %s, %s: want about 10ms, 20ms, then capped at 25ms", gap(1), gap(2), gap(3))
	}
	if gap(3) > 200*time.Millisecond {
		t.Fatalf("third gap %s exceeds the 25ms cap by far", gap(3))
	}
	if gap(5) > gap(3) {
		t.Fatalf("backoff after a success (%s) should restart from the interval, not continue from %s", gap(5), gap(3))
	}
}

func TestStopsPromptlyWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := runAsync(Loop{
		Name:     "stop",
		Interval: time.Hour,
		Logger:   quiet,
		Step: func(context.Context) (int, error) {
			calls.Add(1)
			return 0, nil
		},
	}, ctx)

	waitFor(t, "first sweep", func() bool { return calls.Load() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop while waiting out its interval")
	}
}

func TestShutdownLetsTheInFlightStepFinish(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var sawCancel atomic.Bool
	var finished atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(Loop{
		Name:     "graceful",
		Interval: time.Hour,
		Logger:   quiet,
		Step: func(stepCtx context.Context) (int, error) {
			close(started)
			<-release
			sawCancel.Store(stepCtx.Err() != nil)
			finished.Store(true)
			return 1, nil
		},
	}, ctx)

	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("loop returned before its in-flight step finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done

	if !finished.Load() {
		t.Fatal("in-flight step did not finish")
	}
	if sawCancel.Load() {
		t.Fatal("in-flight step saw a cancelled context; shutdown must not abort a step midway")
	}
}

func TestStepTimeoutBoundsAStuckStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := runAsync(Loop{
		Name:        "stuck",
		Interval:    time.Hour,
		StepTimeout: 20 * time.Millisecond,
		Logger:      quiet,
		Step: func(stepCtx context.Context) (int, error) {
			calls.Add(1)
			<-stepCtx.Done()
			return 0, stepCtx.Err()
		},
	}, ctx)

	waitFor(t, "first step", func() bool { return calls.Load() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a stuck step held shutdown past its timeout")
	}
}

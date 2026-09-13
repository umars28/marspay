package velocity

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

func counters(t *testing.T) map[string]Counter {
	t.Helper()
	out := map[string]Counter{"memory": NewMemoryCounter()}

	addr := os.Getenv("MARSPAY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Log("MARSPAY_TEST_REDIS_ADDR unset, skipping the Redis counter")
		return out
	}

	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	out["redis"] = NewRedisCounter(client, "test:"+id.ULID()+":")
	return out
}

func transfer(user string, newRecipient bool) Subject {
	return Subject{UserID: user, Kind: KindTransfer, Amount: 100_000, NewRecipient: newRecipient}
}

func TestAQuietUserIsAllowed(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())

			d, err := engine.Evaluate(ctx, transfer(id.New("usr"), true))
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if d.Action != ActionAllow || !d.Allowed() {
				t.Errorf("action = %q, want allow", d.Action)
			}
			if len(d.Trips) != 0 {
				t.Errorf("trips = %v, want none", d.Trips)
			}
		})
	}
}

func TestTenTransfersToNewRecipientsFreezeTheAccount(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			var last Decision
			for i := 0; i < 10; i++ {
				d, err := engine.Evaluate(ctx, transfer(user, true))
				if err != nil {
					t.Fatalf("evaluate %d: %v", i, err)
				}
				last = d
				if i < 9 && d.Action != ActionAllow {
					t.Fatalf("transfer %d already tripped: %v", i+1, d.Trips)
				}
			}

			if last.Action != ActionFreeze {
				t.Errorf("action = %q, want freeze on the tenth", last.Action)
			}
			if last.Allowed() {
				t.Error("a frozen decision reported itself as allowed")
			}
		})
	}
}

func TestTransfersToKnownRecipientsNeverTripVR03(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			for i := 0; i < 40; i++ {
				d, err := engine.Evaluate(ctx, transfer(user, false))
				if err != nil {
					t.Fatalf("evaluate %d: %v", i, err)
				}
				for _, trip := range d.Trips {
					if trip.Code == "VR-03" {
						t.Fatalf("VR-03 tripped on a known recipient at %d", i)
					}
				}
			}
		})
	}
}

func TestStructuringJustUnderTheReportingThresholdTrips(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")
			amount := ReportingThreshold * 99 / 100

			var last Decision
			for i := 0; i < 3; i++ {
				d, err := engine.Evaluate(ctx, Subject{
					UserID: user, Kind: KindPayment, Amount: amount,
				})
				if err != nil {
					t.Fatalf("evaluate %d: %v", i, err)
				}
				last = d
			}

			if last.Action != ActionFreeze {
				t.Errorf("action = %q, want freeze after three near-threshold amounts", last.Action)
			}
		})
	}
}

func TestAnAmountOverTheThresholdIsNotStructuring(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			for i := 0; i < 5; i++ {
				d, err := engine.Evaluate(ctx, Subject{
					UserID: user, Kind: KindPayment, Amount: ReportingThreshold + 1,
				})
				if err != nil {
					t.Fatalf("evaluate: %v", err)
				}
				for _, trip := range d.Trips {
					if trip.Code == "VR-05" {
						t.Fatal("VR-05 tripped on an amount above the threshold, which is not structuring")
					}
				}
			}
		})
	}
}

func TestWithdrawingRightAfterATopupIsHeld(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			topup, err := engine.Evaluate(ctx, Subject{UserID: user, Kind: KindTopup, Amount: 1_000_000})
			if err != nil {
				t.Fatalf("topup: %v", err)
			}
			if topup.Action != ActionAllow {
				t.Errorf("the top up itself tripped: %v", topup.Trips)
			}

			withdrawal, err := engine.Evaluate(ctx, Subject{
				UserID: user, Kind: KindWithdrawal, Amount: 1_000_000,
			})
			if err != nil {
				t.Fatalf("withdrawal: %v", err)
			}
			if withdrawal.Action != ActionHoldWithdrawal {
				t.Errorf("action = %q, want hold_withdrawal", withdrawal.Action)
			}
		})
	}
}

func TestAWithdrawalWithoutARecentTopupIsFine(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())

			d, err := engine.Evaluate(ctx, Subject{
				UserID: id.New("usr"), Kind: KindWithdrawal, Amount: 1_000_000,
			})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if d.Action != ActionAllow {
				t.Errorf("action = %q, want allow", d.Action)
			}
		})
	}
}

func TestHighHourlyValueAsksForAnOTP(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			d, err := engine.Evaluate(ctx, Subject{
				UserID: user, Kind: KindPayment, Amount: money.Minor(60_000_000_00),
			})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if d.Action != ActionRequireOTP {
				t.Errorf("action = %q, want require_otp", d.Action)
			}
			if !d.Allowed() {
				t.Error("require_otp should still be allowed after a step-up, not blocked")
			}
		})
	}
}

func TestSumRulesAccumulateAcrossSmallerAmounts(t *testing.T) {
	ctx := context.Background()
	for name, c := range counters(t) {
		t.Run(name, func(t *testing.T) {
			engine := NewEngine(c, DefaultRules())
			user := id.New("usr")

			for i := 0; i < 5; i++ {
				d, err := engine.Evaluate(ctx, Subject{
					UserID: user, Kind: KindPayment, Amount: money.Minor(9_000_000_00),
				})
				if err != nil {
					t.Fatalf("evaluate %d: %v", i, err)
				}
				if i < 5 && d.Action == ActionRequireOTP && i < 4 {
					t.Fatalf("tripped early at %d with total %d", i, (i+1)*9_000_000_00)
				}
			}

			d, err := engine.Evaluate(ctx, Subject{
				UserID: user, Kind: KindPayment, Amount: money.Minor(9_000_000_00),
			})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if d.Action != ActionRequireOTP {
				t.Errorf("action = %q, want require_otp once the hourly sum passed the limit", d.Action)
			}
		})
	}
}

func TestMonitorModeRecordsTheTripButDoesNotAct(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(NewMemoryCounter(), DefaultRules())

	d, err := engine.Evaluate(ctx, Subject{
		UserID: id.New("usr"), Kind: KindTransfer, Amount: 100_000,
		NewDevice: true, NewBank: true,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if d.Action != ActionAllow {
		t.Errorf("action = %q, want allow: VR-02 is in monitor mode", d.Action)
	}
	if len(d.Trips) != 1 || d.Trips[0].Code != "VR-02" {
		t.Fatalf("trips = %v, want one VR-02 trip recorded", d.Trips)
	}
	if d.Trips[0].Mode != ModeMonitor {
		t.Errorf("mode = %q, want monitor", d.Trips[0].Mode)
	}
}

func TestDisabledRulesDoNotEvenCount(t *testing.T) {
	ctx := context.Background()
	rules := DefaultRules()
	for i := range rules {
		rules[i].Mode = ModeDisabled
	}
	engine := NewEngine(NewMemoryCounter(), rules)

	user := id.New("usr")
	for i := 0; i < 50; i++ {
		d, err := engine.Evaluate(ctx, transfer(user, true))
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if len(d.Trips) != 0 {
			t.Fatalf("a disabled rule tripped: %v", d.Trips)
		}
	}
}

func TestTheStrongestActionWins(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(NewMemoryCounter(), DefaultRules())
	user := id.New("usr")

	for i := 0; i < 9; i++ {
		if _, err := engine.Evaluate(ctx, transfer(user, true)); err != nil {
			t.Fatalf("warm up: %v", err)
		}
	}

	d, err := engine.Evaluate(ctx, Subject{
		UserID: user, Kind: KindTransfer, Amount: money.Minor(60_000_000_00),
		NewRecipient: true,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	if len(d.Trips) < 2 {
		t.Fatalf("trips = %v, want at least VR-03 and VR-08", d.Trips)
	}
	if d.Action != ActionFreeze {
		t.Errorf("action = %q, want freeze: it outranks require_otp", d.Action)
	}
}

func TestCountersExpireSoOldBehaviourStopsCounting(t *testing.T) {
	ctx := context.Background()
	counter := NewMemoryCounter()
	engine := NewEngine(counter, DefaultRules())
	user := id.New("usr")

	for i := 0; i < 9; i++ {
		if _, err := engine.Evaluate(ctx, transfer(user, true)); err != nil {
			t.Fatalf("warm up: %v", err)
		}
	}

	counter.now = func() time.Time { return time.Now().Add(6 * time.Minute) }

	d, err := engine.Evaluate(ctx, transfer(user, true))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if d.Action != ActionAllow {
		t.Errorf("action = %q, want allow once the window has passed", d.Action)
	}
}

func TestACounterFailureIsNotSwallowed(t *testing.T) {
	engine := NewEngine(failingCounter{}, DefaultRules())

	_, err := engine.Evaluate(context.Background(), transfer(id.New("usr"), true))
	if err == nil {
		t.Error("a broken counter produced a decision anyway; the hot path must fail closed")
	}
}

type failingCounter struct{}

func (failingCounter) Add(context.Context, string, int64, time.Duration) (int64, error) {
	return 0, errors.New("redis is down")
}

func (failingCounter) Peek(context.Context, string) (int64, error) {
	return 0, errors.New("redis is down")
}

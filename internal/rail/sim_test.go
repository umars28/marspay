package rail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

func newSim(t *testing.T, outcomes map[Outcome]float64) *Sim {
	t.Helper()
	sim, err := NewSim(Config{
		Rail:     BIFast,
		Latency:  Latency{P50: 3 * time.Second, P99: 11 * time.Second},
		Outcomes: outcomes,
		Seed:     42,
	})
	if err != nil {
		t.Fatalf("new sim: %v", err)
	}
	return sim
}

func send(t *testing.T, s *Sim) (Result, error) {
	t.Helper()
	return s.Send(context.Background(), Request{
		PayoutID:    id.New("po"),
		BankCode:    "BCA",
		AccountNo:   "4471",
		AccountName: "PT Tuku",
		Amount:      money.FromRupiah(32_000),
	})
}

func TestSuccessSendsExactlyOneCallback(t *testing.T) {
	sim := newSim(t, nil)

	result, err := send(t, sim)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.CallbackSent {
		t.Error("CallbackSent = false, want true")
	}
	if len(sim.Callbacks()) != 1 {
		t.Errorf("callbacks = %d, want 1", len(sim.Callbacks()))
	}
}

func TestLostCallbackReturnsSuccessButNeverNotifies(t *testing.T) {
	sim := newSim(t, map[Outcome]float64{OutcomeCallbackLost: 1.0})

	result, err := send(t, sim)
	if err != nil {
		t.Fatalf("send returned an error, want success with no callback: %v", err)
	}
	if result.CallbackSent {
		t.Error("CallbackSent = true, want false")
	}
	if got := len(sim.Callbacks()); got != 0 {
		t.Errorf("callbacks = %d, want 0; this is the case reconciliation exists for", got)
	}
}

func TestDuplicateCallbackDeliversTheSameEventTwice(t *testing.T) {
	sim := newSim(t, map[Outcome]float64{OutcomeCallbackDuplicated: 1.0})

	if _, err := send(t, sim); err != nil {
		t.Fatalf("send: %v", err)
	}

	callbacks := sim.Callbacks()
	if len(callbacks) != 2 {
		t.Fatalf("callbacks = %d, want 2", len(callbacks))
	}
	if callbacks[0].BankReference != callbacks[1].BankReference {
		t.Error("duplicate callbacks carry different references; a receiver could not dedupe them")
	}
}

func TestAmbiguousTimeoutStillMovedTheMoney(t *testing.T) {
	sim := newSim(t, map[Outcome]float64{OutcomeAmbiguous: 1.0})

	if _, err := send(t, sim); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("got %v, want ErrAmbiguous", err)
	}

	all := sim.Callbacks()
	if len(all) != 1 {
		t.Fatalf("callbacks = %d, want 1", len(all))
	}
	if all[0].Outcome != OutcomeSuccess {
		t.Errorf("callback outcome = %q, want success", all[0].Outcome)
	}
}

func TestRejectionSendsNoCallback(t *testing.T) {
	sim := newSim(t, map[Outcome]float64{OutcomeRejected: 1.0})

	if _, err := send(t, sim); !errors.Is(err, ErrRejected) {
		t.Fatalf("got %v, want ErrRejected", err)
	}
	if len(sim.Callbacks()) != 0 {
		t.Error("a rejected transfer sent a callback")
	}
}

func TestSameSeedProducesTheSameSequence(t *testing.T) {
	outcomes := map[Outcome]float64{
		OutcomeRejected:     0.2,
		OutcomeCallbackLost: 0.2,
		OutcomeAmbiguous:    0.2,
	}

	collect := func() []string {
		sim := newSim(t, outcomes)
		var seq []string
		for i := 0; i < 50; i++ {
			_, err := send(t, sim)
			switch {
			case err == nil:
				seq = append(seq, "ok")
			default:
				seq = append(seq, err.Error())
			}
		}
		return seq
	}

	a, b := collect(), collect()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("run %d differs: %q vs %q; the simulator is not reproducible", i, a[i], b[i])
		}
	}
}

func TestDistributionRoughlyMatchesConfiguration(t *testing.T) {
	sim := newSim(t, map[Outcome]float64{OutcomeRejected: 0.10})

	const n = 20_000
	rejected := 0
	for i := 0; i < n; i++ {
		if _, err := send(t, sim); errors.Is(err, ErrRejected) {
			rejected++
		}
	}

	rate := float64(rejected) / n
	if rate < 0.08 || rate > 0.12 {
		t.Errorf("rejection rate = %.4f, want roughly 0.10", rate)
	}
}

func TestConfigRejectsProbabilitiesAboveOne(t *testing.T) {
	_, err := NewSim(Config{
		Rail: BIFast,
		Outcomes: map[Outcome]float64{
			OutcomeRejected:     0.7,
			OutcomeCallbackLost: 0.5,
		},
	})
	if err == nil {
		t.Error("probabilities summing above 1 were accepted")
	}
}

func TestSelectPrefersInternalRailForTheSameBank(t *testing.T) {
	internal, _ := NewSim(Config{Rail: BankInternal, Seed: 1})
	bifast, _ := NewSim(Config{Rail: BIFast, Seed: 2})
	rails := []Rail{internal, bifast}

	if got := Select(rails, "BCA", nil); got.Name() != BankInternal {
		t.Errorf("selected %q for BCA, want bank_internal", got.Name())
	}
	if got := Select(rails, "MANDIRI", nil); got.Name() != BIFast {
		t.Errorf("selected %q for MANDIRI, want bifast", got.Name())
	}
}

func TestSelectAvoidsADegradedRail(t *testing.T) {
	internal, _ := NewSim(Config{Rail: BankInternal, Seed: 1})
	bifast, _ := NewSim(Config{Rail: BIFast, Seed: 2})
	rails := []Rail{internal, bifast}

	health := map[Name]Health{BankInternal: {Rail: BankInternal, Degraded: true}}
	if got := Select(rails, "BCA", health); got.Name() == BankInternal {
		t.Error("selected a degraded rail")
	}
}

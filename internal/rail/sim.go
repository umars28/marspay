package rail

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

type Outcome string

const (
	OutcomeSuccess            Outcome = "success"
	OutcomeRejected           Outcome = "rejected_inactive_account"
	OutcomeTimeout            Outcome = "timeout"
	OutcomeAmbiguous          Outcome = "timeout_but_succeeded"
	OutcomeCallbackLost       Outcome = "callback_lost"
	OutcomeCallbackDuplicated Outcome = "callback_duplicate"
	OutcomeRailDown           Outcome = "rail_down"
)

type Latency struct {
	P50 time.Duration
	P99 time.Duration
}

type Config struct {
	Rail     Name
	Latency  Latency
	Outcomes map[Outcome]float64
	Seed     int64
	Sleep    bool
}

type Callback struct {
	PayoutID      string
	BankReference string
	Outcome       Outcome
	At            time.Time
}

type Settlement struct {
	PayoutID      string
	BankReference string
	Amount        money.Minor
	At            time.Time
}

type Sim struct {
	cfg         Config
	mu          sync.Mutex
	rng         *rand.Rand
	callbacks   []Callback
	settlements []Settlement
	now         func() time.Time
}

func NewSim(cfg Config) (*Sim, error) {
	if cfg.Rail == "" {
		return nil, fmt.Errorf("rail: sim needs a rail name")
	}

	total := 0.0
	for _, p := range cfg.Outcomes {
		if p < 0 {
			return nil, fmt.Errorf("rail: outcome probability must not be negative")
		}
		total += p
	}
	if total > 1.0000001 {
		return nil, fmt.Errorf("rail: outcome probabilities sum to %.4f, must not exceed 1", total)
	}

	return &Sim{
		cfg: cfg,
		rng: rand.New(rand.NewSource(cfg.Seed)),
		now: time.Now,
	}, nil
}

func (s *Sim) Name() Name {
	return s.cfg.Rail
}

func (s *Sim) Send(ctx context.Context, req Request) (Result, error) {
	outcome, latency := s.draw()

	if s.cfg.Sleep {
		select {
		case <-ctx.Done():
			return Result{Rail: s.cfg.Rail}, ctx.Err()
		case <-time.After(latency):
		}
	}

	ref := id.New("bank")
	result := Result{Rail: s.cfg.Rail, BankReference: ref, Latency: latency}

	if moved(outcome) {
		s.settle(Settlement{PayoutID: req.PayoutID, BankReference: ref,
			Amount: req.Amount, At: s.now()})
	}

	switch outcome {
	case OutcomeRejected:
		return result, ErrRejected
	case OutcomeRailDown:
		return result, ErrRailDown
	case OutcomeTimeout:
		return result, ErrTimeout
	case OutcomeAmbiguous:
		s.record(Callback{PayoutID: req.PayoutID, BankReference: ref,
			Outcome: OutcomeSuccess, At: s.now()})
		return result, ErrAmbiguous
	case OutcomeCallbackLost:
		result.CallbackSent = false
		return result, nil
	case OutcomeCallbackDuplicated:
		cb := Callback{PayoutID: req.PayoutID, BankReference: ref,
			Outcome: OutcomeSuccess, At: s.now()}
		s.record(cb)
		s.record(cb)
		result.CallbackSent = true
		return result, nil
	default:
		s.record(Callback{PayoutID: req.PayoutID, BankReference: ref,
			Outcome: OutcomeSuccess, At: s.now()})
		result.CallbackSent = true
		return result, nil
	}
}

func (s *Sim) draw() (Outcome, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	roll := s.rng.Float64()
	cumulative := 0.0

	for _, outcome := range outcomeOrder {
		p, ok := s.cfg.Outcomes[outcome]
		if !ok {
			continue
		}
		cumulative += p
		if roll < cumulative {
			return outcome, s.latency()
		}
	}
	return OutcomeSuccess, s.latency()
}

func (s *Sim) latency() time.Duration {
	if s.cfg.Latency.P50 <= 0 {
		return 0
	}
	if s.rng.Float64() < 0.99 {
		return s.cfg.Latency.P50
	}
	if s.cfg.Latency.P99 > 0 {
		return s.cfg.Latency.P99
	}
	return s.cfg.Latency.P50
}

func moved(o Outcome) bool {
	switch o {
	case OutcomeRejected, OutcomeRailDown, OutcomeTimeout:
		return false
	default:
		return true
	}
}

func (s *Sim) record(cb Callback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callbacks = append(s.callbacks, cb)
}

func (s *Sim) settle(st Settlement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlements = append(s.settlements, st)
}

func (s *Sim) Statement() []Settlement {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Settlement, len(s.settlements))
	copy(out, s.settlements)
	return out
}

func (s *Sim) Callbacks() []Callback {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Callback, len(s.callbacks))
	copy(out, s.callbacks)
	return out
}

func (s *Sim) CallbacksFor(payoutID string) []Callback {
	var out []Callback
	for _, cb := range s.Callbacks() {
		if cb.PayoutID == payoutID {
			out = append(out, cb)
		}
	}
	return out
}

var outcomeOrder = []Outcome{
	OutcomeRejected,
	OutcomeRailDown,
	OutcomeTimeout,
	OutcomeAmbiguous,
	OutcomeCallbackLost,
	OutcomeCallbackDuplicated,
	OutcomeSuccess,
}

func BIFastDefaults(seed int64) Config {
	return Config{
		Rail:    BIFast,
		Latency: Latency{P50: 3 * time.Second, P99: 11 * time.Second},
		Outcomes: map[Outcome]float64{
			OutcomeRejected:  0.02,
			OutcomeRailDown:  0.01,
			OutcomeAmbiguous: 0.005,
		},
		Seed: seed,
	}
}

func BillerDefaults(seed int64) Config {
	return Config{
		Rail:    Name("pln"),
		Latency: Latency{P50: 180 * time.Millisecond, P99: 8 * time.Second},
		Outcomes: map[Outcome]float64{
			OutcomeAmbiguous:          0.03,
			OutcomeCallbackLost:       0.02,
			OutcomeCallbackDuplicated: 0.03,
		},
		Seed: seed,
	}
}

package velocity

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Counter interface {
	Add(ctx context.Context, key string, delta int64, window time.Duration) (int64, error)
	Peek(ctx context.Context, key string) (int64, error)
}

type Trip struct {
	Code      string `json:"code"`
	Observed  int64  `json:"observed"`
	Threshold int64  `json:"threshold"`
	Action    Action `json:"action"`
	Mode      Mode   `json:"mode"`
	Detail    string `json:"detail"`
}

type Decision struct {
	Action Action `json:"action"`
	Trips  []Trip `json:"trips"`
}

func (d Decision) Allowed() bool {
	return d.Action != ActionFreeze && d.Action != ActionHoldWithdrawal
}

type Engine struct {
	counter Counter
	rules   []Rule
}

func NewEngine(counter Counter, rules []Rule) *Engine {
	if rules == nil {
		rules = DefaultRules()
	}
	return &Engine{counter: counter, rules: rules}
}

func (e *Engine) Rules() []Rule {
	return e.rules
}

func (e *Engine) Evaluate(ctx context.Context, s Subject) (Decision, error) {
	decision := Decision{Action: ActionAllow}

	for _, rule := range e.rules {
		if rule.Mode == ModeDisabled {
			continue
		}

		observed, counted, err := e.observe(ctx, rule, s)
		if err != nil {
			return Decision{}, err
		}
		if !counted || observed < rule.Threshold {
			continue
		}

		trip := Trip{
			Code:      rule.Code,
			Observed:  observed,
			Threshold: rule.Threshold,
			Action:    rule.Action,
			Mode:      rule.Mode,
			Detail:    rule.Description,
		}
		decision.Trips = append(decision.Trips, trip)

		if rule.Mode == ModeActive && rule.Action.strongerThan(decision.Action) {
			decision.Action = rule.Action
		}
	}

	return decision, nil
}

func (e *Engine) observe(ctx context.Context, rule Rule, s Subject) (int64, bool, error) {
	key := rule.Key(s.UserID)

	if rule.Triggers(s) {
		observed, err := e.counter.Add(ctx, key, rule.delta(s), rule.Window)
		if err != nil {
			return 0, false, err
		}
		if rule.Checks(s) {
			return observed, true, nil
		}
		return 0, false, nil
	}

	if !rule.Checks(s) {
		return 0, false, nil
	}

	observed, err := e.counter.Peek(ctx, key)
	if err != nil {
		return 0, false, err
	}
	return observed, true, nil
}

type MemoryCounter struct {
	mu     sync.Mutex
	values map[string]int64
	expiry map[string]time.Time
	now    func() time.Time
}

func NewMemoryCounter() *MemoryCounter {
	return &MemoryCounter{
		values: map[string]int64{},
		expiry: map[string]time.Time{},
		now:    time.Now,
	}
}

func (c *MemoryCounter) Add(_ context.Context, key string, delta int64, window time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweep(key)
	c.values[key] += delta
	c.expiry[key] = c.now().Add(window)
	return c.values[key], nil
}

func (c *MemoryCounter) Peek(_ context.Context, key string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweep(key)
	return c.values[key], nil
}

func (c *MemoryCounter) Expire(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	delete(c.expiry, key)
}

func (c *MemoryCounter) sweep(key string) {
	if at, ok := c.expiry[key]; ok && c.now().After(at) {
		delete(c.values, key)
		delete(c.expiry, key)
	}
}

var addScript = redis.NewScript(`
local value = redis.call('INCRBY', KEYS[1], ARGV[1])
if redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return value
`)

type RedisCounter struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisCounter(client redis.UniversalClient, prefix string) *RedisCounter {
	return &RedisCounter{client: client, prefix: prefix}
}

func (c *RedisCounter) Add(ctx context.Context, key string, delta int64, window time.Duration) (int64, error) {
	v, err := addScript.Run(ctx, c.client, []string{c.prefix + key},
		delta, window.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("velocity: add %s: %w", key, err)
	}
	return v, nil
}

func (c *RedisCounter) Peek(ctx context.Context, key string) (int64, error) {
	v, err := c.client.Get(ctx, c.prefix+key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("velocity: peek %s: %w", key, err)
	}
	return v, nil
}

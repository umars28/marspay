package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/money"
)

const (
	resultNotCached    = -2
	resultInsufficient = -1
)

var reserveScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false then
  return -2
end
local balance = tonumber(current)
local amount = tonumber(ARGV[1])
if balance < amount then
  return -1
end
return redis.call('DECRBY', KEYS[1], amount)
`)

var releaseScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current == false then
  return -2
end
return redis.call('INCRBY', KEYS[1], tonumber(ARGV[1]))
`)

type Redis struct {
	client redis.UniversalClient
	prefix string
	ttl    time.Duration
}

func NewRedis(client redis.UniversalClient, prefix string, ttl time.Duration) *Redis {
	return &Redis{client: client, prefix: prefix, ttl: ttl}
}

func (r *Redis) key(accountID string) string {
	return r.prefix + "balance:" + accountID
}

func (r *Redis) Warm(ctx context.Context, accountID string, balance money.Minor) error {
	if err := r.client.Set(ctx, r.key(accountID), int64(balance), r.ttl).Err(); err != nil {
		return fmt.Errorf("wallet: warm %s: %w", accountID, err)
	}
	return nil
}

func (r *Redis) Available(ctx context.Context, accountID string) (money.Minor, error) {
	v, err := r.client.Get(ctx, r.key(accountID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, ErrAccountNotCached
	}
	if err != nil {
		return 0, fmt.Errorf("wallet: available %s: %w", accountID, err)
	}
	return money.Minor(v), nil
}

func (r *Redis) Reserve(ctx context.Context, accountID string, amount money.Minor) (money.Minor, error) {
	if amount <= 0 {
		return 0, money.ErrNegativeAmount
	}
	return r.run(ctx, reserveScript, accountID, amount)
}

func (r *Redis) Release(ctx context.Context, accountID string, amount money.Minor) (money.Minor, error) {
	if amount <= 0 {
		return 0, money.ErrNegativeAmount
	}
	return r.run(ctx, releaseScript, accountID, amount)
}

func (r *Redis) run(ctx context.Context, script *redis.Script, accountID string, amount money.Minor) (money.Minor, error) {
	res, err := script.Run(ctx, r.client, []string{r.key(accountID)}, int64(amount)).Int64()
	if err != nil {
		return 0, fmt.Errorf("wallet: script on %s: %w", accountID, err)
	}

	switch res {
	case resultNotCached:
		return 0, ErrAccountNotCached
	case resultInsufficient:
		return 0, ErrInsufficientFunds
	default:
		return money.Minor(res), nil
	}
}

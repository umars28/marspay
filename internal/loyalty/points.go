package loyalty

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

const (
	PointsExpireAfter = 180 * 24 * time.Hour
	MinRedeemToCash   = 10_000
)

var ErrQuotaExhausted = errors.New("loyalty: this offer has run out")

type Quota interface {
	Take(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Remaining(ctx context.Context, key string) (int64, error)
	Seed(ctx context.Context, key string, total int64, ttl time.Duration) error
}

type Balance struct {
	Points       int64      `json:"points"`
	ExpiringSoon int64      `json:"expiring_soon"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Rate         string     `json:"rate"`
}

type Entry struct {
	ID        string     `json:"id"`
	Amount    int64      `json:"amount"`
	Kind      string     `json:"kind"`
	Source    string     `json:"source,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type Service struct {
	pool  *pgxpool.Pool
	quota Quota
	now   func() time.Time
}

func NewService(pool *pgxpool.Pool, quota Quota) *Service {
	return &Service{pool: pool, quota: quota, now: time.Now}
}

func (s *Service) account(ctx context.Context, userID string) (string, error) {
	var accountID string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO point_accounts (id, user_id) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
		 RETURNING id`,
		id.New("pts"), userID).Scan(&accountID)
	if err != nil {
		return "", fmt.Errorf("loyalty: point account: %w", err)
	}
	return accountID, nil
}

func (s *Service) Balance(ctx context.Context, userID string) (*Balance, error) {
	accountID, err := s.account(ctx, userID)
	if err != nil {
		return nil, err
	}

	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM point_entries WHERE account_id = $1`,
		accountID).Scan(&total); err != nil {
		return nil, fmt.Errorf("loyalty: point balance: %w", err)
	}

	var soon int64
	var expiresAt *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0), MIN(expires_at) FROM point_entries
		 WHERE account_id = $1 AND kind = 'earn' AND expires_at IS NOT NULL
		   AND expires_at < now() + interval '30 days'`,
		accountID).Scan(&soon, &expiresAt)
	if err != nil {
		return nil, fmt.Errorf("loyalty: expiring points: %w", err)
	}

	return &Balance{
		Points:       total,
		ExpiringSoon: soon,
		ExpiresAt:    expiresAt,
		Rate:         "1 point = Rp 1",
	}, nil
}

func (s *Service) History(ctx context.Context, userID string, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	accountID, err := s.account(ctx, userID)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, amount, kind, COALESCE(source_id, ''), expires_at, created_at
		 FROM point_entries WHERE account_id = $1
		 ORDER BY created_at DESC, id DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("loyalty: point history: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Amount, &e.Kind, &e.Source,
			&e.ExpiresAt, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("loyalty: scan entry: %w", err)
		}
		e.CreatedAt = e.CreatedAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) award(ctx context.Context, tx pgx.Tx, accountID string, points int64, sourceType, sourceID string) error {
	expiresAt := s.now().Add(PointsExpireAfter)
	_, err := tx.Exec(ctx,
		`INSERT INTO point_entries (id, account_id, amount, kind, source_type, source_id, expires_at)
		 VALUES ($1, $2, $3, 'earn', $4, $5, $6)`,
		id.New("pe"), accountID, points, sourceType, sourceID, expiresAt)
	if err != nil {
		return fmt.Errorf("loyalty: award points: %w", err)
	}
	return nil
}

type RedeemRequest struct {
	Kind   string `json:"kind"`
	Points int64  `json:"points"`
}

type Redemption struct {
	Points     int64     `json:"points"`
	Kind       string    `json:"kind"`
	ValueMinor int64     `json:"value,omitempty"`
	Remaining  int64     `json:"remaining_points"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Service) Redeem(ctx context.Context, userID string, req RedeemRequest) (*Redemption, error) {
	if req.Points <= 0 {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field points must be positive.")
	}
	if req.Kind != "cash" && req.Kind != "voucher" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field kind must be cash or voucher.")
	}
	if req.Kind == "cash" && req.Points < MinRedeemToCash {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Redeeming to balance needs at least %d points.", MinRedeemToCash)
	}

	balance, err := s.Balance(ctx, userID)
	if err != nil {
		return nil, err
	}
	if balance.Points < req.Points {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInsufficient,
			"You do not have enough points.").
			WithDetails(map[string]any{
				"available": balance.Points,
				"required":  req.Points,
			})
	}

	accountID, err := s.account(ctx, userID)
	if err != nil {
		return nil, err
	}

	result := &Redemption{
		Points:     req.Points,
		Kind:       req.Kind,
		ValueMinor: req.Points * money.MinorPerRupiah,
		Remaining:  balance.Points - req.Points,
	}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO point_entries (id, account_id, amount, kind, source_type)
		 VALUES ($1, $2, $3, 'redeem', $4)
		 RETURNING created_at`,
		id.New("pe"), accountID, -req.Points, req.Kind).Scan(&result.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("loyalty: redeem: %w", err)
	}
	result.CreatedAt = result.CreatedAt.UTC()
	return result, nil
}

func (s *Service) ExpireStale(ctx context.Context) (int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT account_id, SUM(amount) FROM point_entries
		 WHERE kind = 'earn' AND expires_at IS NOT NULL AND expires_at < now()
		 GROUP BY account_id`)
	if err != nil {
		return 0, fmt.Errorf("loyalty: find stale points: %w", err)
	}

	type stale struct {
		accountID string
		amount    int64
	}
	var pending []stale
	for rows.Next() {
		var v stale
		if err := rows.Scan(&v.accountID, &v.amount); err != nil {
			rows.Close()
			return 0, fmt.Errorf("loyalty: scan stale: %w", err)
		}
		pending = append(pending, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var expired int64
	for _, v := range pending {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO point_entries (id, account_id, amount, kind, source_type)
			 VALUES ($1, $2, $3, 'expire', 'expiry_worker')`,
			id.New("pe"), v.accountID, -v.amount); err != nil {
			return expired, fmt.Errorf("loyalty: expire points: %w", err)
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE point_entries SET expires_at = NULL
			 WHERE account_id = $1 AND kind = 'earn' AND expires_at < now()`,
			v.accountID); err != nil {
			return expired, fmt.Errorf("loyalty: clear expiry: %w", err)
		}
		expired += v.amount
	}
	return expired, nil
}

type MemoryQuota struct {
	mu     sync.Mutex
	values map[string]int64
}

func NewMemoryQuota() *MemoryQuota {
	return &MemoryQuota{values: map[string]int64{}}
}

func (q *MemoryQuota) Seed(_ context.Context, key string, total int64, _ time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.values[key] = total
	return nil
}

func (q *MemoryQuota) Take(_ context.Context, key string, _ time.Duration) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	left, ok := q.values[key]
	if !ok {
		return true, nil
	}
	if left <= 0 {
		return false, nil
	}
	q.values[key] = left - 1
	return true, nil
}

func (q *MemoryQuota) Remaining(_ context.Context, key string) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.values[key], nil
}

var takeScript = redis.NewScript(`
local left = redis.call('GET', KEYS[1])
if left == false then
  return 1
end
if tonumber(left) <= 0 then
  return 0
end
redis.call('DECR', KEYS[1])
return 1
`)

type RedisQuota struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisQuota(client redis.UniversalClient, prefix string) *RedisQuota {
	return &RedisQuota{client: client, prefix: prefix}
}

func (q *RedisQuota) Seed(ctx context.Context, key string, total int64, ttl time.Duration) error {
	if err := q.client.Set(ctx, q.prefix+key, total, ttl).Err(); err != nil {
		return fmt.Errorf("loyalty: seed quota %s: %w", key, err)
	}
	return nil
}

func (q *RedisQuota) Take(ctx context.Context, key string, _ time.Duration) (bool, error) {
	ok, err := takeScript.Run(ctx, q.client, []string{q.prefix + key}).Int64()
	if err != nil {
		return false, fmt.Errorf("loyalty: take quota %s: %w", key, err)
	}
	return ok == 1, nil
}

func (q *RedisQuota) Remaining(ctx context.Context, key string) (int64, error) {
	v, err := q.client.Get(ctx, q.prefix+key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("loyalty: quota remaining %s: %w", key, err)
	}
	return v, nil
}

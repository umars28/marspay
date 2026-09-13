package compliance

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
)

const (
	SubjectUser     = "user"
	SubjectMerchant = "merchant"

	TypeAccountBlocked = "account_blocked"
	flagTTL            = 12 * time.Hour
	notBlocked         = "-"
)

type Flags interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, key string) error
}

type Block struct {
	ID           string     `json:"id"`
	SubjectType  string     `json:"subject_type"`
	SubjectID    string     `json:"subject_id"`
	Reason       string     `json:"reason"`
	BlockedBy    string     `json:"blocked_by"`
	RuleCode     string     `json:"rule_code,omitempty"`
	HeldMinor    int64      `json:"balance_held"`
	AppealStatus string     `json:"appeal_status,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LiftedAt     *time.Time `json:"lifted_at,omitempty"`
}

type Blocks struct {
	pool  *pgxpool.Pool
	flags Flags
	audit *Audit
}

func NewBlocks(pool *pgxpool.Pool, flags Flags, audit *Audit) *Blocks {
	return &Blocks{pool: pool, flags: flags, audit: audit}
}

func flagKey(subjectType, subjectID string) string {
	return "block:" + subjectType + ":" + subjectID
}

func (b *Blocks) IsBlocked(ctx context.Context, subjectType, subjectID string) (bool, string, error) {
	key := flagKey(subjectType, subjectID)

	if b.flags != nil {
		value, found, err := b.flags.Get(ctx, key)
		if err != nil {
			return false, "", err
		}
		if found {
			return value != notBlocked, value, nil
		}
	}

	var reason string
	err := b.pool.QueryRow(ctx,
		`SELECT reason FROM account_blocks
		 WHERE subject_type = $1 AND subject_id = $2 AND lifted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`,
		subjectType, subjectID).Scan(&reason)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if b.flags != nil {
			_ = b.flags.Set(ctx, key, notBlocked, flagTTL)
		}
		return false, "", nil
	case err != nil:
		return false, "", fmt.Errorf("compliance: read block: %w", err)
	}

	if b.flags != nil {
		_ = b.flags.Set(ctx, key, reason, flagTTL)
	}
	return true, reason, nil
}

func (b *Blocks) Assert(ctx context.Context, subjectType, subjectID string) error {
	blocked, reason, err := b.IsBlocked(ctx, subjectType, subjectID)
	if err != nil {
		return fmt.Errorf("compliance: block check failed, refusing to proceed blind: %w", err)
	}
	if !blocked {
		return nil
	}
	return httpx.Errorf(http.StatusForbidden, TypeAccountBlocked,
		"This account is blocked and cannot move money.").
		WithDetails(map[string]any{"reason": reason})
}

type BlockRequest struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Reason      string `json:"reason"`
	RuleCode    string `json:"rule_code,omitempty"`
}

func (b *Blocks) Block(ctx context.Context, req BlockRequest, actor string) (*Block, error) {
	if actor == "" {
		return nil, ErrNoActor
	}
	if req.Reason == "" {
		return nil, ErrNoReason
	}
	if req.SubjectType == "" || req.SubjectID == "" {
		return nil, ErrNoObject
	}

	held, err := b.heldBalance(ctx, req.SubjectType, req.SubjectID)
	if err != nil {
		return nil, err
	}

	block := &Block{
		ID:           id.New("blk"),
		SubjectType:  req.SubjectType,
		SubjectID:    req.SubjectID,
		Reason:       req.Reason,
		BlockedBy:    actor,
		RuleCode:     req.RuleCode,
		HeldMinor:    held,
		AppealStatus: "none",
	}

	err = b.pool.QueryRow(ctx,
		`INSERT INTO account_blocks
		   (id, subject_type, subject_id, reason, blocked_by, rule_code, held_minor, appeal_status)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, 'none')
		 RETURNING created_at`,
		block.ID, req.SubjectType, req.SubjectID, req.Reason, actor,
		req.RuleCode, held).Scan(&block.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("compliance: insert block: %w", err)
	}
	block.CreatedAt = block.CreatedAt.UTC()

	if req.SubjectType == SubjectUser {
		if _, err := b.pool.Exec(ctx,
			`UPDATE users SET status = 'blocked', updated_at = now() WHERE id = $1`,
			req.SubjectID); err != nil {
			return nil, fmt.Errorf("compliance: mark user blocked: %w", err)
		}
	}

	if b.flags != nil {
		if err := b.flags.Set(ctx, flagKey(req.SubjectType, req.SubjectID), req.Reason, flagTTL); err != nil {
			return nil, err
		}
	}

	if b.audit != nil {
		if err := b.audit.Record(ctx, Entry{
			Actor: actor, Action: "block_account",
			ObjectType: req.SubjectType, ObjectID: req.SubjectID,
			After:  map[string]any{"status": "blocked", "rule": req.RuleCode},
			Reason: req.Reason,
		}); err != nil {
			return nil, err
		}
	}

	return block, nil
}

func (b *Blocks) Unblock(ctx context.Context, subjectType, subjectID, actor, reason string) error {
	if actor == "" {
		return ErrNoActor
	}
	if reason == "" {
		return ErrNoReason
	}

	tag, err := b.pool.Exec(ctx,
		`UPDATE account_blocks SET lifted_by = $1, lifted_at = now()
		 WHERE subject_type = $2 AND subject_id = $3 AND lifted_at IS NULL`,
		actor, subjectType, subjectID)
	if err != nil {
		return fmt.Errorf("compliance: lift block: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"%s %s is not blocked.", subjectType, subjectID)
	}

	if subjectType == SubjectUser {
		if _, err := b.pool.Exec(ctx,
			`UPDATE users SET status = 'active', updated_at = now() WHERE id = $1`,
			subjectID); err != nil {
			return fmt.Errorf("compliance: reactivate user: %w", err)
		}
	}

	if b.flags != nil {
		if err := b.flags.Del(ctx, flagKey(subjectType, subjectID)); err != nil {
			return err
		}
	}

	if b.audit != nil {
		return b.audit.Record(ctx, Entry{
			Actor: actor, Action: "unblock_account",
			ObjectType: subjectType, ObjectID: subjectID,
			Before: map[string]any{"status": "blocked"},
			After:  map[string]any{"status": "active"},
			Reason: reason,
		})
	}
	return nil
}

func (b *Blocks) List(ctx context.Context) ([]Block, error) {
	rows, err := b.pool.Query(ctx,
		`SELECT id, subject_type, subject_id, reason, blocked_by,
		        COALESCE(rule_code, ''), held_minor, COALESCE(appeal_status, 'none'), created_at
		 FROM account_blocks WHERE lifted_at IS NULL
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("compliance: list blocks: %w", err)
	}
	defer rows.Close()

	var out []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.ID, &b.SubjectType, &b.SubjectID, &b.Reason,
			&b.BlockedBy, &b.RuleCode, &b.HeldMinor, &b.AppealStatus, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("compliance: scan block: %w", err)
		}
		b.CreatedAt = b.CreatedAt.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

func (b *Blocks) heldBalance(ctx context.Context, subjectType, subjectID string) (int64, error) {
	if subjectType != SubjectUser {
		return 0, nil
	}

	var held int64
	err := b.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries
		 WHERE account_id = $1`,
		"acc_"+subjectID+"_user_wallet").Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("compliance: held balance: %w", err)
	}
	if held < 0 {
		return 0, nil
	}
	return held, nil
}

type MemoryFlags struct {
	mu     sync.Mutex
	values map[string]string
}

func NewMemoryFlags() *MemoryFlags {
	return &MemoryFlags{values: map[string]string{}}
}

func (f *MemoryFlags) Get(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	return v, ok, nil
}

func (f *MemoryFlags) Set(_ context.Context, key, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
	return nil
}

func (f *MemoryFlags) Del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
	return nil
}

func (f *MemoryFlags) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values = map[string]string{}
}

type RedisFlags struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisFlags(client redis.UniversalClient, prefix string) *RedisFlags {
	return &RedisFlags{client: client, prefix: prefix}
}

func (f *RedisFlags) Get(ctx context.Context, key string) (string, bool, error) {
	v, err := f.client.Get(ctx, f.prefix+key).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("compliance: read flag %s: %w", key, err)
	}
	return v, true, nil
}

func (f *RedisFlags) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := f.client.Set(ctx, f.prefix+key, value, ttl).Err(); err != nil {
		return fmt.Errorf("compliance: set flag %s: %w", key, err)
	}
	return nil
}

func (f *RedisFlags) Del(ctx context.Context, key string) error {
	if err := f.client.Del(ctx, f.prefix+key).Err(); err != nil {
		return fmt.Errorf("compliance: clear flag %s: %w", key, err)
	}
	return nil
}

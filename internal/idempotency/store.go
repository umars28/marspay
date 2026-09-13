package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"

	DefaultTTL = 24 * time.Hour
)

var (
	ErrKeyReused = errors.New("idempotency: key reused with a different request body")
	ErrInFlight  = errors.New("idempotency: an identical request is still in progress")
)

type Record struct {
	Scope        string
	Key          string
	RequestHash  string
	Status       string
	ResponseCode int
	ResponseBody []byte
}

func HashRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type Store struct {
	pool *pgxpool.Pool
	ttl  time.Duration
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, ttl: DefaultTTL}
}

func (s *Store) Claim(ctx context.Context, scope, key, requestHash string) (*Record, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO idempotency_keys (scope, key, request_hash, status, expires_at)
		 VALUES ($1, $2, $3, $4, now() + $5::interval)
		 ON CONFLICT (scope, key) DO NOTHING`,
		scope, key, requestHash, StatusInProgress, s.ttl.String())
	if err != nil {
		return nil, fmt.Errorf("idempotency: claim: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}

	existing, err := s.get(ctx, scope, key)
	if err != nil {
		return nil, err
	}

	if existing.RequestHash != requestHash {
		return nil, ErrKeyReused
	}
	if existing.Status == StatusInProgress {
		return nil, ErrInFlight
	}
	return existing, nil
}

func (s *Store) Complete(ctx context.Context, scope, key string, code int, body []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE idempotency_keys
		 SET status = $1, response_code = $2, response_body = $3
		 WHERE scope = $4 AND key = $5`,
		StatusCompleted, code, body, scope, key)
	if err != nil {
		return fmt.Errorf("idempotency: complete: %w", err)
	}
	return nil
}

func (s *Store) Release(ctx context.Context, scope, key string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys
		 WHERE scope = $1 AND key = $2 AND status = $3`,
		scope, key, StatusInProgress)
	if err != nil {
		return fmt.Errorf("idempotency: release: %w", err)
	}
	return nil
}

func (s *Store) get(ctx context.Context, scope, key string) (*Record, error) {
	var rec Record
	var code *int
	var body []byte

	err := s.pool.QueryRow(ctx,
		`SELECT scope, key, request_hash, status, response_code, response_body
		 FROM idempotency_keys WHERE scope = $1 AND key = $2`,
		scope, key).Scan(&rec.Scope, &rec.Key, &rec.RequestHash, &rec.Status, &code, &body)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("idempotency: key vanished between insert and read")
		}
		return nil, fmt.Errorf("idempotency: get: %w", err)
	}

	if code != nil {
		rec.ResponseCode = *code
	}
	rec.ResponseBody = body
	return &rec, nil
}

package merchant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
)

const (
	ModeLive = "live"
	ModeTest = "test"

	secretLength = 24
	prefixLength = 12

	ScopeRead  = "read"
	ScopeWrite = "write"
)

var ErrKeyNotFound = errors.New("merchant: no live key matches that token")

type Key struct {
	ID         string     `json:"id"`
	MerchantID string     `json:"merchant_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Mode       string     `json:"mode"`
	Scopes     []string   `json:"scopes"`
	Secret     string     `json:"secret,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Keys struct {
	pool  *pgxpool.Pool
	audit *compliance.Audit
}

func NewKeys(pool *pgxpool.Pool, audit *compliance.Audit) *Keys {
	return &Keys{pool: pool, audit: audit}
}

func hashKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

type CreateKeyRequest struct {
	Name   string   `json:"name"`
	Mode   string   `json:"mode"`
	Scopes []string `json:"scopes"`
}

func (k *Keys) Create(ctx context.Context, merchantID string, req CreateKeyRequest, actor string) (*Key, error) {
	if actor == "" {
		return nil, compliance.ErrNoActor
	}
	if req.Name == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field name is required so a leaked key can be identified later.")
	}
	if req.Mode != ModeLive && req.Mode != ModeTest {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field mode must be live or test.")
	}

	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = []string{ScopeRead}
	}
	for _, s := range scopes {
		if s != ScopeRead && s != ScopeWrite {
			return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
				"Scope %q is not recognised.", s)
		}
	}

	secret := "mp_" + req.Mode + "_" + id.Secret(secretLength)
	prefix := secret[:prefixLength]

	key := &Key{
		ID:         id.New("ak"),
		MerchantID: merchantID,
		Name:       req.Name,
		Prefix:     prefix,
		Mode:       req.Mode,
		Scopes:     scopes,
		Secret:     secret,
	}

	err := k.pool.QueryRow(ctx,
		`INSERT INTO api_keys (id, merchant_id, name, prefix, key_hash, mode, scopes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING created_at`,
		key.ID, merchantID, req.Name, prefix, hashKey(secret), req.Mode, scopes).
		Scan(&key.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("merchant: insert key: %w", err)
	}
	key.CreatedAt = key.CreatedAt.UTC()

	if k.audit != nil {
		if err := k.audit.Record(ctx, compliance.Entry{
			Actor: actor, Action: "create_api_key",
			ObjectType: "merchant", ObjectID: merchantID,
			After:  map[string]any{"prefix": prefix, "mode": req.Mode, "scopes": scopes},
			Reason: "merchant requested a new API key named " + req.Name,
		}); err != nil {
			return nil, err
		}
	}

	return key, nil
}

func (k *Keys) Verify(ctx context.Context, token string) (*Key, error) {
	if !strings.HasPrefix(token, "mp_") || len(token) < prefixLength+4 {
		return nil, ErrKeyNotFound
	}

	prefix := token[:prefixLength]

	var key Key
	var storedHash string
	err := k.pool.QueryRow(ctx,
		`SELECT id, merchant_id, name, prefix, key_hash, mode, scopes, created_at
		 FROM api_keys WHERE prefix = $1 AND revoked_at IS NULL`,
		prefix).Scan(&key.ID, &key.MerchantID, &key.Name, &key.Prefix,
		&storedHash, &key.Mode, &key.Scopes, &key.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("merchant: verify key: %w", err)
	}

	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(hashKey(token))) != 1 {
		return nil, ErrKeyNotFound
	}

	if _, err := k.pool.Exec(ctx,
		`UPDATE api_keys SET last_used_at = now() WHERE id = $1`, key.ID); err != nil {
		return nil, fmt.Errorf("merchant: touch key: %w", err)
	}

	key.CreatedAt = key.CreatedAt.UTC()
	return &key, nil
}

func (k *Keys) List(ctx context.Context, merchantID string) ([]Key, error) {
	rows, err := k.pool.Query(ctx,
		`SELECT id, merchant_id, name, prefix, mode, scopes, last_used_at, revoked_at, created_at
		 FROM api_keys WHERE merchant_id = $1 ORDER BY created_at DESC`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant: list keys: %w", err)
	}
	defer rows.Close()

	var out []Key
	for rows.Next() {
		var key Key
		if err := rows.Scan(&key.ID, &key.MerchantID, &key.Name, &key.Prefix,
			&key.Mode, &key.Scopes, &key.LastUsedAt, &key.RevokedAt, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("merchant: scan key: %w", err)
		}
		key.CreatedAt = key.CreatedAt.UTC()
		out = append(out, key)
	}
	return out, rows.Err()
}

func (k *Keys) Revoke(ctx context.Context, merchantID, keyID, actor, reason string) error {
	if actor == "" {
		return compliance.ErrNoActor
	}
	if reason == "" {
		return compliance.ErrNoReason
	}

	var prefix string
	err := k.pool.QueryRow(ctx,
		`UPDATE api_keys SET revoked_at = now()
		 WHERE id = $1 AND merchant_id = $2 AND revoked_at IS NULL
		 RETURNING prefix`,
		keyID, merchantID).Scan(&prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Key %s was not found or is already revoked.", keyID)
	}
	if err != nil {
		return fmt.Errorf("merchant: revoke key: %w", err)
	}

	if k.audit != nil {
		return k.audit.Record(ctx, compliance.Entry{
			Actor: actor, Action: "revoke_api_key",
			ObjectType: "merchant", ObjectID: merchantID,
			Before: map[string]any{"prefix": prefix, "status": "active"},
			After:  map[string]any{"prefix": prefix, "status": "revoked"},
			Reason: reason,
		})
	}
	return nil
}

func (k Key) Can(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

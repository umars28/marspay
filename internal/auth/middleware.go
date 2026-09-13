package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/httpx"
)

const (
	AccessPrefix  = "mp_at_"
	RefreshPrefix = "mp_rt_"
	MerchantLive  = "mp_live_"
	MerchantTest  = "mp_test_"
)

func DeviceID(ctx context.Context) string {
	v, _ := ctx.Value(deviceIDKey).(string)
	return v
}

func SessionID(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}

const RoleConsumer, RoleOperator = "consumer", "operator"

func Role(ctx context.Context) string {
	v, _ := ctx.Value(roleKey).(string)
	return v
}

func IsOperator(ctx context.Context) bool {
	return Role(ctx) == RoleOperator
}

func withPrincipal(ctx context.Context, p *Principal) context.Context {
	ctx = WithUserID(ctx, p.UserID)
	ctx = context.WithValue(ctx, deviceIDKey, p.DeviceID)
	ctx = context.WithValue(ctx, roleKey, p.Role)
	return context.WithValue(ctx, sessionIDKey, p.SessionID)
}

func BearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func Bearer(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := BearerToken(r)
			if token == "" ||
				strings.HasPrefix(token, MerchantLive) ||
				strings.HasPrefix(token, MerchantTest) {
				next.ServeHTTP(w, r)
				return
			}

			if !strings.HasPrefix(token, AccessPrefix) {
				httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
					httpx.TypeUnauthorized,
					"This credential is not an access token."))
				return
			}

			p, err := store.Verify(r.Context(), token)
			if err != nil {
				httpx.WriteError(w, r, TranslateError(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
		})
	}
}

func TranslateError(err error) error {
	switch {
	case errors.Is(err, ErrNoSession), errors.Is(err, ErrNoUser):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"This credential is not valid.")
	case errors.Is(err, ErrSessionExpired):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"This access token has expired; refresh it.")
	case errors.Is(err, ErrTokenReplayed):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"This refresh token was already used. Every session on this device has been "+
				"signed out because a replayed refresh token usually means the token was stolen.")
	case errors.Is(err, ErrNotActive):
		return httpx.Errorf(http.StatusForbidden, httpx.TypeForbidden,
			"This account is not active.")
	case errors.Is(err, ErrWrongPin):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"That PIN is not correct.")
	case errors.Is(err, ErrPinLocked):
		return httpx.Errorf(http.StatusForbidden, httpx.TypeForbidden,
			"PIN entry is locked for %s after too many wrong attempts.", PinLockout)
	case errors.Is(err, ErrWrongCode):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"That code is not correct.")
	case errors.Is(err, ErrTooManyAttempts):
		return httpx.Errorf(http.StatusForbidden, httpx.TypeForbidden,
			"Too many attempts on this code. Request a new one.")
	case errors.Is(err, ErrChallengeStale), errors.Is(err, ErrChallengeSpent),
		errors.Is(err, ErrNoChallenge):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"This code is no longer usable. Request a new one.")
	case errors.Is(err, ErrPinFormat):
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"A PIN must be exactly six digits.")
	default:
		return err
	}
}

type RedisCache struct {
	rdb    *redis.Client
	prefix string
}

func NewRedisCache(rdb *redis.Client, prefix string) *RedisCache {
	return &RedisCache{rdb: rdb, prefix: prefix}
}

func (c *RedisCache) Get(ctx context.Context, key string) (string, bool) {
	v, err := c.rdb.Get(ctx, c.prefix+key).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

func (c *RedisCache) Set(ctx context.Context, key, value string, ttl time.Duration) {
	_ = c.rdb.Set(ctx, c.prefix+key, value, ttl).Err()
}

func (c *RedisCache) Del(ctx context.Context, key string) {
	_ = c.rdb.Del(ctx, c.prefix+key).Err()
}

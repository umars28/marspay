package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey int

const (
	userIDKey ctxKey = iota
	merchantIDKey
)

func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

func MerchantID(ctx context.Context) string {
	v, _ := ctx.Value(merchantIDKey).(string)
	return v
}

func WithMerchantID(ctx context.Context, merchantID string) context.Context {
	return context.WithValue(ctx, merchantIDKey, merchantID)
}

func DevBearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
		if token == "" || strings.HasPrefix(token, "mp_") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUserID(r.Context(), token)))
	})
}

func ScopeFromRequest(r *http.Request) string {
	if userID := UserID(r.Context()); userID != "" {
		return userID
	}
	return "anonymous"
}

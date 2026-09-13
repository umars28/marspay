package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/umars28/marspay/internal/id"
)

type ctxKey int

const requestIDKey ctxKey = iota

const (
	TypeInvalidRequest   = "invalid_request"
	TypeUnknownField     = "unknown_field"
	TypeUnauthorized     = "unauthorized"
	TypeForbidden        = "forbidden"
	TypeNotFound         = "not_found"
	TypeConflict         = "conflict"
	TypeInsufficient     = "insufficient_balance"
	TypeKeyMissing       = "idempotency_key_missing"
	TypeKeyReused        = "idempotency_key_reused"
	TypeKeyInProgress    = "idempotency_key_in_progress"
	TypeRateLimited      = "rate_limited"
	TypeOverloaded       = "service_overloaded"
	TypeInternal         = "internal_error"
	TypeServiceUnhealthy = "service_unavailable"
)

type APIError struct {
	Status  int            `json:"-"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func Errorf(status int, typ, format string, args ...any) *APIError {
	return &APIError{Status: status, Type: typ, Message: fmt.Sprintf(format, args...)}
}

func (e *APIError) WithDetails(d map[string]any) *APIError {
	e.Details = d
	return e
}

type envelope struct {
	Error struct {
		Type      string         `json:"type"`
		Message   string         `json:"message"`
		RequestID string         `json:"request_id"`
		Details   map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		slog.ErrorContext(r.Context(), "unhandled error",
			"request_id", RequestID(r.Context()), "error", err)
		apiErr = Errorf(http.StatusInternalServerError, TypeInternal,
			"Something went wrong on our side.")
	}

	var env envelope
	env.Error.Type = apiErr.Type
	env.Error.Message = apiErr.Message
	env.Error.RequestID = RequestID(r.Context())
	env.Error.Details = apiErr.Details

	WriteJSON(w, apiErr.Status, env)
}

func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = id.New("req")
		}
		w.Header().Set("X-Request-Id", rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, rid)))
	})
}

const maxBodyBytes = 1 << 20

func DecodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		var unknown *json.UnmarshalTypeError
		if errors.As(err, &unknown) {
			return Errorf(http.StatusUnprocessableEntity, TypeInvalidRequest,
				"Field %q expects a %s.", unknown.Field, unknown.Type)
		}
		return Errorf(http.StatusUnprocessableEntity, TypeUnknownField,
			"Request body could not be parsed: %s", err)
	}

	if dec.More() {
		return Errorf(http.StatusUnprocessableEntity, TypeInvalidRequest,
			"Request body must contain exactly one JSON object.")
	}
	return nil
}

package idempotency

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/umars28/marspay/internal/httpx"
)

const HeaderKey = "Idempotency-Key"

type ScopeFunc func(*http.Request) string

func Middleware(store *Store, scopeOf ScopeFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			key := r.Header.Get(HeaderKey)
			if key == "" {
				httpx.WriteError(w, r, httpx.Errorf(http.StatusBadRequest, httpx.TypeKeyMissing,
					"Header %s is required on this endpoint.", HeaderKey))
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				httpx.WriteError(w, r, httpx.Errorf(http.StatusBadRequest,
					httpx.TypeInvalidRequest, "Request body could not be read."))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			scope := scopeOf(r)
			hash := HashRequest(r.Method, r.URL.Path, body)

			existing, err := store.Claim(r.Context(), scope, key, hash)
			switch {
			case errors.Is(err, ErrKeyReused):
				httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
					httpx.TypeKeyReused,
					"This idempotency key was already used with a different request body."))
				return
			case errors.Is(err, ErrInFlight):
				w.Header().Set("Retry-After", "1")
				httpx.WriteError(w, r, httpx.Errorf(http.StatusConflict,
					httpx.TypeKeyInProgress,
					"An identical request is still being processed. Retry shortly."))
				return
			case err != nil:
				httpx.WriteError(w, r, err)
				return
			}

			if existing != nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(existing.ResponseCode)
				_, _ = w.Write(existing.ResponseBody)
				return
			}

			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			if rec.status >= 500 {
				_ = store.Release(r.Context(), scope, key)
				return
			}
			_ = store.Complete(r.Context(), scope, key, rec.status, rec.body.Bytes())
		})
	}
}

type recorder struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (w *recorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

package api

import (
	"net/http"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/payment"
)

type Deps struct {
	Payments    *payment.Service
	Idempotency *idempotency.Store
}

func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	payments := payment.NewHandler(d.Payments)
	guard := idempotency.Middleware(d.Idempotency, payment.ScopeFromUser)

	mux.Handle("POST /v1/payments", guard(http.HandlerFunc(payments.Create)))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No route matches %s %s.", r.Method, r.URL.Path))
	})

	return httpx.WithRequestID(payment.DevBearerAuth(mux))
}

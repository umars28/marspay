package api

import (
	"net/http"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/payment"
	"github.com/umars28/marspay/internal/txn"
)

type Deps struct {
	Payments    *payment.Service
	Txn         *txn.Service
	Idempotency *idempotency.Store
}

func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	payments := payment.NewHandler(d.Payments)
	movements := txn.NewHandler(d.Txn)
	guard := idempotency.Middleware(d.Idempotency, auth.ScopeFromRequest)

	mux.Handle("POST /v1/payments", guard(http.HandlerFunc(payments.Create)))
	mux.Handle("POST /v1/transfers", guard(http.HandlerFunc(movements.CreateTransfer)))
	mux.Handle("POST /v1/withdrawals", guard(http.HandlerFunc(movements.CreateWithdrawal)))
	mux.Handle("POST /v1/topups", guard(http.HandlerFunc(movements.CreateTopup)))
	mux.Handle("POST /v1/bill-payments", guard(http.HandlerFunc(movements.PayBill)))
	mux.HandleFunc("GET /v1/billers", movements.ListBillers)
	mux.HandleFunc("POST /v1/billers/{code}/inquire", movements.Inquire)
	mux.HandleFunc("POST /v1/callbacks/{provider}", movements.ProviderCallback)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No route matches %s %s.", r.Method, r.URL.Path))
	})

	return httpx.WithRequestID(auth.DevBearerAuth(mux))
}

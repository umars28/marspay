package api

import (
	"net/http"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/payment"
	"github.com/umars28/marspay/internal/txn"
	"github.com/umars28/marspay/internal/velocity"
)

type Deps struct {
	Payments    *payment.Service
	Txn         *txn.Service
	Idempotency *idempotency.Store
	Velocity    *velocity.Guard
	Blocks      *compliance.Blocks
	Audit       *compliance.Audit
}

func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	if d.Velocity != nil {
		d.Payments = d.Payments.WithVelocity(d.Velocity)
		d.Txn = d.Txn.WithVelocity(d.Velocity)
	}
	if d.Blocks != nil {
		d.Payments = d.Payments.WithBlocks(d.Blocks)
		d.Txn = d.Txn.WithBlocks(d.Blocks)
	}

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
	mux.Handle("POST /v1/refunds", guard(http.HandlerFunc(movements.CreateRefund)))
	mux.HandleFunc("POST /v1/callbacks/{provider}", movements.ProviderCallback)

	mux.HandleFunc("GET /v1/balance", movements.Balance)
	mux.HandleFunc("GET /v1/transactions", movements.History)
	mux.HandleFunc("GET /v1/me", movements.Profile)

	if d.Blocks != nil && d.Audit != nil {
		ops := compliance.NewHandler(d.Blocks, d.Audit)
		mux.HandleFunc("GET /internal/v1/blocks", ops.ListBlocks)
		mux.HandleFunc("POST /internal/v1/blocks", ops.Block)
		mux.HandleFunc("POST /internal/v1/blocks/{subject_type}/{subject_id}/unblock", ops.Unblock)
		mux.HandleFunc("GET /internal/v1/audit", ops.Audit)
		mux.HandleFunc("GET /internal/v1/audit/{object_type}/{object_id}", ops.AuditFor)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No route matches %s %s.", r.Method, r.URL.Path))
	})

	return httpx.WithRequestID(auth.DevBearerAuth(mux))
}

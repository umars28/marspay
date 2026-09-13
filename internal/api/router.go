package api

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/admission"
	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/console"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/loyalty"
	"github.com/umars28/marspay/internal/merchant"
	"github.com/umars28/marspay/internal/payment"
	"github.com/umars28/marspay/internal/risk"
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
	KYC         *compliance.KYC
	Disputes    *compliance.Disputes
	Keys        *merchant.Keys
	Outlets     *merchant.Outlets
	Loyalty     *loyalty.Service
	Scores      *risk.Store
	Pool        *pgxpool.Pool
	Admission   *admission.Limiter
	Auth        *auth.Store
	RevealOTP   bool
	CORSOrigins []string
	Console     *console.Store
}

func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	if d.Velocity != nil {
		if d.Payments != nil {
			d.Payments = d.Payments.WithVelocity(d.Velocity)
		}
		if d.Txn != nil {
			d.Txn = d.Txn.WithVelocity(d.Velocity)
		}
	}
	if d.Blocks != nil {
		if d.Payments != nil {
			d.Payments = d.Payments.WithBlocks(d.Blocks)
		}
		if d.Txn != nil {
			d.Txn = d.Txn.WithBlocks(d.Blocks)
		}
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
	mux.HandleFunc("GET /v1/notifications", movements.Notifications)
	mux.HandleFunc("GET /v1/me", movements.Profile)

	if d.Blocks != nil && d.Audit != nil {
		ops := compliance.NewHandler(d.Blocks, d.Audit, d.KYC, d.Disputes)
		mux.HandleFunc("GET /internal/v1/blocks", ops.ListBlocks)
		mux.HandleFunc("POST /internal/v1/blocks", ops.Block)
		mux.HandleFunc("POST /internal/v1/blocks/{subject_type}/{subject_id}/unblock", ops.Unblock)
		mux.HandleFunc("GET /internal/v1/audit", ops.Audit)
		mux.HandleFunc("GET /internal/v1/audit/{object_type}/{object_id}", ops.AuditFor)

		if d.KYC != nil {
			mux.HandleFunc("POST /v1/me/kyc", ops.SubmitKYC)
			mux.HandleFunc("GET /internal/v1/kyc", ops.KYCQueue)
			mux.HandleFunc("POST /internal/v1/kyc/{id}/review", ops.ReviewKYC)
		}
		if d.Disputes != nil {
			mux.HandleFunc("POST /v1/disputes", ops.OpenDispute)
			mux.HandleFunc("GET /internal/v1/disputes", ops.DisputeQueue)
			mux.HandleFunc("POST /internal/v1/disputes/{id}/resolve", ops.ResolveDispute)
		}
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	if d.Pool != nil {
		mux.HandleFunc("GET /internal/v1/saturation", poolStats(d.Pool, d.Admission))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No route matches %s %s.", r.Method, r.URL.Path))
	})

	if d.Keys != nil && d.Outlets != nil {
		m := merchant.NewHandler(d.Keys, d.Outlets)
		mux.HandleFunc("POST /v1/api-keys", m.CreateKey)
		mux.HandleFunc("GET /v1/api-keys", m.ListKeys)
		mux.HandleFunc("POST /v1/api-keys/{id}/revoke", m.RevokeKey)
		mux.HandleFunc("POST /v1/outlets", m.CreateOutlet)
		mux.HandleFunc("GET /v1/outlets", m.ListOutlets)
		mux.HandleFunc("POST /v1/staff", m.AddStaff)
		mux.HandleFunc("GET /v1/staff", m.ListStaff)
		mux.HandleFunc("POST /v1/staff/{id}/revoke", m.RevokeStaff)
	}

	if d.Loyalty != nil {
		l := loyalty.NewHandler(d.Loyalty)
		mux.HandleFunc("GET /v1/points", l.Points)
		mux.HandleFunc("POST /v1/points/redeem", l.Redeem)
		mux.HandleFunc("GET /v1/promos", l.Promos)
		mux.HandleFunc("POST /v1/promos/apply", l.ApplyPromo)
		mux.HandleFunc("POST /v1/money-requests", l.RequestMoney)
		mux.HandleFunc("GET /v1/money-requests", l.Incoming)
		mux.HandleFunc("POST /v1/money-requests/{id}/decline", l.Decline)
		mux.HandleFunc("POST /v1/bill-splits", l.CreateSplit)
	}

	if d.Scores != nil {
		scores := risk.NewHandler(d.Scores)
		mux.HandleFunc("GET /v1/payouts/config", scores.PayoutConfig)
		mux.HandleFunc("GET /internal/v1/merchants/{id}/score", scores.MerchantScore)
	}

	if d.Console != nil {
		c := console.NewHandler(d.Console)

		mux.HandleFunc("GET /v1/payments", c.Payments)
		mux.HandleFunc("GET /v1/payments/{id}", c.Payment)
		mux.HandleFunc("GET /v1/payouts", c.Payouts)
		mux.HandleFunc("GET /v1/settlements", c.Settlements)
		mux.HandleFunc("GET /v1/webhook-endpoints", c.Endpoints)
		mux.HandleFunc("GET /v1/webhook-deliveries", c.Deliveries)

		mux.HandleFunc("GET /internal/v1/search", c.Search)
		mux.HandleFunc("GET /internal/v1/float", c.Float)
		mux.HandleFunc("GET /internal/v1/payouts", c.OpsPayouts)
		mux.HandleFunc("GET /internal/v1/payouts/engine", c.Engine)
		mux.HandleFunc("GET /internal/v1/reconciliation", c.Reconciliation)
		mux.HandleFunc("GET /internal/v1/payments/{id}/ledger", c.Ledger)
		mux.HandleFunc("GET /internal/v1/velocity/rules", c.Rules)
		mux.HandleFunc("GET /internal/v1/velocity/alerts", c.Alerts)
	}

	if d.Auth != nil {
		a := auth.NewHandler(d.Auth, d.RevealOTP)
		mux.HandleFunc("POST /v1/auth/otp", a.RequestOTP)
		mux.HandleFunc("POST /v1/auth/token", a.Token)
		mux.HandleFunc("POST /v1/auth/refresh", a.Refresh)
		mux.HandleFunc("POST /v1/auth/logout", a.Logout)
		mux.HandleFunc("GET /v1/devices", a.ListDevices)
		mux.HandleFunc("POST /v1/devices/{id}/revoke", a.RevokeDevice)
	}

	handler := http.Handler(mux)
	if d.Keys != nil {
		handler = merchant.APIKeyAuth(d.Keys)(handler)
	}

	if d.Auth != nil {
		handler = auth.Bearer(d.Auth)(handler)
	} else {
		handler = auth.DevBearerAuth(handler)
	}

	if d.Admission != nil {
		handler = admission.Skip(alwaysAnswer, d.Admission.Middleware)(handler)
	}
	return CORS(d.CORSOrigins)(httpx.WithRequestID(handler))
}

func alwaysAnswer(r *http.Request) bool {
	return r.URL.Path == "/healthz" || r.URL.Path == "/internal/v1/saturation"
}

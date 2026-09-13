package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/risk"
	"github.com/umars28/marspay/internal/testdb"
	"github.com/umars28/marspay/internal/wallet"
)

func signIn(t *testing.T, pool *pgxpool.Pool, ctx context.Context, store *auth.Store, phone, role string) *auth.Tokens {
	t.Helper()

	pin := "294715"
	hash, err := auth.HashPin(pin)
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}

	userID := id.New("usr")
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status, role)
		 VALUES ($1, $2, 'Role Test', $3, 'verified', 'active', $4)`,
		userID, phone, hash, role); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	challenge, err := store.StartOTP(ctx, phone)
	if err != nil {
		t.Fatalf("start otp: %v", err)
	}
	if _, err := store.VerifyOTP(ctx, challenge.ID, challenge.Code); err != nil {
		t.Fatalf("verify otp: %v", err)
	}

	tokens, err := store.Login(ctx, auth.LoginRequest{
		ChallengeID: challenge.ID, Phone: phone, Pin: pin, Platform: "web",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	return tokens
}

func roleRouter(t *testing.T) (http.Handler, *pgxpool.Pool, context.Context, *auth.Store) {
	t.Helper()

	pool, ctx := testdb.New(t)
	store := auth.NewStore(pool)
	audit := compliance.NewAudit(pool)

	router := NewRouter(Deps{
		Audit:    audit,
		Blocks:   compliance.NewBlocks(pool, compliance.NewMemoryFlags(), audit),
		KYC:      compliance.NewKYC(pool, audit),
		Disputes: compliance.NewDisputes(pool, ledger.NewRepo(pool), wallet.NewMemory(), audit),
		Scores:   risk.NewStore(pool),
		Auth:     store,
	})
	return router, pool, ctx, store
}

func callAs(t *testing.T, router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()

	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestAConsumerSessionCannotReachOperatorEndpoints(t *testing.T) {
	router, pool, ctx, store := roleRouter(t)
	consumer := signIn(t, pool, ctx, store, "081277770001", auth.RoleConsumer)

	for _, path := range []string{
		"/internal/v1/audit",
		"/internal/v1/blocks",
	} {
		rec := callAs(t, router, http.MethodGet, path, consumer.AccessToken, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as a consumer = %d, want 403 (body %s)",
				path, rec.Code, rec.Body.String())
		}
	}

	rec := callAs(t, router, http.MethodPost, "/internal/v1/blocks", consumer.AccessToken,
		`{"subject_type":"user","subject_id":"usr_x","reason":"testing"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a consumer could block an account: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAnOperatorSessionCanReachOperatorEndpoints(t *testing.T) {
	router, pool, ctx, store := roleRouter(t)
	op := signIn(t, pool, ctx, store, "081277770002", auth.RoleOperator)

	rec := callAs(t, router, http.MethodGet, "/internal/v1/audit", op.AccessToken, "")
	if rec.Code != http.StatusOK {
		t.Errorf("GET /internal/v1/audit as an operator = %d, want 200 (body %s)",
			rec.Code, rec.Body.String())
	}
}

func TestNoTokenIsStillUnauthorisedRatherThanForbidden(t *testing.T) {
	router, _, _, _ := roleRouter(t)

	rec := callAs(t, router, http.MethodGet, "/internal/v1/audit", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /internal/v1/audit with no token = %d, want 401", rec.Code)
	}
}

func TestAConsumerCanStillUseTheConsumerHalfOfCompliance(t *testing.T) {
	router, pool, ctx, store := roleRouter(t)
	consumer := signIn(t, pool, ctx, store, "081277770003", auth.RoleConsumer)

	rec := callAs(t, router, http.MethodPost, "/v1/me/kyc", consumer.AccessToken,
		`{"id_number":"3175000000000001"}`)
	if rec.Code == http.StatusForbidden {
		t.Errorf("a consumer was refused its own KYC submission: %s", rec.Body.String())
	}

	rec = callAs(t, router, http.MethodPost, "/v1/disputes", consumer.AccessToken,
		`{"payment_id":"pay_does_not_exist","reason":"never arrived"}`)
	if rec.Code == http.StatusForbidden {
		t.Errorf("a consumer was refused opening a dispute: %s", rec.Body.String())
	}
}

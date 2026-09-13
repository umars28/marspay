package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/idempotency"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/payment"
	"github.com/umars28/marspay/internal/testdb"
	"github.com/umars28/marspay/internal/txn"
	"github.com/umars28/marspay/internal/velocity"
	"github.com/umars28/marspay/internal/wallet"
)

type fixture struct {
	router     http.Handler
	pool       *pgxpool.Pool
	wallet     *wallet.Memory
	ledger     *ledger.Repo
	counter    *velocity.MemoryCounter
	userID     string
	merchantID string
}

func newFixture(t *testing.T) (*fixture, context.Context) {
	t.Helper()

	pool, ctx := testdb.New(t)

	userID := id.New("usr")
	merchantID := id.New("merch")
	seed(t, ctx, pool, userID, merchantID)

	repo := ledger.NewRepo(pool)
	mem := wallet.NewMemory()
	if err := mem.Warm(ctx, ledger.UserWallet(userID), money.FromRupiah(100_000)); err != nil {
		t.Fatalf("warm: %v", err)
	}

	store := velocity.NewStore(pool)
	if err := store.SyncRules(ctx, velocity.DefaultRules()); err != nil {
		t.Fatalf("sync velocity rules: %v", err)
	}
	counter := velocity.NewMemoryCounter()

	return &fixture{
		router: NewRouter(Deps{
			Payments:    payment.NewService(pool, repo, mem),
			Txn:         txn.NewService(pool, repo, mem),
			Idempotency: idempotency.NewStore(pool),
			Velocity: velocity.NewGuard(
				velocity.NewEngine(counter, velocity.DefaultRules()), store),
		}),
		pool:       pool,
		wallet:     mem,
		ledger:     repo,
		counter:    counter,
		userID:     userID,
		merchantID: merchantID,
	}, ctx
}

func seed(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, merchantID string) {
	t.Helper()

	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ($1, $2, 'Test User', 'x', 'verified', 'active')`,
		userID, "0812"+id.ULID()[16:24])
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
		 VALUES ($1, 'PT Test', 'Kopi Test', 'food', 'active', 70)`,
		merchantID)
	if err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	repo := ledger.NewRepo(pool)
	accounts := [][4]string{
		{ledger.UserWallet(userID), ledger.OwnerUser, userID, ledger.TypeUserWallet},
		{ledger.MerchantPayable(merchantID), ledger.OwnerMerchant, merchantID, ledger.TypeMerchantPayable},
		{ledger.PlatformFeeRevenue(), ledger.OwnerPlatform, "", ledger.TypeFeeRevenue},
	}
	for _, a := range accounts {
		if err := repo.EnsureAccount(ctx, a[0], a[1], a[2], a[3]); err != nil {
			t.Fatalf("seed account %s: %v", a[0], err)
		}
	}
}

func (f *fixture) post(t *testing.T, key string, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/v1/payments", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.userID)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) validBody(amount int64) string {
	return `{"merchant_id":"` + f.merchantID + `","method":"qris","amount":` +
		itoa(amount) + `,"currency":"IDR"}`
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodePayment(t *testing.T, rec *httptest.ResponseRecorder) payment.Payment {
	t.Helper()
	var p payment.Payment
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode payment: %v (body %s)", err, rec.Body.String())
	}
	return p
}

func decodeErrorType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error: %v (body %s)", err, rec.Body.String())
	}
	if env.Error.RequestID == "" {
		t.Error("error envelope has no request_id")
	}
	return env.Error.Type
}

func TestCreatePaymentHappyPath(t *testing.T) {
	f, ctx := newFixture(t)

	rec := f.post(t, id.ULID(), f.validBody(3_200_000))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	p := decodePayment(t, rec)
	if p.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", p.Status)
	}
	if p.Fee != 22_400 {
		t.Errorf("fee = %d, want 22400", p.Fee)
	}
	if p.BalanceAfter != 6_800_000 {
		t.Errorf("balance_after = %d, want 6800000", p.BalanceAfter)
	}
	if p.LedgerTransactionID == "" {
		t.Error("ledger_transaction_id is empty")
	}

	entries, err := f.ledger.Entries(ctx, p.LedgerTransactionID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("ledger entries = %d, want 3", len(entries))
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestASuccessfulPaymentLeavesExactlyOneOutboxMessage(t *testing.T) {
	f, ctx := newFixture(t)

	rec := f.post(t, id.ULID(), f.validBody(3_200_000))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	p := decodePayment(t, rec)

	var topic, key, eventType string
	var payload []byte
	var publishedAt *string
	err := f.pool.QueryRow(ctx,
		`SELECT topic, partition_key, event_type, payload, published_at::text FROM outbox`).
		Scan(&topic, &key, &eventType, &payload, &publishedAt)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}

	if topic != "payment.events" {
		t.Errorf("topic = %q, want payment.events", topic)
	}
	if key != f.merchantID {
		t.Errorf("partition key = %q, want the merchant id %q", key, f.merchantID)
	}
	if eventType != "payment.succeeded" {
		t.Errorf("event type = %q, want payment.succeeded", eventType)
	}
	if publishedAt != nil {
		t.Error("the message is already marked published; the relay has not run yet")
	}
	if !bytes.Contains(payload, []byte(p.ID)) {
		t.Errorf("payload does not mention the payment id %s", p.ID)
	}
}

func TestAFailedPaymentLeavesNoOutboxMessage(t *testing.T) {
	f, ctx := newFixture(t)

	if rec := f.post(t, id.ULID(), f.validBody(20_000_000)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}

	var messages int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&messages); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if messages != 0 {
		t.Errorf("outbox has %d messages after a rejected payment, want 0", messages)
	}
}

func TestReplayWithSameKeyReturnsTheOriginalPayment(t *testing.T) {
	f, ctx := newFixture(t)

	key := id.ULID()
	body := f.validBody(3_200_000)

	first := f.post(t, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", first.Code)
	}
	second := f.post(t, key, body)

	if second.Code != http.StatusCreated {
		t.Errorf("replay status = %d, want 201", second.Code)
	}
	if second.Header().Get("Idempotent-Replay") != "true" {
		t.Error("replay did not set Idempotent-Replay")
	}

	a, b := decodePayment(t, first), decodePayment(t, second)
	if a.ID != b.ID {
		t.Errorf("replay returned a different payment: %s vs %s", a.ID, b.ID)
	}
	if a.LedgerTransactionID != b.LedgerTransactionID {
		t.Error("replay produced a different ledger transaction")
	}

	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Errorf("replay body is not byte-identical:\nfirst:  %s\nsecond: %s",
			first.Body.String(), second.Body.String())
	}

	balance, err := f.wallet.Available(ctx, ledger.UserWallet(f.userID))
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if balance != 6_800_000 {
		t.Errorf("balance = %d, want 6800000 (replay charged twice)", balance)
	}
}

func TestReplayWithSameKeyButDifferentBodyIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	key := id.ULID()
	if rec := f.post(t, key, f.validBody(3_200_000)); rec.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", rec.Code)
	}

	rec := f.post(t, key, f.validBody(9_900_000))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "idempotency_key_reused" {
		t.Errorf("error type = %q, want idempotency_key_reused", got)
	}
}

func TestMissingIdempotencyKeyIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.post(t, "", f.validBody(3_200_000))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := decodeErrorType(t, rec); got != "idempotency_key_missing" {
		t.Errorf("error type = %q, want idempotency_key_missing", got)
	}
}

func TestInsufficientBalanceIsRejectedAndNothingIsPosted(t *testing.T) {
	f, ctx := newFixture(t)

	rec := f.post(t, id.ULID(), f.validBody(20_000_000))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "insufficient_balance" {
		t.Errorf("error type = %q, want insufficient_balance", got)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}

	balance, err := f.wallet.Available(ctx, ledger.UserWallet(f.userID))
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if balance != 10_000_000 {
		t.Errorf("balance = %d, want 10000000 (a rejected payment moved money)", balance)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"merchant_id":"` + f.merchantID +
		`","method":"qris","amount":3200000,"currency":"IDR","ammount":99}`

	rec := f.post(t, id.ULID(), body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "unknown_field" {
		t.Errorf("error type = %q, want unknown_field", got)
	}
}

func TestUnknownMerchantIsNotFound(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"merchant_id":"merch_does_not_exist","method":"qris","amount":3200000,"currency":"IDR"}`
	rec := f.post(t, id.ULID(), body)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "not_found" {
		t.Errorf("error type = %q, want not_found", got)
	}
}

func TestColdCacheRebuildsBalanceFromLedger(t *testing.T) {
	f, ctx := newFixture(t)

	walletAccount := ledger.UserWallet(f.userID)

	topup, err := ledger.NewMerchantPayment(ledger.MerchantPayment{
		TransactionID:    id.New("txn"),
		PayerAccountID:   ledger.PlatformFeeRevenue(),
		PayableAccountID: walletAccount,
		FeeAccountID:     ledger.PlatformFeeRevenue(),
		Amount:           money.FromRupiah(50_000),
		FeeBps:           0,
	})
	if err != nil {
		t.Fatalf("build topup: %v", err)
	}
	if err := f.ledger.Post(ctx, topup); err != nil {
		t.Fatalf("post topup: %v", err)
	}

	f.wallet = wallet.NewMemory()
	f.router = NewRouter(Deps{
		Payments:    payment.NewService(f.pool, f.ledger, f.wallet),
		Txn:         txn.NewService(f.pool, f.ledger, f.wallet),
		Idempotency: idempotency.NewStore(f.pool),
	})

	rec := f.post(t, id.ULID(), f.validBody(1_000_000))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	p := decodePayment(t, rec)
	if p.BalanceAfter != 4_000_000 {
		t.Errorf("balance_after = %d, want 4000000 rebuilt from the ledger", p.BalanceAfter)
	}
}

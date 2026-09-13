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
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/txn"
)

func (f *fixture) postTo(t *testing.T, path, key, body, asUser string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+asUser)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) seedPeer(t *testing.T, ctx context.Context) string {
	t.Helper()

	peerID := id.New("usr")
	_, err := f.pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ($1, $2, 'Rani Wulandari', 'x', 'verified', 'active')`,
		peerID, "0813"+id.ULID()[16:24])
	if err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	if err := ledger.NewRepo(f.pool).EnsureAccount(ctx,
		ledger.UserWallet(peerID), ledger.OwnerUser, peerID, ledger.TypeUserWallet); err != nil {
		t.Fatalf("seed peer account: %v", err)
	}
	return peerID
}

func decodeTransfer(t *testing.T, rec *httptest.ResponseRecorder) txn.Transfer {
	t.Helper()
	var v txn.Transfer
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode transfer: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func decodeWithdrawal(t *testing.T, rec *httptest.ResponseRecorder) txn.Withdrawal {
	t.Helper()
	var v txn.Withdrawal
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode withdrawal: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func balanceOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, account string) money.Minor {
	t.Helper()
	balance, err := ledger.NewRepo(pool).Balance(ctx, account)
	if err != nil {
		t.Fatalf("balance of %s: %v", account, err)
	}
	return balance
}

func TestTransferMovesMoneyBetweenTwoWallets(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	body := `{"to":"` + peerID + `","amount":1500000,"currency":"IDR","note":"lunch"}`
	rec := f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeTransfer(t, rec)
	if got.Payee.ID != peerID {
		t.Errorf("payee = %q, want %q", got.Payee.ID, peerID)
	}
	if got.BalanceAfter != 8_500_000 {
		t.Errorf("balance_after = %d, want 8500000", got.BalanceAfter)
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != -1_500_000 {
		t.Errorf("payer ledger = %d, want -1500000", b)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(peerID)); b != 1_500_000 {
		t.Errorf("payee ledger = %d, want 1500000", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestTransferInvalidatesThePayeeCacheRatherThanGuessingIt(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)
	peerAccount := ledger.UserWallet(peerID)

	if err := f.wallet.Warm(ctx, peerAccount, 999); err != nil {
		t.Fatalf("warm: %v", err)
	}

	body := `{"to":"` + peerID + `","amount":1500000,"currency":"IDR"}`
	if rec := f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	if _, err := f.wallet.Available(ctx, peerAccount); err == nil {
		t.Error("the stale payee cache survived the transfer")
	}
}

func TestTransferToYourselfIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"to":"` + f.userID + `","amount":100000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestTransferToAnUnknownAccountIsNotFound(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"to":"0899-does-not-exist","amount":100000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "not_found" {
		t.Errorf("error type = %q, want not_found", got)
	}
}

func TestTransferBeyondTheBalanceLeavesBothSidesUntouched(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	body := `{"to":"` + peerID + `","amount":99000000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID)

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
	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(peerID)); b != 0 {
		t.Errorf("payee ledger = %d, want 0", b)
	}
}

func TestReplayingATransferDoesNotSendTwice(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	key := id.ULID()
	body := `{"to":"` + peerID + `","amount":1500000,"currency":"IDR"}`

	first := f.postTo(t, "/v1/transfers", key, body, f.userID)
	second := f.postTo(t, "/v1/transfers", key, body, f.userID)

	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d and %d, want 201 twice", first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Error("the replay returned a different body")
	}
	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(peerID)); b != 1_500_000 {
		t.Errorf("payee ledger = %d, want 1500000: the replay moved money twice", b)
	}
}

func TestWithdrawalDebitsAmountPlusFeeAndSitsPending(t *testing.T) {
	f, ctx := newFixture(t)

	body := `{"bank_code":"BCA","account_number":"4471","account_name":"Test User",` +
		`"amount":1000000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/withdrawals", id.ULID(), body, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeWithdrawal(t, rec)
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending: the bank has not confirmed yet", got.Status)
	}
	if got.AdminFee != 250_000 {
		t.Errorf("admin_fee = %d, want 250000", got.AdminFee)
	}
	if got.TotalDebited != 1_250_000 {
		t.Errorf("total_debited = %d, want 1250000", got.TotalDebited)
	}
	if got.BankName != "Bank Central Asia" {
		t.Errorf("bank_name = %q", got.BankName)
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != -1_250_000 {
		t.Errorf("wallet ledger = %d, want -1250000", b)
	}
	clearing := ledger.AccountID(ledger.OwnerProvider, "BCA", ledger.TypeClearing)
	if b := balanceOf(t, ctx, f.pool, clearing); b != 1_000_000 {
		t.Errorf("clearing = %d, want 1000000: the bank gets the amount, not the fee", b)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.PlatformFeeRevenue()); b != 250_000 {
		t.Errorf("fee revenue = %d, want 250000", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestWithdrawalToAnUnsupportedBankIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"bank_code":"NOTABANK","account_number":"1","account_name":"X",` +
		`"amount":100000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/withdrawals", id.ULID(), body, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestWithdrawalWithoutAnAccountNumberIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"bank_code":"BCA","account_number":"","account_name":"",` +
		`"amount":100000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/withdrawals", id.ULID(), body, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestMovingMoneyWithoutATokenIsUnauthorized(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"to":"whoever","amount":100000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/transfers", id.ULID(), body, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestEveryMovementLeavesAnOutboxEvent(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	f.postTo(t, "/v1/transfers", id.ULID(),
		`{"to":"`+peerID+`","amount":100000,"currency":"IDR"}`, f.userID)
	f.postTo(t, "/v1/withdrawals", id.ULID(),
		`{"bank_code":"BCA","account_number":"4471","account_name":"T","amount":100000,"currency":"IDR"}`,
		f.userID)

	rows, err := f.pool.Query(ctx, `SELECT event_type FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()

	var types []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		types = append(types, v)
	}

	want := []string{"transfer.succeeded", "withdrawal.created"}
	if len(types) != len(want) {
		t.Fatalf("outbox events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, types[i], want[i])
		}
	}
}

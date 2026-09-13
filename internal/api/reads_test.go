package api

import (
	"encoding/json"
	"errors"
	"github.com/umars28/marspay/internal/httpx"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/txn"
)

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+f.userID)

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func decodeRefund(t *testing.T, rec *httptest.ResponseRecorder) txn.Refund {
	t.Helper()
	var v txn.Refund
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode refund: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func TestAFullRefundPutsEverythingBackIncludingTheFee(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","reason":"customer cancelled"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeRefund(t, rec)
	if got.Amount != 3_200_000 {
		t.Errorf("amount = %d, want the full 3200000", got.Amount)
	}
	if got.FeeReturned != 22_400 {
		t.Errorf("fee_returned = %d, want 22400", got.FeeReturned)
	}
	if got.Partial {
		t.Error("a full refund was marked partial")
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 0 {
		t.Errorf("payer ledger = %d, want 0: the payment was fully undone", b)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.MerchantPayable(f.merchantID)); b != 0 {
		t.Errorf("merchant payable = %d, want 0", b)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.PlatformFeeRevenue()); b != 0 {
		t.Errorf("fee revenue = %d, want 0: the platform returned its fee", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestPartialRefundsReturnAProportionalFee(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","amount":1600000,"reason":"one item returned"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeRefund(t, rec)
	if got.FeeReturned != 11_200 {
		t.Errorf("fee_returned = %d, want 11200 (half of 22400)", got.FeeReturned)
	}
	if !got.Partial {
		t.Error("a half refund was not marked partial")
	}

	if b := balanceOf(t, ctx, f.pool, ledger.PlatformFeeRevenue()); b != 11_200 {
		t.Errorf("fee revenue = %d, want 11200 left", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestRefundingMoreThanWasPaidIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","amount":9900000,"reason":"oops"}`, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRefundingTwiceBeyondTheTotalIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	first := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","amount":2000000,"reason":"partial"}`, f.userID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first refund status = %d", first.Code)
	}

	second := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","amount":2000000,"reason":"too much"}`, f.userID)
	if second.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second refund status = %d, want 422 (body %s)", second.Code, second.Body.String())
	}
}

func TestARefundWithoutAReasonIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/refunds", id.ULID(),
		`{"payment_id":"`+paid.ID+`","reason":""}`, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestBalanceReportsTheLedgerAndWhetherTheCacheAgrees(t *testing.T) {
	f, _ := newFixture(t)

	f.post(t, id.ULID(), f.validBody(3_200_000))

	rec := f.get(t, "/v1/balance")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var b txn.Balance
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if b.LedgerBalance != -3_200_000 {
		t.Errorf("ledger_balance = %d, want -3200000", b.LedgerBalance)
	}
	if b.Held != 0 {
		t.Errorf("held = %d, want 0", b.Held)
	}
	if b.Cached == nil {
		t.Error("cached balance was not reported")
	}
}

func TestAPendingWithdrawalShowsUpAsHeld(t *testing.T) {
	f, _ := newFixture(t)

	f.postTo(t, "/v1/withdrawals", id.ULID(),
		`{"bank_code":"BCA","account_number":"4471","account_name":"T","amount":1000000,"currency":"IDR"}`,
		f.userID)

	var b txn.Balance
	if err := json.Unmarshal(f.get(t, "/v1/balance").Body.Bytes(), &b); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if b.Held != 1_250_000 {
		t.Errorf("held = %d, want 1250000 (amount plus fee)", b.Held)
	}
	if b.Available != b.LedgerBalance-b.Held {
		t.Errorf("available %d != ledger %d minus held %d", b.Available, b.LedgerBalance, b.Held)
	}
}

func TestHistoryMergesEveryKindOfMovement(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	f.post(t, id.ULID(), f.validBody(1_000_000))
	f.postTo(t, "/v1/transfers", id.ULID(),
		`{"to":"`+peerID+`","amount":500000,"currency":"IDR"}`, f.userID)
	f.postTo(t, "/v1/withdrawals", id.ULID(),
		`{"bank_code":"BCA","account_number":"4471","account_name":"T","amount":100000,"currency":"IDR"}`,
		f.userID)
	f.postTo(t, "/v1/bill-payments", id.ULID(),
		`{"biller_code":"PLN_POSTPAID","customer_ref":"512201884471","amount":100000,"currency":"IDR"}`,
		f.userID)
	f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID)

	rec := f.get(t, "/v1/transactions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var page txn.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	kinds := map[string]bool{}
	for _, a := range page.Data {
		kinds[a.Kind] = true
		if a.Currency != "IDR" {
			t.Errorf("activity %s has currency %q", a.ID, a.Currency)
		}
	}

	for _, want := range []string{"payment", "transfer", "topup", "withdrawal", "bill_payment"} {
		if !kinds[want] {
			t.Errorf("history is missing %s entries: got %v", want, kinds)
		}
	}
}

func TestHistoryIsNewestFirst(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	for i := 0; i < 4; i++ {
		f.postTo(t, "/v1/transfers", id.ULID(),
			`{"to":"`+peerID+`","amount":100000,"currency":"IDR"}`, f.userID)
	}

	var page txn.Page
	if err := json.Unmarshal(f.get(t, "/v1/transactions").Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Data) < 4 {
		t.Fatalf("entries = %d, want at least 4", len(page.Data))
	}

	for i := 1; i < len(page.Data); i++ {
		if page.Data[i].CreatedAt.After(page.Data[i-1].CreatedAt) {
			t.Fatalf("entry %d is newer than the one before it", i)
		}
	}
}

func TestHistoryPaginates(t *testing.T) {
	f, ctx := newFixture(t)
	peerID := f.seedPeer(t, ctx)

	for i := 0; i < 5; i++ {
		f.postTo(t, "/v1/transfers", id.ULID(),
			`{"to":"`+peerID+`","amount":100000,"currency":"IDR"}`, f.userID)
	}

	var page txn.Page
	if err := json.Unmarshal(f.get(t, "/v1/transactions?limit=2").Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(page.Data) != 2 {
		t.Errorf("entries = %d, want 2", len(page.Data))
	}
	if !page.HasMore {
		t.Error("has_more = false with more entries left")
	}
	if page.NextCursor == "" {
		t.Error("next_cursor is empty")
	}
}

func TestProfileReportsTierAndLimits(t *testing.T) {
	f, _ := newFixture(t)

	f.post(t, id.ULID(), f.validBody(3_200_000))

	rec := f.get(t, "/v1/me")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var p txn.Profile
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if p.ID != f.userID {
		t.Errorf("id = %q, want %q", p.ID, f.userID)
	}
	if p.KYCTier != "verified" {
		t.Errorf("kyc_tier = %q, want verified", p.KYCTier)
	}
	if p.Limits.UsedMonthly != 3_200_000 {
		t.Errorf("used_this_month = %d, want 3200000", p.Limits.UsedMonthly)
	}
	if p.Limits.Currency != "IDR" {
		t.Errorf("currency = %q", p.Limits.Currency)
	}
}

func TestReadingWithoutATokenIsUnauthorized(t *testing.T) {
	f, _ := newFixture(t)

	for _, path := range []string{"/v1/balance", "/v1/transactions", "/v1/me"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, rec.Code)
		}
	}
}

func TestARefundIsScopedToTheMerchantThatTookThePayment(t *testing.T) {
	f, ctx := newFixture(t)
	_ = ctx

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	svc := txn.NewService(f.pool, f.ledger, f.wallet)
	_, err := svc.CreateRefund(t.Context(), "merch_somebody_else", txn.RefundRequest{
		PaymentID: paid.ID,
		Reason:    "trying to refund a payment that is not mine",
	})
	if err == nil {
		t.Fatal("a merchant refunded another merchant's payment")
	}

	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("got %v, want a 404 that does not confirm the payment exists", err)
	}

	if _, err := svc.CreateRefund(t.Context(), "", txn.RefundRequest{
		PaymentID: paid.ID,
		Reason:    "an operator refunds on behalf of support",
	}); err != nil {
		t.Errorf("an operator could not refund: %v", err)
	}
}

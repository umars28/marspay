package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/txn"
)

func decodeTopup(t *testing.T, rec *httptest.ResponseRecorder) txn.Topup {
	t.Helper()
	var v txn.Topup
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode topup: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func decodeBill(t *testing.T, rec *httptest.ResponseRecorder) txn.BillPayment {
	t.Helper()
	var v txn.BillPayment
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode bill: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func (f *fixture) callback(t *testing.T, provider, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.postTo(t, "/v1/callbacks/"+provider, "", body, f.userID)
}

func TestATopupIsPendingUntilTheProviderConfirms(t *testing.T) {
	f, ctx := newFixture(t)

	body := `{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/topups", id.ULID(), body, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeTopup(t, rec)
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.VirtualAccount == "" {
		t.Error("no virtual account was issued")
	}
	if got.LedgerTransactionID != "" {
		t.Error("a pending topup already posted to the ledger")
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 0 {
		t.Errorf("wallet ledger = %d, want 0: nothing arrived yet", b)
	}
}

func TestTheCallbackIsWhatActuallyCreditsTheWallet(t *testing.T) {
	f, ctx := newFixture(t)

	created := decodeTopup(t, f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID))

	rec := f.callback(t, "BCA", `{"external_ref":"`+id.ULID()+
		`","topup_id":"`+created.ID+`","amount":50000000,"outcome":"success"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	confirmed := decodeTopup(t, rec)
	if confirmed.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", confirmed.Status)
	}
	if confirmed.LedgerTransactionID == "" {
		t.Error("no ledger transaction was recorded")
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 50_000_000 {
		t.Errorf("wallet ledger = %d, want 50000000", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestADuplicateCallbackCreditsTheWalletOnlyOnce(t *testing.T) {
	f, ctx := newFixture(t)

	created := decodeTopup(t, f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID))

	ref := id.ULID()
	payload := `{"external_ref":"` + ref + `","topup_id":"` + created.ID +
		`","amount":50000000,"outcome":"success"}`

	for i := 0; i < 4; i++ {
		if rec := f.callback(t, "BCA", payload); rec.Code != http.StatusOK {
			t.Fatalf("callback %d status = %d (body %s)", i, rec.Code, rec.Body.String())
		}
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 50_000_000 {
		t.Errorf("wallet ledger = %d after four identical callbacks, want 50000000", b)
	}

	var transactions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_transactions WHERE kind = 'topup'`).Scan(&transactions); err != nil {
		t.Fatalf("count: %v", err)
	}
	if transactions != 1 {
		t.Errorf("topup ledger transactions = %d, want 1", transactions)
	}
}

func TestACallbackForAnAmountWeNeverRecordedIsRefused(t *testing.T) {
	f, _ := newFixture(t)

	created := decodeTopup(t, f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID))

	rec := f.callback(t, "BCA", `{"external_ref":"`+id.ULID()+
		`","topup_id":"`+created.ID+`","amount":99000000,"outcome":"success"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAFailedCallbackMarksTheTopupFailedWithoutTouchingTheLedger(t *testing.T) {
	f, ctx := newFixture(t)

	created := decodeTopup(t, f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID))

	rec := f.callback(t, "BCA", `{"external_ref":"`+id.ULID()+
		`","topup_id":"`+created.ID+`","amount":50000000,"outcome":"expired"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	if got := decodeTopup(t, rec); got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 0 {
		t.Errorf("wallet ledger = %d, want 0", b)
	}
}

func TestRetailTopupChargesAnAdminFee(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"retail","provider_code":"ALFAMART","amount":50000000,"currency":"IDR"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeTopup(t, rec)
	if got.AdminFee != 250_000 {
		t.Errorf("admin_fee = %d, want 250000", got.AdminFee)
	}
	if got.TotalPayable != 50_250_000 {
		t.Errorf("total_payable = %d, want 50250000", got.TotalPayable)
	}
}

func TestATopupBelowTheMinimumIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":100000,"currency":"IDR"}`, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestBillerInquiryReturnsAnAmountDue(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/billers/PLN_POSTPAID/inquire", "",
		`{"customer_ref":"512201884471"}`, f.userID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var inq txn.Inquiry
	if err := json.Unmarshal(rec.Body.Bytes(), &inq); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if inq.Amount <= 0 {
		t.Errorf("amount = %d, want a positive bill", inq.Amount)
	}
	if inq.TotalPayable != inq.Amount+inq.AdminFee {
		t.Errorf("total %d != amount %d + fee %d", inq.TotalPayable, inq.Amount, inq.AdminFee)
	}
}

func TestInquiryIsStableForTheSameCustomer(t *testing.T) {
	f, _ := newFixture(t)

	first := f.postTo(t, "/v1/billers/PLN_POSTPAID/inquire", "", `{"customer_ref":"512201884471"}`, f.userID)
	second := f.postTo(t, "/v1/billers/PLN_POSTPAID/inquire", "", `{"customer_ref":"512201884471"}`, f.userID)

	if first.Body.String() != second.Body.String() {
		t.Error("two inquiries for the same customer returned different bills")
	}
}

func TestPayingABillDebitsTheWalletAndCreditsTheBiller(t *testing.T) {
	f, ctx := newFixture(t)

	body := `{"biller_code":"PLN_POSTPAID","customer_ref":"512201884471",` +
		`"amount":1000000,"currency":"IDR"}`
	rec := f.postTo(t, "/v1/bill-payments", id.ULID(), body, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	got := decodeBill(t, rec)
	if got.Status != "succeeded" {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.TotalDebited != 1_200_000 {
		t.Errorf("total_debited = %d, want 1200000 (bill plus admin fee)", got.TotalDebited)
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != -1_200_000 {
		t.Errorf("wallet ledger = %d, want -1200000", b)
	}
	clearing := ledger.AccountID(ledger.OwnerProvider, "PLN_POSTPAID", ledger.TypeClearing)
	if b := balanceOf(t, ctx, f.pool, clearing); b != 1_000_000 {
		t.Errorf("biller clearing = %d, want 1000000", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestPayingAnUnknownBillerIsNotFound(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/bill-payments", id.ULID(),
		`{"biller_code":"NOPE","customer_ref":"1","amount":100000,"currency":"IDR"}`, f.userID)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestListingBillersNeedsNoToken(t *testing.T) {
	f, _ := newFixture(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/billers", nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Data []txn.Biller `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) == 0 {
		t.Error("the biller catalogue is empty")
	}
}

func TestEveryProviderCallbackIsRecordedBeforeItIsProcessed(t *testing.T) {
	f, ctx := newFixture(t)

	created := decodeTopup(t, f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID))

	f.callback(t, "BCA", `{"external_ref":"`+id.ULID()+
		`","topup_id":"`+created.ID+`","amount":50000000,"outcome":"success"}`)

	var recorded int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM provider_callbacks WHERE provider_code = 'BCA'`).Scan(&recorded); err != nil {
		t.Fatalf("count callbacks: %v", err)
	}
	if recorded != 1 {
		t.Errorf("provider_callbacks rows = %d, want 1", recorded)
	}
}

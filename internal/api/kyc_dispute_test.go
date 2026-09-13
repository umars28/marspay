package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
)

func decodeSubmission(t *testing.T, rec *httptest.ResponseRecorder) compliance.Submission {
	t.Helper()
	var v compliance.Submission
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode submission: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func decodeDispute(t *testing.T, rec *httptest.ResponseRecorder) compliance.Dispute {
	t.Helper()
	var v compliance.Dispute
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode dispute: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func (f *fixture) downgradeTier(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE users SET kyc_tier = 'unverified' WHERE id = $1`, f.userID); err != nil {
		t.Fatalf("downgrade tier: %v", err)
	}
}

func TestSubmittingKYCLandsInTheReviewQueue(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	rec := f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	submitted := decodeSubmission(t, rec)
	if submitted.Status != "pending" {
		t.Errorf("status = %q, want pending", submitted.Status)
	}
	if submitted.SLADueAt.Before(submitted.SubmittedAt) {
		t.Error("the SLA deadline is not after the submission time")
	}

	queue := f.asOperator(t, http.MethodGet, "/internal/v1/kyc", "")
	var body struct {
		Data []compliance.Submission `json:"data"`
	}
	if err := json.Unmarshal(queue.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != submitted.ID {
		t.Fatalf("queue = %v, want the one submission", body.Data)
	}
}

func TestTheIDNumberIsNeverStoredInTheClear(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	const idNumber = "3201012509900001"
	f.postTo(t, "/v1/me/kyc", "", `{"id_number":"`+idNumber+`"}`, f.userID)

	var stored string
	if err := f.pool.QueryRow(ctx,
		`SELECT id_number_hash FROM kyc_submissions WHERE user_id = $1`,
		f.userID).Scan(&stored); err != nil {
		t.Fatalf("read submission: %v", err)
	}

	if stored == idNumber {
		t.Fatal("the national ID number was stored verbatim")
	}
	if len(stored) != 64 {
		t.Errorf("stored value is %d characters, want a 64 character hash", len(stored))
	}
}

func TestApprovingKYCUpgradesTheTier(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	submitted := decodeSubmission(t,
		f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID))

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/kyc/"+submitted.ID+"/review",
		`{"decision":"approved","reason":"ID card and selfie match"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var tier string
	if err := f.pool.QueryRow(ctx,
		`SELECT kyc_tier FROM users WHERE id = $1`, f.userID).Scan(&tier); err != nil {
		t.Fatalf("read tier: %v", err)
	}
	if tier != "verified" {
		t.Errorf("kyc_tier = %q, want verified", tier)
	}
}

func TestRejectingKYCLeavesTheTierAlone(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	submitted := decodeSubmission(t,
		f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID))

	f.asOperator(t, http.MethodPost, "/internal/v1/kyc/"+submitted.ID+"/review",
		`{"decision":"rejected","reason":"the document photo is unreadable"}`)

	var tier string
	if err := f.pool.QueryRow(ctx,
		`SELECT kyc_tier FROM users WHERE id = $1`, f.userID).Scan(&tier); err != nil {
		t.Fatalf("read tier: %v", err)
	}
	if tier != "unverified" {
		t.Errorf("kyc_tier = %q, want unverified", tier)
	}
}

func TestReviewingKYCWithoutAReasonIsRejected(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	submitted := decodeSubmission(t,
		f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID))

	rec := f.asOperator(t, http.MethodPost, "/internal/v1/kyc/"+submitted.ID+"/review",
		`{"decision":"approved","reason":""}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestASecondSubmissionWhileOneIsPendingIsRefused(t *testing.T) {
	f, ctx := newFixture(t)
	f.downgradeTier(t, ctx)

	f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID)
	rec := f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAnAlreadyVerifiedAccountCannotResubmit(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/me/kyc", "", `{"id_number":"3201012509900001"}`, f.userID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestOpeningADisputeAgainstYourOwnPayment(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"goods never shipped"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	opened := decodeDispute(t, rec)
	if opened.Status != "open" {
		t.Errorf("status = %q, want open", opened.Status)
	}
	if opened.Amount != 3_200_000 {
		t.Errorf("amount = %d, want 3200000", opened.Amount)
	}
	if !opened.SLADueAt.After(opened.CreatedAt) {
		t.Error("the SLA deadline is not after the creation time")
	}
}

func TestYouCannotDisputeSomebodyElsesPayment(t *testing.T) {
	f, ctx := newFixture(t)
	peer := f.seedPeer(t, ctx)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	rec := f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"not mine"}`, peer)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestOpeningTwoDisputesOnOnePaymentIsRefused(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	f.postTo(t, "/v1/disputes", "", `{"payment_id":"`+paid.ID+`","reason":"first"}`, f.userID)
	rec := f.postTo(t, "/v1/disputes", "", `{"payment_id":"`+paid.ID+`","reason":"second"}`, f.userID)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestResolvingForTheUserRefundsThemAndThePlatformAbsorbsTheLoss(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))
	opened := decodeDispute(t, f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"goods never shipped"}`, f.userID))

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/disputes/"+opened.ID+"/resolve",
		`{"outcome":"resolved_user","reason":"merchant provided no shipping evidence"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	resolved := decodeDispute(t, rec)
	if resolved.Status != "resolved_user" {
		t.Errorf("status = %q, want resolved_user", resolved.Status)
	}
	if resolved.CoveredMinor != 0 {
		t.Errorf("covered = %d, want 0: there is no holdback for this merchant", resolved.CoveredMinor)
	}
	if resolved.LossMinor != 3_200_000 {
		t.Errorf("platform_loss = %d, want the whole 3200000", resolved.LossMinor)
	}

	if b := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID)); b != 0 {
		t.Errorf("user ledger = %d, want 0: the payment was made whole", b)
	}
	if b := balanceOf(t, ctx, f.pool, ledger.PlatformFloat()); b != -3_200_000 {
		t.Errorf("platform float = %d, want -3200000: the loss lands here", b)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestAHoldbackCoversTheDisputeBeforeThePlatformDoes(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))

	if err := f.ledger.EnsureAccount(ctx, ledger.MerchantHoldback(f.merchantID),
		ledger.OwnerMerchant, f.merchantID, ledger.TypeMerchantHoldback); err != nil {
		t.Fatalf("seed holdback account: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO payouts (id, merchant_id, mode, gross_minor, holdback_minor, net_minor, status)
		 VALUES ('po_test', $1, 'instant', 3200000, 2000000, 1200000, 'settled')`,
		f.merchantID); err != nil {
		t.Fatalf("seed payout: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO holdbacks (id, merchant_id, payout_id, amount_minor, release_at)
		 VALUES ('hb_test', $1, 'po_test', 2000000, now() + interval '1 day')`,
		f.merchantID); err != nil {
		t.Fatalf("seed holdback: %v", err)
	}

	opened := decodeDispute(t, f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"goods not as described"}`, f.userID))

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/disputes/"+opened.ID+"/resolve",
		`{"outcome":"resolved_user","reason":"photos show a different product"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	resolved := decodeDispute(t, rec)
	if resolved.CoveredMinor != 2_000_000 {
		t.Errorf("covered = %d, want 2000000 from the holdback", resolved.CoveredMinor)
	}
	if resolved.LossMinor != 1_200_000 {
		t.Errorf("platform_loss = %d, want 1200000", resolved.LossMinor)
	}

	var consumedBy *string
	if err := f.pool.QueryRow(ctx,
		`SELECT consumed_by FROM holdbacks WHERE id = 'hb_test'`).Scan(&consumedBy); err != nil {
		t.Fatalf("read holdback: %v", err)
	}
	if consumedBy == nil || *consumedBy != opened.ID {
		t.Errorf("holdback consumed_by = %v, want the dispute id", consumedBy)
	}

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestResolvingForTheMerchantMovesNoMoney(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))
	opened := decodeDispute(t, f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"not received"}`, f.userID))

	before := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID))

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/disputes/"+opened.ID+"/resolve",
		`{"outcome":"resolved_merchant","reason":"delivery signature provided"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	after := balanceOf(t, ctx, f.pool, ledger.UserWallet(f.userID))
	if before != after {
		t.Errorf("user ledger moved from %d to %d on a merchant win", before, after)
	}
}

func TestResolvingADisputeTwiceIsRefused(t *testing.T) {
	f, _ := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))
	opened := decodeDispute(t, f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"not received"}`, f.userID))

	path := "/internal/v1/disputes/" + opened.ID + "/resolve"
	body := `{"outcome":"resolved_merchant","reason":"evidence provided"}`

	f.asOperator(t, http.MethodPost, path, body)
	rec := f.asOperator(t, http.MethodPost, path, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestEveryKYCAndDisputeDecisionIsAudited(t *testing.T) {
	f, ctx := newFixture(t)

	paid := decodePayment(t, f.post(t, id.ULID(), f.validBody(3_200_000)))
	opened := decodeDispute(t, f.postTo(t, "/v1/disputes", "",
		`{"payment_id":"`+paid.ID+`","reason":"not received"}`, f.userID))

	f.asOperator(t, http.MethodPost, "/internal/v1/disputes/"+opened.ID+"/resolve",
		`{"outcome":"resolved_merchant","reason":"delivery signature provided"}`)

	var actor, reason string
	err := f.pool.QueryRow(ctx,
		`SELECT actor, reason FROM audit_log
		 WHERE object_type = 'dispute' AND object_id = $1`, opened.ID).Scan(&actor, &reason)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actor != operator {
		t.Errorf("actor = %q, want %q", actor, operator)
	}
	if reason == "" {
		t.Error("the dispute resolution was audited without a reason")
	}
}

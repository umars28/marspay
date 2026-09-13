package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/id"
)

const operator = "umar@marspay"

func (f *fixture) asOperator(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	if method == http.MethodGet {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+operator)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	return f.postTo(t, path, "", body, operator)
}

func (f *fixture) blockUser(t *testing.T, reason string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"subject_type":"user","subject_id":"` + f.userID + `","reason":"` + reason + `"}`
	return f.asOperator(t, http.MethodPost, "/internal/v1/blocks", body)
}

func TestABlockedUserCannotMoveMoney(t *testing.T) {
	f, _ := newFixture(t)

	if rec := f.post(t, id.ULID(), f.validBody(100_000)); rec.Code != http.StatusCreated {
		t.Fatalf("the first payment should work: %d (%s)", rec.Code, rec.Body.String())
	}

	if rec := f.blockUser(t, "structuring pattern"); rec.Code != http.StatusCreated {
		t.Fatalf("block status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	rec := f.post(t, id.ULID(), f.validBody(100_000))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "account_blocked" {
		t.Errorf("error type = %q, want account_blocked", got)
	}
}

func TestABlockStopsEveryKindOfMovement(t *testing.T) {
	f, ctx := newFixture(t)
	peer := f.seedPeer(t, ctx)

	f.blockUser(t, "law enforcement request")

	cases := []struct{ path, body string }{
		{"/v1/transfers", `{"to":"` + peer + `","amount":100000,"currency":"IDR"}`},
		{"/v1/withdrawals", `{"bank_code":"BCA","account_number":"1","account_name":"T","amount":100000,"currency":"IDR"}`},
		{"/v1/topups", `{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`},
		{"/v1/bill-payments", `{"biller_code":"PLN_POSTPAID","customer_ref":"1","amount":100000,"currency":"IDR"}`},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := f.postTo(t, c.path, id.ULID(), c.body, f.userID)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBlockingWithoutAReasonIsRejected(t *testing.T) {
	f, _ := newFixture(t)

	body := `{"subject_type":"user","subject_id":"` + f.userID + `","reason":""}`
	rec := f.asOperator(t, http.MethodPost, "/internal/v1/blocks", body)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUnblockingRestoresTheAccount(t *testing.T) {
	f, _ := newFixture(t)

	f.blockUser(t, "suspected fraud")

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/blocks/user/"+f.userID+"/unblock",
		`{"reason":"appeal accepted, documents verified"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unblock status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	if rec := f.post(t, id.ULID(), f.validBody(100_000)); rec.Code != http.StatusCreated {
		t.Fatalf("status after unblocking = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestUnblockingWithoutAReasonIsRejected(t *testing.T) {
	f, _ := newFixture(t)
	f.blockUser(t, "suspected fraud")

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/blocks/user/"+f.userID+"/unblock", `{"reason":""}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestABlockSurvivesAFlushedCache(t *testing.T) {
	f, _ := newFixture(t)

	f.blockUser(t, "structuring pattern")
	f.flags.Flush()

	rec := f.post(t, id.ULID(), f.validBody(100_000))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: the block must be rebuilt from Postgres", rec.Code)
	}
}

func TestEveryBlockAndUnblockLandsInTheAuditLog(t *testing.T) {
	f, _ := newFixture(t)

	f.blockUser(t, "structuring pattern")
	f.asOperator(t, http.MethodPost,
		"/internal/v1/blocks/user/"+f.userID+"/unblock", `{"reason":"appeal accepted"}`)

	rec := f.asOperator(t, http.MethodGet, "/internal/v1/audit/user/"+f.userID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var body struct {
		Data []compliance.Entry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("audit entries = %d, want 2", len(body.Data))
	}

	actions := map[string]string{}
	for _, e := range body.Data {
		actions[e.Action] = e.Reason
		if e.Actor != operator {
			t.Errorf("actor = %q, want %q", e.Actor, operator)
		}
		if e.Reason == "" {
			t.Errorf("audit entry %s has no reason", e.Action)
		}
	}

	for _, want := range []string{"block_account", "unblock_account"} {
		if _, ok := actions[want]; !ok {
			t.Errorf("audit log is missing %s: got %v", want, actions)
		}
	}
}

func TestTheBlockListShowsWhatIsCurrentlyHeld(t *testing.T) {
	f, _ := newFixture(t)

	f.post(t, id.ULID(), f.validBody(100_000))
	f.blockUser(t, "structuring pattern")

	rec := f.asOperator(t, http.MethodGet, "/internal/v1/blocks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body struct {
		Data []compliance.Block `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("blocks = %d, want 1", len(body.Data))
	}
	if body.Data[0].SubjectID != f.userID {
		t.Errorf("subject = %q, want %q", body.Data[0].SubjectID, f.userID)
	}
	if body.Data[0].Reason == "" {
		t.Error("the block has no recorded reason")
	}
}

func TestUnblockingSomethingThatIsNotBlockedIsNotFound(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.asOperator(t, http.MethodPost,
		"/internal/v1/blocks/user/"+f.userID+"/unblock", `{"reason":"nothing to lift"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestOperatorEndpointsNeedAToken(t *testing.T) {
	f, _ := newFixture(t)

	for _, path := range []string{"/internal/v1/blocks", "/internal/v1/audit"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, rec.Code)
		}
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/umars28/marspay/internal/merchant"
)

func (f *fixture) asMerchant(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()

	var req *http.Request
	if method == http.MethodGet {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) seedKey(t *testing.T, ctx context.Context, scopes string) merchant.Key {
	t.Helper()

	keys := merchant.NewKeys(f.pool, nil)
	created, err := keys.Create(ctx, f.merchantID, merchant.CreateKeyRequest{
		Name: "test key", Mode: "live", Scopes: strings.Split(scopes, ","),
	}, "seed")
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return *created
}

func decodeKey(t *testing.T, rec *httptest.ResponseRecorder) merchant.Key {
	t.Helper()
	var v merchant.Key
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode key: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func TestAKeySecretIsReturnedOnceAndNeverAgain(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read,write")

	rec := f.asMerchant(t, http.MethodPost, "/v1/api-keys",
		`{"name":"production backend","mode":"live","scopes":["read","write"]}`, seed.Secret)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	created := decodeKey(t, rec)
	if created.Secret == "" {
		t.Fatal("the new key came back without its secret")
	}
	if !strings.HasPrefix(created.Secret, "mp_live_") {
		t.Errorf("secret = %q, want an mp_live_ prefix", created.Secret)
	}

	list := f.asMerchant(t, http.MethodGet, "/v1/api-keys", "", seed.Secret)
	if strings.Contains(list.Body.String(), created.Secret) {
		t.Fatal("the key listing leaked a full secret")
	}
	if !strings.Contains(list.Body.String(), created.Prefix) {
		t.Error("the listing does not show the prefix, so keys cannot be told apart")
	}
}

func TestOnlyTheHashIsStored(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read,write")

	var stored string
	if err := f.pool.QueryRow(ctx,
		`SELECT key_hash FROM api_keys WHERE prefix = $1`, seed.Prefix).Scan(&stored); err != nil {
		t.Fatalf("read key: %v", err)
	}

	if stored == seed.Secret {
		t.Fatal("the API key was stored in the clear")
	}
	if len(stored) != 64 {
		t.Errorf("stored value is %d characters, want a 64 character hash", len(stored))
	}
}

func TestAValidKeyAuthenticatesAsItsMerchant(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read")

	rec := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", seed.Secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAnInvalidKeyIsRefused(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read")

	tampered := seed.Secret[:len(seed.Secret)-1] + "Z"

	rec := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", tampered)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAKeyWithTheRightPrefixButWrongSecretIsRefused(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read")

	forged := seed.Prefix + strings.Repeat("A", len(seed.Secret)-len(seed.Prefix))

	rec := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", forged)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: knowing the prefix must not be enough", rec.Code)
	}
}

func TestAReadOnlyKeyCannotWrite(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read")

	rec := f.asMerchant(t, http.MethodPost, "/v1/outlets",
		`{"name":"Kemang"}`, seed.Secret)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestARevokedKeyStopsWorking(t *testing.T) {
	f, ctx := newFixture(t)
	admin := f.seedKey(t, ctx, "read,write")
	victim := f.seedKey(t, ctx, "read,write")

	rec := f.asMerchant(t, http.MethodPost, "/v1/api-keys/"+victim.ID+"/revoke",
		`{"reason":"key leaked in a public repository"}`, admin.Secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	after := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", victim.Secret)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("status after revoking = %d, want 401", after.Code)
	}

	still := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", admin.Secret)
	if still.Code != http.StatusOK {
		t.Errorf("the revoking key stopped working too: %d", still.Code)
	}
}

func TestRevokingAKeyWithoutAReasonIsRejected(t *testing.T) {
	f, ctx := newFixture(t)
	admin := f.seedKey(t, ctx, "read,write")
	victim := f.seedKey(t, ctx, "read,write")

	rec := f.asMerchant(t, http.MethodPost, "/v1/api-keys/"+victim.ID+"/revoke",
		`{"reason":""}`, admin.Secret)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestKeyCreationAndRevocationAreAudited(t *testing.T) {
	f, ctx := newFixture(t)
	admin := f.seedKey(t, ctx, "read,write")

	created := decodeKey(t, f.asMerchant(t, http.MethodPost, "/v1/api-keys",
		`{"name":"staging","mode":"test","scopes":["read"]}`, admin.Secret))
	f.asMerchant(t, http.MethodPost, "/v1/api-keys/"+created.ID+"/revoke",
		`{"reason":"no longer needed"}`, admin.Secret)

	var actions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log
		 WHERE object_id = $1 AND action IN ('create_api_key', 'revoke_api_key')`,
		f.merchantID).Scan(&actions); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if actions != 2 {
		t.Errorf("audit entries = %d, want 2", actions)
	}
}

func TestOutletsAndStaffBelongToTheirMerchant(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read,write")

	outletRec := f.asMerchant(t, http.MethodPost, "/v1/outlets",
		`{"name":"Senopati","address":"Jl. Senopati 1"}`, seed.Secret)
	if outletRec.Code != http.StatusCreated {
		t.Fatalf("outlet status = %d, want 201 (body %s)", outletRec.Code, outletRec.Body.String())
	}

	var outlet merchant.Outlet
	if err := json.Unmarshal(outletRec.Body.Bytes(), &outlet); err != nil {
		t.Fatalf("decode outlet: %v", err)
	}
	if outlet.NMID == "" {
		t.Error("the outlet has no NMID, so it cannot accept QRIS")
	}

	staffRec := f.asMerchant(t, http.MethodPost, "/v1/staff",
		`{"outlet_id":"`+outlet.ID+`","full_name":"Yuda Pratama","email":"yuda@tuku.id","role":"cashier"}`,
		seed.Secret)
	if staffRec.Code != http.StatusCreated {
		t.Fatalf("staff status = %d, want 201 (body %s)", staffRec.Code, staffRec.Body.String())
	}

	listRec := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", seed.Secret)
	var body struct {
		Data []merchant.Outlet `json:"data"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].StaffCount != 1 {
		t.Fatalf("outlets = %+v, want one outlet with one staff member", body.Data)
	}
}

func TestStaffCannotBeAttachedToAnotherMerchantsOutlet(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read,write")

	rec := f.asMerchant(t, http.MethodPost, "/v1/staff",
		`{"outlet_id":"out_does_not_exist","full_name":"X","email":"x@y.id","role":"cashier"}`,
		seed.Secret)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownStaffRoleIsRejected(t *testing.T) {
	f, ctx := newFixture(t)
	seed := f.seedKey(t, ctx, "read,write")

	rec := f.asMerchant(t, http.MethodPost, "/v1/staff",
		`{"full_name":"X","email":"x@y.id","role":"superuser"}`, seed.Secret)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestMerchantEndpointsNeedAKey(t *testing.T) {
	f, _ := newFixture(t)

	for _, path := range []string{"/v1/api-keys", "/v1/outlets", "/v1/staff"} {
		rec := f.asMerchant(t, http.MethodGet, path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, rec.Code)
		}
	}
}

func TestAUserTokenIsNotAMerchantKey(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.asMerchant(t, http.MethodGet, "/v1/outlets", "", f.userID)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: a consumer token must not reach merchant endpoints", rec.Code)
	}
}

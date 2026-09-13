package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/loyalty"
)

func (f *fixture) seedPromo(t *testing.T, ctx context.Context, code, kind string, bps int, quota int) string {
	t.Helper()

	promoID := id.New("promo")
	_, err := f.pool.Exec(ctx,
		`INSERT INTO promos (id, code, name, kind, value_bps, max_benefit_minor,
		                     min_spend_minor, total_quota, per_user_quota,
		                     starts_at, ends_at, status)
		 VALUES ($1, $2, 'Test offer', $3, $4, 1500000, 0, $5, 1,
		         now() - interval '1 hour', now() + interval '7 days', 'active')`,
		promoID, code, kind, bps, nullableQuota(quota))
	if err != nil {
		t.Fatalf("seed promo: %v", err)
	}

	if quota > 0 {
		if err := f.svcLoyalty().SeedQuota(ctx, promoID, int64(quota), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("seed quota: %v", err)
		}
	}
	return promoID
}

func nullableQuota(q int) any {
	if q <= 0 {
		return nil
	}
	return q
}

func (f *fixture) svcLoyalty() *loyalty.Service {
	return loyalty.NewService(f.pool, f.quota)
}

func decodeBenefit(t *testing.T, rec *httptest.ResponseRecorder) loyalty.Benefit {
	t.Helper()
	var v loyalty.Benefit
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode benefit: %v (body %s)", err, rec.Body.String())
	}
	return v
}

func TestCashbackAwardsPointsWorthTheBenefit(t *testing.T) {
	f, ctx := newFixture(t)
	f.seedPromo(t, ctx, "COFFEE30", "cashback", 3000, 0)

	rec := f.postTo(t, "/v1/promos/apply", "",
		`{"code":"COFFEE30","spend":3200000}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	benefit := decodeBenefit(t, rec)
	if benefit.ValueMinor != 960_000 {
		t.Errorf("value = %d, want 960000 (30 percent of 3200000)", benefit.ValueMinor)
	}
	if benefit.Points != 9_600 {
		t.Errorf("points = %d, want 9600", benefit.Points)
	}

	var body struct {
		Balance loyalty.Balance `json:"balance"`
	}
	if err := json.Unmarshal(f.get(t, "/v1/points").Body.Bytes(), &body); err != nil {
		t.Fatalf("decode points: %v", err)
	}
	if body.Balance.Points != 9_600 {
		t.Errorf("point balance = %d, want 9600", body.Balance.Points)
	}
}

func TestCashbackIsCappedAtTheMaximumBenefit(t *testing.T) {
	f, ctx := newFixture(t)
	f.seedPromo(t, ctx, "COFFEE30", "cashback", 3000, 0)

	benefit := decodeBenefit(t, f.postTo(t, "/v1/promos/apply", "",
		`{"code":"COFFEE30","spend":90000000}`, f.userID))

	if benefit.ValueMinor != 1_500_000 {
		t.Errorf("value = %d, want the 1500000 cap", benefit.ValueMinor)
	}
}

func TestAnOfferCanOnlyBeUsedOncePerUser(t *testing.T) {
	f, ctx := newFixture(t)
	f.seedPromo(t, ctx, "COFFEE30", "cashback", 3000, 0)

	first := f.postTo(t, "/v1/promos/apply", "", `{"code":"COFFEE30","spend":3200000}`, f.userID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want 201", first.Code)
	}

	second := f.postTo(t, "/v1/promos/apply", "", `{"code":"COFFEE30","spend":3200000}`, f.userID)
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409 (body %s)", second.Code, second.Body.String())
	}
}

func TestAnExhaustedQuotaStopsFurtherRedemptions(t *testing.T) {
	f, ctx := newFixture(t)
	f.seedPromo(t, ctx, "LIMITED", "cashback", 1000, 2)

	peers := []string{f.userID, f.seedPeer(t, ctx), f.seedPeer(t, ctx)}
	codes := make([]int, 0, 3)
	for _, u := range peers {
		rec := f.postTo(t, "/v1/promos/apply", "", `{"code":"LIMITED","spend":1000000}`, u)
		codes = append(codes, rec.Code)
	}

	granted := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			granted++
		}
	}
	if granted != 2 {
		t.Errorf("granted %d redemptions, want exactly the quota of 2 (codes %v)", granted, codes)
	}
	if codes[2] != http.StatusConflict {
		t.Errorf("the third redemption got %d, want 409", codes[2])
	}
}

func TestQuotaHoldsUnderConcurrency(t *testing.T) {
	f, ctx := newFixture(t)
	promoID := f.seedPromo(t, ctx, "RUSH", "cashback", 1000, 5)

	users := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		users = append(users, f.seedPeer(t, ctx))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0

	for _, u := range users {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			rec := f.postTo(t, "/v1/promos/apply", "", `{"code":"RUSH","spend":1000000}`, u)
			if rec.Code == http.StatusCreated {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()

	if granted != 5 {
		t.Errorf("granted %d redemptions under concurrency, want exactly 5", granted)
	}

	left, err := f.quota.Remaining(ctx, "promo:"+promoID)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if left != 0 {
		t.Errorf("remaining quota = %d, want 0", left)
	}
}

func TestAnOfferBelowTheMinimumSpendIsRefused(t *testing.T) {
	f, ctx := newFixture(t)

	promoID := id.New("promo")
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO promos (id, code, name, kind, value_minor, min_spend_minor,
		                     per_user_quota, starts_at, ends_at, status)
		 VALUES ($1, 'PLN25K', 'Electricity discount', 'discount', 2500000, 10000000, 1,
		         now() - interval '1 hour', now() + interval '7 days', 'active')`,
		promoID); err != nil {
		t.Fatalf("seed promo: %v", err)
	}

	rec := f.postTo(t, "/v1/promos/apply", "", `{"code":"PLN25K","spend":5000000}`, f.userID)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestPointsAreASeparateLedgerFromMoney(t *testing.T) {
	f, ctx := newFixture(t)
	f.seedPromo(t, ctx, "COFFEE30", "cashback", 3000, 0)

	f.postTo(t, "/v1/promos/apply", "", `{"code":"COFFEE30","spend":3200000}`, f.userID)

	sum, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("money ledger = %d, want 0: points must never touch it", sum)
	}

	var points int64
	if err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM point_entries`).Scan(&points); err != nil {
		t.Fatalf("sum points: %v", err)
	}
	if points != 9_600 {
		t.Errorf("point entries sum = %d, want 9600: points are minted, not conserved", points)
	}
}

func TestRedeemingNeedsEnoughPoints(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/points/redeem", "", `{"kind":"cash","points":50000}`, f.userID)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRedeemingToCashHasAMinimum(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/points/redeem", "", `{"kind":"cash","points":500}`, f.userID)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestRequestingMoneyShowsUpForThePayer(t *testing.T) {
	f, ctx := newFixture(t)
	peer := f.seedPeer(t, ctx)

	rec := f.postTo(t, "/v1/money-requests", "",
		`{"payer_id":"`+peer+`","amount":850000,"currency":"IDR","note":"fuel"}`, f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/money-requests", nil)
	req.Header.Set("Authorization", "Bearer "+peer)
	list := httptest.NewRecorder()
	f.router.ServeHTTP(list, req)

	var body struct {
		Data []loyalty.MoneyRequest `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("incoming requests = %d, want 1", len(body.Data))
	}
	if body.Data[0].Amount != 850_000 {
		t.Errorf("amount = %d, want 850000", body.Data[0].Amount)
	}
}

func TestYouCannotRequestMoneyFromYourself(t *testing.T) {
	f, _ := newFixture(t)

	rec := f.postTo(t, "/v1/money-requests", "",
		`{"payer_id":"`+f.userID+`","amount":100000,"currency":"IDR"}`, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestASplitCreatesOneRequestPerOtherPerson(t *testing.T) {
	f, ctx := newFixture(t)
	a, b, c := f.seedPeer(t, ctx), f.seedPeer(t, ctx), f.seedPeer(t, ctx)

	rec := f.postTo(t, "/v1/bill-splits", "",
		`{"title":"Dinner","total":48000000,"currency":"IDR","payers":["`+a+`","`+b+`","`+c+`"]}`,
		f.userID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	var split loyalty.Split
	if err := json.Unmarshal(rec.Body.Bytes(), &split); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if split.Participants != 4 {
		t.Errorf("participants = %d, want 4 including the owner", split.Participants)
	}
	if split.ShareMinor != 12_000_000 {
		t.Errorf("share = %d, want 12000000", split.ShareMinor)
	}
	if len(split.Requests) != 3 {
		t.Fatalf("requests = %d, want 3: the owner does not request from themselves", len(split.Requests))
	}
	for _, req := range split.Requests {
		if req.SplitID != split.ID {
			t.Errorf("request %s is not attached to the split", req.ID)
		}
	}
}

func TestEachSplitRequestStandsOnItsOwn(t *testing.T) {
	f, ctx := newFixture(t)
	a, b := f.seedPeer(t, ctx), f.seedPeer(t, ctx)

	rec := f.postTo(t, "/v1/bill-splits", "",
		`{"title":"Dinner","total":30000000,"currency":"IDR","payers":["`+a+`","`+b+`"]}`,
		f.userID)
	var split loyalty.Split
	if err := json.Unmarshal(rec.Body.Bytes(), &split); err != nil {
		t.Fatalf("decode: %v", err)
	}

	declined := f.postTo(t, "/v1/money-requests/"+split.Requests[0].ID+"/decline", "", `{}`, a)
	if declined.Code != http.StatusOK {
		t.Fatalf("decline status = %d, want 200 (body %s)", declined.Code, declined.Body.String())
	}

	var stillPending int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM money_requests WHERE split_id = $1 AND status = 'pending'`,
		split.ID).Scan(&stillPending); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stillPending != 1 {
		t.Errorf("pending requests = %d, want 1: one decline must not cancel the others", stillPending)
	}
}

func TestDecliningSomebodyElsesRequestIsRefused(t *testing.T) {
	f, ctx := newFixture(t)
	peer := f.seedPeer(t, ctx)
	other := f.seedPeer(t, ctx)

	created := f.postTo(t, "/v1/money-requests", "",
		`{"payer_id":"`+peer+`","amount":100000,"currency":"IDR"}`, f.userID)

	var req loyalty.MoneyRequest
	if err := json.Unmarshal(created.Body.Bytes(), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec := f.postTo(t, "/v1/money-requests/"+req.ID+"/decline", "", `{}`, other)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestASplitTooSmallToDivideIsRefused(t *testing.T) {
	f, ctx := newFixture(t)
	a := f.seedPeer(t, ctx)

	rec := f.postTo(t, "/v1/bill-splits", "",
		`{"title":"Tiny","total":1,"currency":"IDR","payers":["`+a+`"]}`, f.userID)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

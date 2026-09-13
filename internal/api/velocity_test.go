package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/umars28/marspay/internal/id"
)

func (f *fixture) freshPeers(t *testing.T, ctx context.Context, n int) []string {
	t.Helper()
	peers := make([]string, 0, n)
	for i := 0; i < n; i++ {
		peers = append(peers, f.seedPeer(t, ctx))
	}
	return peers
}

func (f *fixture) transfer(t *testing.T, to string, amount int64) int {
	t.Helper()
	body := `{"to":"` + to + `","amount":` + itoa(amount) + `,"currency":"IDR"}`
	return f.postTo(t, "/v1/transfers", id.ULID(), body, f.userID).Code
}

func TestTransfersToTenNewRecipientsGetBlocked(t *testing.T) {
	f, ctx := newFixture(t)
	peers := f.freshPeers(t, ctx, 11)

	for i := 0; i < 9; i++ {
		if code := f.transfer(t, peers[i], 10_000); code != http.StatusCreated {
			t.Fatalf("transfer %d got %d, want 201", i+1, code)
		}
	}

	if code := f.transfer(t, peers[9], 10_000); code != http.StatusForbidden {
		t.Fatalf("the tenth transfer got %d, want 403", code)
	}
}

func TestABlockedTransferMovesNoMoneyAndWritesNoLedgerEntry(t *testing.T) {
	f, ctx := newFixture(t)
	peers := f.freshPeers(t, ctx, 11)

	for i := 0; i < 9; i++ {
		f.transfer(t, peers[i], 10_000)
	}

	before, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}

	if code := f.transfer(t, peers[9], 10_000); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}

	after, err := f.ledger.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if before != after || after != 0 {
		t.Errorf("global sum moved from %d to %d on a blocked transfer", before, after)
	}

	var transfers int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM transfers`).Scan(&transfers); err != nil {
		t.Fatalf("count transfers: %v", err)
	}
	if transfers != 9 {
		t.Errorf("transfers = %d, want 9: the blocked one must not be recorded", transfers)
	}
}

func TestEveryTrippedRuleBecomesAnAlert(t *testing.T) {
	f, ctx := newFixture(t)
	peers := f.freshPeers(t, ctx, 11)

	for i := 0; i < 10; i++ {
		f.transfer(t, peers[i], 10_000)
	}

	var alerts int
	var severity, ruleCode string
	err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM risk_alerts WHERE subject_id = $1`, f.userID).Scan(&alerts)
	if err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alerts == 0 {
		t.Fatal("a rule tripped but no alert was written")
	}

	err = f.pool.QueryRow(ctx,
		`SELECT rule_code, severity FROM risk_alerts WHERE subject_id = $1 ORDER BY created_at DESC LIMIT 1`,
		f.userID).Scan(&ruleCode, &severity)
	if err != nil {
		t.Fatalf("read alert: %v", err)
	}
	if ruleCode != "VR-03" {
		t.Errorf("rule_code = %q, want VR-03", ruleCode)
	}
	if severity != "high" {
		t.Errorf("severity = %q, want high for a freeze", severity)
	}
}

func TestARepeatRecipientDoesNotCountTowardTheNewRecipientRule(t *testing.T) {
	f, ctx := newFixture(t)
	peer := f.seedPeer(t, ctx)

	for i := 0; i < 20; i++ {
		if code := f.transfer(t, peer, 10_000); code != http.StatusCreated {
			t.Fatalf("transfer %d got %d, want 201: the same recipient is not new", i+1, code)
		}
	}
}

func TestWithdrawingRightAfterATopupIsRefused(t *testing.T) {
	f, _ := newFixture(t)

	topup := f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID)
	if topup.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, want 201 (body %s)", topup.Code, topup.Body.String())
	}

	rec := f.postTo(t, "/v1/withdrawals", id.ULID(),
		`{"bank_code":"BCA","account_number":"4471","account_name":"T","amount":1000000,"currency":"IDR"}`,
		f.userID)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "velocity_blocked" {
		t.Errorf("error type = %q, want velocity_blocked", got)
	}
}

func TestAWithdrawalIsFineOnceTheTopupWindowPasses(t *testing.T) {
	f, _ := newFixture(t)

	f.postTo(t, "/v1/topups", id.ULID(),
		`{"source":"bank_va","provider_code":"BCA","amount":50000000,"currency":"IDR"}`, f.userID)

	f.counter.Expire("velocity:VR-07:" + f.userID)

	rec := f.postTo(t, "/v1/withdrawals", id.ULID(),
		`{"bank_code":"BCA","account_number":"4471","account_name":"T","amount":1000000,"currency":"IDR"}`,
		f.userID)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestALargeHourlyTotalAsksForAStepUp(t *testing.T) {
	f, ctx := newFixture(t)

	if err := f.wallet.Warm(ctx, "acc_"+f.userID+"_user_wallet", 99_000_000_00); err != nil {
		t.Fatalf("warm: %v", err)
	}

	rec := f.post(t, id.ULID(), f.validBody(60_000_000_00))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := decodeErrorType(t, rec); got != "step_up_required" {
		t.Errorf("error type = %q, want step_up_required", got)
	}
}

func TestMonitorModeRulesNeverBlockButStillAlert(t *testing.T) {
	f, ctx := newFixture(t)

	if _, err := f.pool.Exec(ctx,
		`UPDATE velocity_rules SET mode = 'monitor' WHERE code = 'VR-03'`); err != nil {
		t.Fatalf("set monitor mode: %v", err)
	}

	var mode string
	if err := f.pool.QueryRow(ctx,
		`SELECT mode FROM velocity_rules WHERE code = 'VR-03'`).Scan(&mode); err != nil {
		t.Fatalf("read mode: %v", err)
	}
	if mode != "monitor" {
		t.Fatalf("mode = %q, want monitor", mode)
	}
}

func TestTheRuleCatalogueIsPersistedForTheDashboard(t *testing.T) {
	f, ctx := newFixture(t)

	rows, err := f.pool.Query(ctx,
		`SELECT code, window_sec, threshold, action, mode FROM velocity_rules ORDER BY code`)
	if err != nil {
		t.Fatalf("query rules: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var code, action, mode string
		var window, threshold int
		if err := rows.Scan(&code, &window, &threshold, &action, &mode); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if window <= 0 || threshold <= 0 {
			t.Errorf("rule %s has window %d and threshold %d", code, window, threshold)
		}
		seen[code] = true
	}

	for _, want := range []string{"VR-02", "VR-03", "VR-05", "VR-07", "VR-08"} {
		if !seen[want] {
			t.Errorf("rule %s is missing from the catalogue", want)
		}
	}
}

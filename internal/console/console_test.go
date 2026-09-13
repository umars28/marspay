package console

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/testdb"
	"github.com/umars28/marspay/internal/velocity"
)

const (
	merchantID = "merch_console"
	userID     = "usr_console"
	paymentID  = "pay_console"
	txID       = "txn_console"
)

func seed(t *testing.T) (*Store, *pgxpool.Pool, context.Context) {
	t.Helper()

	pool, ctx := testdb.New(t)
	if err := velocity.NewStore(pool).SyncRules(ctx, velocity.DefaultRules()); err != nil {
		t.Fatalf("sync rules: %v", err)
	}

	statements := []string{
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ('` + userID + `', '081299001122', 'Console User', 'x', 'verified', 'active')`,

		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
		 VALUES ('` + merchantID + `', 'PT Console', 'Console Coffee', 'food', 'active', 70)`,

		`INSERT INTO accounts (id, owner_type, owner_id, account_type) VALUES
		   ('acc_console_wallet', 'user', '` + userID + `', 'user_wallet'),
		   ('acc_console_payable', 'merchant', '` + merchantID + `', 'merchant_payable'),
		   ('acc_console_fee', 'platform', NULL, 'platform_fee_revenue')`,

		`INSERT INTO ledger_transactions (id, kind, reference_id)
		 VALUES ('` + txID + `', 'payment', '` + paymentID + `')`,

		`INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor) VALUES
		   ('led_1', '` + txID + `', 'acc_console_wallet', -3200000),
		   ('led_2', '` + txID + `', 'acc_console_payable', 3177600),
		   ('led_3', '` + txID + `', 'acc_console_fee', 22400)`,

		`INSERT INTO payments (id, user_id, merchant_id, method, amount_minor, fee_minor,
		                       status, ledger_transaction_id)
		 VALUES ('` + paymentID + `', '` + userID + `', '` + merchantID + `', 'qris',
		         3200000, 22400, 'succeeded', '` + txID + `')`,

		`INSERT INTO payments (id, user_id, merchant_id, method, amount_minor, fee_minor, status)
		 VALUES ('pay_console_failed', '` + userID + `', '` + merchantID + `', 'qris',
		         500000, 0, 'failed')`,

		`INSERT INTO payouts (id, merchant_id, payment_id, mode, gross_minor, holdback_minor,
		                      net_minor, rail, status, latency_ms, settled_at)
		 VALUES ('po_console', '` + merchantID + `', '` + paymentID + `', 'instant',
		         3177600, 127104, 3050496, 'bifast', 'settled', 2900, now())`,

		`INSERT INTO payouts (id, merchant_id, mode, gross_minor, holdback_minor, net_minor, status)
		 VALUES ('po_console_batch', '` + merchantID + `', 'batch', 100000, 0, 100000,
		         'degraded_to_batch')`,

		`INSERT INTO settlement_batches (id, merchant_id, period_start, period_end,
		                                 payout_count, gross_minor, fee_minor, net_minor, status)
		 VALUES ('set_console', '` + merchantID + `', now() - interval '1 day', now(),
		         1, 100000, 700, 99300, 'settled')`,

		`INSERT INTO webhook_endpoints (id, merchant_id, url, signing_secret, events)
		 VALUES ('whe_console', '` + merchantID + `', 'https://example.test/hook',
		         'whsec_abcdef123456', ARRAY['payment.succeeded'])`,

		`INSERT INTO float_positions (outstanding_minor, limit_minor, utilisation_bps, instant_enabled)
		 VALUES (4000000, 10000000, 4000, true)`,

		`INSERT INTO reconciliation_runs (id, business_date, provider_code, rows_compared,
		                                  rows_matched, discrepancies, delta_minor, status)
		 VALUES ('rec_console', current_date, 'bifast', 10, 9, 1, -50000, 'finished')`,

		`INSERT INTO reconciliation_discrepancies (id, run_id, external_ref, internal_minor,
		                                           provider_minor, delta_minor, suspected_cause)
		 VALUES ('dsc_console', 'rec_console', 'REF-1', NULL, 50000, -50000, 'lost callback')`,

		`INSERT INTO risk_alerts (id, subject_type, subject_id, rule_code, detail,
		                          observed_minor, severity)
		 VALUES ('alr_console', 'user', '` + userID + `', 'VR-08',
		         'Hourly outbound total crossed the threshold', 5100000000, 'high')`,
	}

	for i, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed statement %d: %v", i, err)
		}
	}

	return NewStore(pool), pool, ctx
}

func TestAMerchantSeesItsOwnPaymentsAndTheirTotals(t *testing.T) {
	store, _, ctx := seed(t)

	page, err := store.Payments(ctx, merchantID, "", 10)
	if err != nil {
		t.Fatalf("payments: %v", err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("rows = %d, want 2", len(page.Data))
	}
	if page.Totals.Succeeded != 1 || page.Totals.Failed != 1 {
		t.Errorf("totals = %+v, want one succeeded and one failed", page.Totals)
	}
	if page.Data[0].Net != page.Data[0].Amount-page.Data[0].Fee {
		t.Error("net is not gross minus fee")
	}

	filtered, err := store.Payments(ctx, merchantID, "succeeded", 10)
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(filtered.Data) != 1 {
		t.Errorf("status filter returned %d rows, want 1", len(filtered.Data))
	}
}

func TestAMerchantCannotReadAnotherMerchantsPayment(t *testing.T) {
	store, _, ctx := seed(t)

	if _, err := store.Payment(ctx, "merch_someone_else", paymentID); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound: a payment leaked across merchants", err)
	}
	if _, err := store.Payment(ctx, merchantID, paymentID); err != nil {
		t.Errorf("the owning merchant could not read its own payment: %v", err)
	}
}

func TestPayoutSummaryCountsModesAndLatency(t *testing.T) {
	store, _, ctx := seed(t)

	page, err := store.Payouts(ctx, merchantID, "", 10)
	if err != nil {
		t.Fatalf("payouts: %v", err)
	}
	if page.Summary.Instant != 1 || page.Summary.Batch != 1 {
		t.Errorf("summary = %+v, want one instant and one batch", page.Summary)
	}
	if page.Summary.MedianMs == nil || *page.Summary.MedianMs != 2900 {
		t.Errorf("median latency = %v, want 2900", page.Summary.MedianMs)
	}

	instant, err := store.Payouts(ctx, merchantID, "instant", 10)
	if err != nil {
		t.Fatalf("instant: %v", err)
	}
	if len(instant.Data) != 1 || instant.Data[0].Mode != "instant" {
		t.Errorf("mode filter returned %d rows", len(instant.Data))
	}
}

func TestSettlementsAndWebhookViewsReadBack(t *testing.T) {
	store, _, ctx := seed(t)

	batches, err := store.Settlements(ctx, merchantID, 10)
	if err != nil {
		t.Fatalf("settlements: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}

	endpoints, err := store.Endpoints(ctx, merchantID)
	if err != nil {
		t.Fatalf("endpoints: %v", err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(endpoints))
	}
	if endpoints[0].SecretHint == "whsec_abcdef123456" {
		t.Error("the full signing secret was returned; only a hint may leave the server")
	}
	if len(endpoints[0].Events) != 1 {
		t.Errorf("events = %v", endpoints[0].Events)
	}

	if _, err := store.Deliveries(ctx, merchantID, "", 10); err != nil {
		t.Errorf("deliveries: %v", err)
	}
}

func TestFloatReportsHeadroomAndExposure(t *testing.T) {
	store, _, ctx := seed(t)

	view, err := store.Float(ctx)
	if err != nil {
		t.Fatalf("float: %v", err)
	}
	if view.Position.Headroom != view.Position.Limit-view.Position.Outstanding {
		t.Errorf("headroom = %d, want %d", view.Position.Headroom,
			view.Position.Limit-view.Position.Outstanding)
	}
	if view.Position.UtilisationBps != 4000 {
		t.Errorf("utilisation = %d bps, want 4000", view.Position.UtilisationBps)
	}
	if len(view.Top) == 0 {
		t.Error("no merchant exposure although an instant payout is outstanding")
	}
}

func TestTheEngineViewSeparatesRailsFromTotals(t *testing.T) {
	store, _, ctx := seed(t)

	engine, err := store.Engine(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	if engine.Instant != 1 {
		t.Errorf("instant = %d, want 1", engine.Instant)
	}
	if engine.Batch != 1 {
		t.Errorf("degraded = %d, want 1", engine.Batch)
	}
	if len(engine.Rails) != 1 || engine.Rails[0].Rail != "bifast" {
		t.Errorf("rails = %+v, want one bifast row", engine.Rails)
	}
	if engine.P99Ms == nil {
		t.Error("no p99 although a payout recorded a latency")
	}
}

func TestReconciliationShowsRunsAndWhatIsStillOpen(t *testing.T) {
	store, _, ctx := seed(t)

	view, err := store.Reconciliation(ctx, 10)
	if err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if len(view.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(view.Runs))
	}
	if len(view.Open) != 1 {
		t.Fatalf("open discrepancies = %d, want 1", len(view.Open))
	}
	if view.Open[0].InternalMinor != nil {
		t.Error("a provider-only difference reported an internal amount")
	}
	if view.Open[0].Delta != -50000 {
		t.Errorf("delta = %d, want -50000", view.Open[0].Delta)
	}
}

func TestTheLedgerViewProvesAPaymentBalances(t *testing.T) {
	store, _, ctx := seed(t)

	view, err := store.Ledger(ctx, paymentID)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(view.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(view.Entries))
	}
	if view.Sum != 0 || !view.Balanced {
		t.Errorf("sum = %d balanced = %v, want 0 and true", view.Sum, view.Balanced)
	}
	if view.Entries[0].Amount >= 0 {
		t.Error("entries are not ordered debit first")
	}

	if _, err := store.Ledger(ctx, "pay_console_failed"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a payment with no ledger transaction gave %v, want ErrNotFound", err)
	}
}

func TestSearchFindsAcrossKinds(t *testing.T) {
	store, _, ctx := seed(t)

	hits, err := store.Search(ctx, merchantID)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	kinds := map[string]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
	}
	for _, want := range []string{"payment", "payout", "merchant"} {
		if !kinds[want] {
			t.Errorf("searching for a merchant id found no %s", want)
		}
	}

	byPhone, err := store.Search(ctx, "081299001122")
	if err != nil {
		t.Fatalf("search by phone: %v", err)
	}
	if len(byPhone) == 0 || byPhone[0].Kind != "user" {
		t.Errorf("a phone number did not find its user: %+v", byPhone)
	}

	none, err := store.Search(ctx, "")
	if err != nil || len(none) != 0 {
		t.Errorf("an empty query returned %d hits and %v", len(none), err)
	}
}

func TestRulesCarryTheirRecentTripCount(t *testing.T) {
	store, _, ctx := seed(t)

	rules, err := store.Rules(ctx)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("no rules; the catalogue is empty")
	}

	var vr08 *Rule
	for i := range rules {
		if rules[i].Code == "VR-08" {
			vr08 = &rules[i]
		}
	}
	if vr08 == nil {
		t.Fatal("VR-08 is missing from the catalogue")
	}
	if vr08.Trips != 1 {
		t.Errorf("VR-08 trips in the last 7 days = %d, want 1", vr08.Trips)
	}
}

func TestAlertsNameTheirSubjectAndRule(t *testing.T) {
	store, _, ctx := seed(t)

	page, err := store.Alerts(ctx, "", 10)
	if err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("alerts = %d, want 1", len(page.Data))
	}

	a := page.Data[0]
	if a.SubjectName != "Console User" {
		t.Errorf("subject name = %q, want the user's name", a.SubjectName)
	}
	if a.RuleText == "" {
		t.Error("the alert does not carry what the rule says")
	}
	if page.Open != 1 || page.High != 1 {
		t.Errorf("counters = open %d high %d, want 1 and 1", page.Open, page.High)
	}

	filtered, err := store.Alerts(ctx, "low", 10)
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(filtered.Data) != 0 {
		t.Errorf("severity filter returned %d rows, want 0", len(filtered.Data))
	}
}

func TestAnAccountViewShowsItsBalanceAndEntries(t *testing.T) {
	store, _, ctx := seed(t)

	view, err := store.Account(ctx, "acc_console_payable", 10)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if view.Balance != 3177600 {
		t.Errorf("balance = %d, want 3177600", view.Balance)
	}
	if view.OwnerType != "merchant" || view.Kind != "merchant_payable" {
		t.Errorf("owner = %q kind = %q", view.OwnerType, view.Kind)
	}
	if len(view.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(view.Entries))
	}

	if _, err := store.Account(ctx, "acc_does_not_exist", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestHourlyVolumeAlwaysCoversTwentyFourHours(t *testing.T) {
	store, _, ctx := seed(t)

	hours, err := store.Hourly(ctx, merchantID)
	if err != nil {
		t.Fatalf("hourly: %v", err)
	}
	if len(hours) != 24 {
		t.Fatalf("buckets = %d, want 24: a chart with gaps is a chart that lies", len(hours))
	}

	var total int64
	for i, h := range hours {
		if h.Hour != i {
			t.Fatalf("bucket %d reports hour %d", i, h.Hour)
		}
		total += h.Volume
	}
	if total == 0 {
		t.Error("every bucket is empty although a payment was seeded in the last 24 hours")
	}
}

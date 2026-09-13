package merchant

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/testdb"
)

func seedCharges(t *testing.T) (*Charges, *pgxpool.Pool, context.Context, string) {
	t.Helper()

	pool, ctx := testdb.New(t)
	merchantID := id.New("merch")

	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
		 VALUES ($1, 'PT Link', 'Link Merchant', 'food', 'active', 70)`, merchantID); err != nil {
		t.Fatalf("seed merchant: %v", err)
	}

	return NewCharges(pool), pool, ctx, merchantID
}

func openCharge(t *testing.T, c *Charges, ctx context.Context, merchantID string) *Charge {
	t.Helper()

	charge, err := c.Create(ctx, merchantID, CreateChargeRequest{
		Description: "Table 4 · dinner",
		Amount:      48_000_00,
	})
	if err != nil {
		t.Fatalf("create charge: %v", err)
	}
	return charge
}

func TestALinkCarriesEverythingThePayerNeeds(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)

	charge := openCharge(t, c, ctx, merchantID)
	if charge.Status != "open" {
		t.Errorf("status = %q, want open", charge.Status)
	}
	if charge.Reference == "" {
		t.Error("no reference was generated, so the merchant cannot reconcile it")
	}
	if !charge.ExpiresAt.After(time.Now()) {
		t.Error("the link is already expired on creation")
	}

	read, err := c.Get(ctx, merchantID, charge.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if read.Amount != charge.Amount || read.Description != charge.Description {
		t.Errorf("read back %+v, want %+v", read, charge)
	}
}

func TestALinkWithoutADescriptionIsRefused(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)

	if _, err := c.Create(ctx, merchantID, CreateChargeRequest{Amount: 10_000_00}); err == nil {
		t.Error("a link with no description was accepted; the payer would see nothing")
	}
	if _, err := c.Create(ctx, merchantID, CreateChargeRequest{Description: "x"}); err == nil {
		t.Error("a link with no amount was accepted")
	}
}

func TestTheSameReferenceCannotBeUsedTwice(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)

	req := CreateChargeRequest{Reference: "INV-001", Description: "Invoice 1", Amount: 10_000_00}
	if _, err := c.Create(ctx, merchantID, req); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := c.Create(ctx, merchantID, req); err == nil {
		t.Error("the same reference produced two links; reconciliation would be ambiguous")
	}
}

func TestALinkExpiresAndSaysSo(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)

	charge, err := c.Create(ctx, merchantID, CreateChargeRequest{
		Description: "Quick sale", Amount: 5_000_00, ExpiresIn: "10m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	c.now = func() time.Time { return time.Now().Add(time.Hour) }

	read, err := c.Get(ctx, merchantID, charge.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !read.Expired {
		t.Error("an expired link does not report itself as expired")
	}
	if read.Status != "open" {
		t.Errorf("status = %q: expiry is derived, the row is not rewritten by a read", read.Status)
	}

	if _, _, _, err := c.Lookup(ctx, charge.ID); err == nil {
		t.Error("an expired link was still payable")
	}
}

func TestAWindowOutsideTheAllowedRangeIsRefused(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)

	for _, window := range []string{"1m", "60d", "not-a-duration"} {
		_, err := c.Create(ctx, merchantID, CreateChargeRequest{
			Description: "x", Amount: 1000, ExpiresIn: window,
		})
		if err == nil {
			t.Errorf("expires_in %q was accepted", window)
		}
	}
}

func TestALinkIsPaidOnceEvenUnderConcurrency(t *testing.T) {
	c, pool, ctx, merchantID := seedCharges(t)
	charge := openCharge(t, c, ctx, merchantID)

	const racers = 12

	payerID := id.New("usr")
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ($1, '081255550001', 'Link Payer', 'x', 'verified', 'active')`,
		payerID); err != nil {
		t.Fatalf("seed payer: %v", err)
	}

	payments := make([]string, racers)
	for i := range payments {
		payments[i] = id.New("pay")
		if _, err := pool.Exec(ctx,
			`INSERT INTO payments (id, user_id, merchant_id, method, amount_minor, fee_minor, status)
			 VALUES ($1, $2, $3, 'payment_link', 4800000, 33600, 'succeeded')`,
			payments[i], payerID, merchantID); err != nil {
			t.Fatalf("seed payment %d: %v", i, err)
		}
	}
	var (
		mu       sync.Mutex
		accepted int
		refused  int
		wg       sync.WaitGroup
	)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()

			tx, err := pool.Begin(ctx)
			if err != nil {
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()

			err = c.MarkPaid(ctx, tx, charge.ID, payments[n], payerID)
			if err != nil {
				mu.Lock()
				refused++
				mu.Unlock()
				return
			}
			if err := tx.Commit(ctx); err != nil {
				mu.Lock()
				refused++
				mu.Unlock()
				return
			}

			mu.Lock()
			accepted++
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if accepted != 1 {
		t.Errorf("%d of %d racers paid the same link; it must be exactly 1", accepted, racers)
	}
	if refused != racers-1 {
		t.Errorf("refused = %d, want %d", refused, racers-1)
	}

	read, err := c.Get(ctx, merchantID, charge.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if read.Status != "paid" || read.PaymentID == "" {
		t.Errorf("after the race the link is %+v", read)
	}
}

func TestCancellingALinkStopsItBeingPaid(t *testing.T) {
	c, _, ctx, merchantID := seedCharges(t)
	charge := openCharge(t, c, ctx, merchantID)

	cancelled, err := c.Cancel(ctx, merchantID, charge.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", cancelled.Status)
	}

	if _, _, _, err := c.Lookup(ctx, charge.ID); err == nil {
		t.Error("a cancelled link was still payable")
	}
	if _, err := c.Cancel(ctx, merchantID, charge.ID); err == nil {
		t.Error("cancelling twice was accepted")
	}
}

func TestALinkIsInvisibleToAnotherMerchant(t *testing.T) {
	c, pool, ctx, merchantID := seedCharges(t)
	charge := openCharge(t, c, ctx, merchantID)

	other := id.New("merch")
	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
		 VALUES ($1, 'PT Other', 'Other', 'retail', 'active', 70)`, other); err != nil {
		t.Fatalf("seed other merchant: %v", err)
	}

	if _, err := c.Get(ctx, other, charge.ID); !errors.Is(err, ErrChargeNotFound) {
		t.Errorf("got %v, want ErrChargeNotFound: a link leaked across merchants", err)
	}
	if _, err := c.Cancel(ctx, other, charge.ID); err == nil {
		t.Error("another merchant cancelled a link that is not theirs")
	}

	list, err := c.List(ctx, other, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("another merchant sees %d links", len(list))
	}
}

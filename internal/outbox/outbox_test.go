package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
	"github.com/umars28/marspay/internal/testdb"
)

func newRelay(t *testing.T) (*Relay, *MemoryPublisher, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool, ctx := testdb.New(t)
	pub := NewMemoryPublisher()
	return NewRelay(pool, pub), pub, pool, ctx
}

func write(t *testing.T, ctx context.Context, pool *pgxpool.Pool, msgs ...Message) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := Write(ctx, tx, msgs...); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func msg(key, eventType string, n int) Message {
	return Message{
		Topic:        TopicPaymentEvents,
		PartitionKey: key,
		EventType:    eventType,
		Payload:      []byte(fmt.Sprintf(`{"seq":%d}`, n)),
	}
}

func TestAMessageIsOnlyVisibleIfItsTransactionCommitted(t *testing.T) {
	relay, _, pool, ctx := newRelay(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := Write(ctx, tx, msg("merch_1", "payment.succeeded", 1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after a rollback, want 0", pending)
	}
}

func TestLedgerAndEventCommitTogetherOrNotAtAll(t *testing.T) {
	relay, _, pool, ctx := newRelay(t)
	repo := ledger.NewRepo(pool)

	for _, a := range [][4]string{
		{"acc_w", ledger.OwnerUser, "usr_1", ledger.TypeUserWallet},
		{"acc_p", ledger.OwnerMerchant, "merch_1", ledger.TypeMerchantPayable},
		{"acc_f", ledger.OwnerPlatform, "", ledger.TypeFeeRevenue},
	} {
		if err := repo.EnsureAccount(ctx, a[0], a[1], a[2], a[3]); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	posting, err := ledger.NewMerchantPayment(ledger.MerchantPayment{
		TransactionID:    id.New("txn"),
		PayerAccountID:   "acc_w",
		PayableAccountID: "acc_p",
		FeeAccountID:     "acc_f",
		Amount:           money.FromRupiah(32_000),
		FeeBps:           70,
	})
	if err != nil {
		t.Fatalf("build posting: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := repo.PostTx(ctx, tx, posting); err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := Write(ctx, tx, msg("merch_1", "payment.succeeded", 1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	entries, err := repo.Entries(ctx, posting.TransactionID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}

	if len(entries) != 0 || pending != 0 {
		t.Errorf("after a rollback: %d ledger entries and %d outbox rows, want 0 and 0",
			len(entries), pending)
	}
}

func TestSweepPublishesAndMarksEverythingItSent(t *testing.T) {
	relay, pub, pool, ctx := newRelay(t)

	write(t, ctx, pool,
		msg("merch_1", "payment.succeeded", 1),
		msg("merch_2", "payment.succeeded", 2),
		msg("merch_3", "payment.failed", 3),
	)

	n, err := relay.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 3 {
		t.Errorf("published %d, want 3", n)
	}
	if got := len(pub.Published()); got != 3 {
		t.Errorf("publisher saw %d messages, want 3", got)
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after a successful sweep, want 0", pending)
	}
}

func TestOrderIsPreservedWithinAPartitionKey(t *testing.T) {
	relay, pub, pool, ctx := newRelay(t)

	for i := 1; i <= 20; i++ {
		write(t, ctx, pool, msg("merch_hot", "payment.succeeded", i))
	}

	if _, err := relay.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	published := pub.PublishedFor("merch_hot")
	if len(published) != 20 {
		t.Fatalf("published %d, want 20", len(published))
	}
	for i := 1; i < len(published); i++ {
		if published[i].ID <= published[i-1].ID {
			t.Fatalf("out of order at position %d: id %d came after %d",
				i, published[i].ID, published[i-1].ID)
		}
	}
}

func TestAFailedPublishLeavesEverythingUnpublishedAndCountsTheAttempt(t *testing.T) {
	relay, pub, pool, ctx := newRelay(t)
	write(t, ctx, pool, msg("merch_1", "payment.succeeded", 1))

	pub.FailNext(errors.New("broker unavailable"))

	if _, err := relay.Sweep(ctx); err == nil {
		t.Fatal("sweep reported success while the publisher failed")
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("pending = %d, want 1: a failed publish must not lose the message", pending)
	}

	var attempts int
	var lastError *string
	if err := pool.QueryRow(ctx,
		`SELECT attempts, last_error FROM outbox LIMIT 1`).Scan(&attempts, &lastError); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if lastError == nil || *lastError == "" {
		t.Error("the failure reason was not recorded")
	}

	if _, err := relay.Sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := len(pub.Published()); got != 1 {
		t.Errorf("after recovery the publisher saw %d messages, want 1", got)
	}
}

func TestTwoRelaysNeverSplitOnePartitionKey(t *testing.T) {
	pool, ctx := testdb.New(t)

	const perKey = 40
	for i := 1; i <= perKey; i++ {
		write(t, ctx, pool, msg("merch_contended", "payment.succeeded", i))
	}

	pubA, pubB := NewMemoryPublisher(), NewMemoryPublisher()
	relayA := NewRelay(pool, pubA)
	relayB := NewRelay(pool, pubB)
	relayA.batchSize = 5
	relayB.batchSize = 5

	var wg sync.WaitGroup
	for _, r := range []*Relay{relayA, relayB} {
		wg.Add(1)
		go func(r *Relay) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := r.Sweep(context.Background()); err != nil {
					t.Errorf("sweep: %v", err)
					return
				}
			}
		}(r)
	}
	wg.Wait()

	a := pubA.PublishedFor("merch_contended")
	b := pubB.PublishedFor("merch_contended")

	if len(a)+len(b) != perKey {
		t.Fatalf("published %d + %d = %d, want %d",
			len(a), len(b), len(a)+len(b), perKey)
	}

	seen := map[int64]bool{}
	for _, m := range append(append([]Message{}, a...), b...) {
		if seen[m.ID] {
			t.Fatalf("message %d was published twice", m.ID)
		}
		seen[m.ID] = true
	}

	for _, batch := range [][]Message{a, b} {
		for i := 1; i < len(batch); i++ {
			if batch[i].ID <= batch[i-1].ID {
				t.Fatalf("a relay published out of order: %d after %d", batch[i].ID, batch[i-1].ID)
			}
		}
	}
}

func TestDifferentKeysAreProcessedInParallel(t *testing.T) {
	pool, ctx := testdb.New(t)

	for _, key := range []string{"merch_a", "merch_b", "merch_c", "merch_d"} {
		for i := 1; i <= 10; i++ {
			write(t, ctx, pool, msg(key, "payment.succeeded", i))
		}
	}

	pubA, pubB := NewMemoryPublisher(), NewMemoryPublisher()
	relayA := NewRelay(pool, pubA)
	relayB := NewRelay(pool, pubB)

	var wg sync.WaitGroup
	for _, r := range []*Relay{relayA, relayB} {
		wg.Add(1)
		go func(r *Relay) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := r.Sweep(context.Background()); err != nil {
					t.Errorf("sweep: %v", err)
					return
				}
			}
		}(r)
	}
	wg.Wait()

	total := len(pubA.Published()) + len(pubB.Published())
	if total != 40 {
		t.Errorf("published %d, want 40", total)
	}

	relay := NewRelay(pool, NewMemoryPublisher())
	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d, want 0", pending)
	}
}

func TestWriteRejectsAMessageWithoutAPartitionKey(t *testing.T) {
	_, _, pool, ctx := newRelay(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = Write(ctx, tx, Message{Topic: TopicPaymentEvents, EventType: "payment.succeeded"})
	if !errors.Is(err, ErrNoPartitionKey) {
		t.Errorf("got %v, want ErrNoPartitionKey: without a key there is no ordering guarantee", err)
	}
}

func TestPruneRemovesOnlyPublishedHistory(t *testing.T) {
	relay, _, pool, ctx := newRelay(t)

	write(t, ctx, pool, msg("merch_1", "payment.succeeded", 1))
	if _, err := relay.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	write(t, ctx, pool, msg("merch_2", "payment.succeeded", 2))

	if _, err := pool.Exec(ctx,
		`UPDATE outbox SET published_at = now() - interval '8 days'
		 WHERE published_at IS NOT NULL`); err != nil {
		t.Fatalf("age rows: %v", err)
	}

	removed, err := relay.Prune(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d rows, want 1", removed)
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("pending = %d after pruning, want 1: unpublished rows must survive", pending)
	}
}

package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

func testRepo(t *testing.T) (*Repo, context.Context) {
	t.Helper()

	dsn := os.Getenv("MARSPAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MARSPAY_TEST_DATABASE_URL to run ledger integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	applyMigrations(t, ctx, pool)
	return NewRepo(pool), ctx
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	down, err := filepath.Glob("../../migrations/*.down.sql")
	if err != nil {
		t.Fatalf("glob down migrations: %v", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(down)))
	for _, f := range down {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		_, _ = pool.Exec(ctx, string(sql))
	}

	up, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob up migrations: %v", err)
	}
	sort.Strings(up)
	for _, f := range up {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
}

func seedAccounts(t *testing.T, ctx context.Context, r *Repo, specs map[string][2]string) {
	t.Helper()
	for accountID, spec := range specs {
		if err := r.EnsureAccount(ctx, accountID, spec[0], accountID, spec[1]); err != nil {
			t.Fatalf("ensure account %s: %v", accountID, err)
		}
	}
}

func TestPostWritesBalancedPayment(t *testing.T) {
	r, ctx := testRepo(t)
	seedAccounts(t, ctx, r, map[string][2]string{
		"acc_wallet":  {"user", "user_wallet"},
		"acc_payable": {"merchant", "merchant_payable"},
		"acc_fee":     {"platform", "platform_fee_revenue"},
	})

	txID := id.New("txn")
	posting, err := NewMerchantPayment(MerchantPayment{
		TransactionID:    txID,
		PayerAccountID:   "acc_wallet",
		PayableAccountID: "acc_payable",
		FeeAccountID:     "acc_fee",
		Amount:           money.FromRupiah(32_000),
		FeeBps:           70,
	})
	if err != nil {
		t.Fatalf("build posting: %v", err)
	}

	if err := r.Post(ctx, posting); err != nil {
		t.Fatalf("post: %v", err)
	}

	entries, err := r.Entries(ctx, txID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}

	wallet, err := r.Balance(ctx, "acc_wallet")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if wallet != -3_200_000 {
		t.Errorf("wallet balance = %d, want -3200000", wallet)
	}

	sum, err := r.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestDatabaseRejectsUnbalancedPostingBypassingGoValidation(t *testing.T) {
	r, ctx := testRepo(t)
	seedAccounts(t, ctx, r, map[string][2]string{
		"acc_wallet":  {"user", "user_wallet"},
		"acc_payable": {"merchant", "merchant_payable"},
	})

	unbalanced := Posting{
		TransactionID: id.New("txn"),
		Kind:          KindPayment,
		Entries: []Entry{
			{AccountID: "acc_wallet", Amount: -3_200_000},
			{AccountID: "acc_payable", Amount: 3_100_000},
		},
	}

	err := r.post(ctx, unbalanced)
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("post unbalanced: got %v, want ErrUnbalanced", err)
	}

	entries, err := r.Entries(ctx, unbalanced.TransactionID)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("rejected transaction left %d entries behind, want 0", len(entries))
	}

	sum, err := r.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum = %d, want 0", sum)
	}
}

func TestConcurrentPostingsKeepGlobalSumZero(t *testing.T) {
	r, ctx := testRepo(t)
	seedAccounts(t, ctx, r, map[string][2]string{
		"acc_wallet":  {"user", "user_wallet"},
		"acc_payable": {"merchant", "merchant_payable"},
		"acc_fee":     {"platform", "platform_fee_revenue"},
	})

	const n = 400
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			posting, err := NewMerchantPayment(MerchantPayment{
				TransactionID:    id.New("txn"),
				PayerAccountID:   "acc_wallet",
				PayableAccountID: "acc_payable",
				FeeAccountID:     "acc_fee",
				Amount:           money.Minor(1_000 + i*37),
				FeeBps:           70,
			})
			if err != nil {
				errs <- err
				return
			}
			if err := r.Post(ctx, posting); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent post: %v", err)
	}

	sum, err := r.GlobalSum(ctx)
	if err != nil {
		t.Fatalf("global sum: %v", err)
	}
	if sum != 0 {
		t.Errorf("global sum after %d concurrent postings = %d, want 0", n, sum)
	}

	wallet, err := r.Balance(ctx, "acc_wallet")
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	var expected money.Minor
	for i := 0; i < n; i++ {
		expected -= money.Minor(1_000 + i*37)
	}
	if wallet != expected {
		t.Errorf("wallet balance = %d, want %d", wallet, expected)
	}
}

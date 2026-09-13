package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
)

const (
	feeBps      = 70
	paymentSize = money.Minor(3_200_000)
)

type result struct {
	Name        string
	Ops         int
	Concurrency int
	Elapsed     time.Duration
	Latencies   []time.Duration
	Errors      int
}

func (r result) rate() float64 {
	if r.Elapsed == 0 {
		return 0
	}
	return float64(r.Ops-r.Errors) / r.Elapsed.Seconds()
}

func (r result) percentile(p float64) time.Duration {
	if len(r.Latencies) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), r.Latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func main() {
	dsn := flag.String("dsn", "", "postgres connection string")
	ops := flag.Int("ops", 4000, "operations per scenario")
	concurrency := flag.Int("concurrency", 32, "concurrent writers")
	spread := flag.Int("spread", 200, "distinct merchant accounts in the spread scenario")
	flag.Parse()

	if *dsn == "" {
		fatal(errors.New("-dsn is required"))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		fatal(err)
	}

	fmt.Printf("==> %d operations, %d concurrent writers\n\n", *ops, *concurrency)

	appendHot, columnHot, err := setup(ctx, pool, *spread)
	if err != nil {
		fatal(err)
	}

	var results []result

	fixedPayer := func(int) string { return "bench_payer" }
	fixedFee := func(int) string { return "bench_fee" }
	spreadPayer := func(i int) string { return fmt.Sprintf("bench_payer_%d", i%*spread) }
	shardedFee := func(i int) string { return fmt.Sprintf("bench_fee_%d", i%16) }
	spreadMerchant := func(i int) string { return fmt.Sprintf("acc_bench_column_%d", i%*spread) }

	r, err := runAppend(ctx, pool, "append, one hot merchant", *ops, *concurrency,
		func(int) string { return appendHot })
	if err != nil {
		fatal(err)
	}
	results = append(results, r)

	r, err = runAppend(ctx, pool, "append, spread merchants", *ops, *concurrency,
		func(i int) string { return fmt.Sprintf("acc_bench_append_%d", i%*spread) })
	if err != nil {
		fatal(err)
	}
	results = append(results, r)

	r, err = runColumn(ctx, pool, "column, one hot merchant", *ops, *concurrency,
		fixedPayer, func(int) string { return columnHot }, fixedFee)
	if err != nil {
		fatal(err)
	}
	results = append(results, r)

	r, err = runColumn(ctx, pool, "column, spread merchants", *ops, *concurrency,
		spreadPayer, spreadMerchant, fixedFee)
	if err != nil {
		fatal(err)
	}
	results = append(results, r)

	r, err = runColumn(ctx, pool, "column, sharded fee row", *ops, *concurrency,
		spreadPayer, spreadMerchant, shardedFee)
	if err != nil {
		fatal(err)
	}
	results = append(results, r)

	report(results)

	if err := reportReads(ctx, pool, appendHot, columnHot); err != nil {
		fatal(err)
	}
	if err := verify(ctx, pool, appendHot, columnHot, *ops); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ledgerbench:", err)
	os.Exit(1)
}

func setup(ctx context.Context, pool *pgxpool.Pool, spread int) (string, string, error) {
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS bench_balances (
		   id            TEXT PRIMARY KEY,
		   balance_minor BIGINT NOT NULL DEFAULT 0
		 )`); err != nil {
		return "", "", err
	}
	if _, err := pool.Exec(ctx, `TRUNCATE bench_balances`); err != nil {
		return "", "", err
	}

	repo := ledger.NewRepo(pool)
	appendHot := "acc_bench_append_hot"
	columnHot := "acc_bench_column_hot"

	accounts := []string{appendHot, "acc_bench_payer", "acc_bench_fee"}
	for i := 0; i < spread; i++ {
		accounts = append(accounts, fmt.Sprintf("acc_bench_append_%d", i))
	}
	for _, a := range accounts {
		if err := repo.EnsureAccount(ctx, a, ledger.OwnerPlatform, "", ledger.TypeFloat); err != nil {
			return "", "", err
		}
	}

	columns := []string{columnHot, "bench_payer", "bench_fee"}
	for i := 0; i < spread; i++ {
		columns = append(columns,
			fmt.Sprintf("acc_bench_column_%d", i),
			fmt.Sprintf("bench_payer_%d", i))
	}
	for i := 0; i < 16; i++ {
		columns = append(columns, fmt.Sprintf("bench_fee_%d", i))
	}
	for _, c := range columns {
		if _, err := pool.Exec(ctx,
			`INSERT INTO bench_balances (id, balance_minor) VALUES ($1, 0)
			 ON CONFLICT (id) DO NOTHING`, c); err != nil {
			return "", "", err
		}
	}

	return appendHot, columnHot, nil
}

func drive(name string, ops, concurrency int, work func(i int) error) result {
	r := result{Name: name, Ops: ops, Concurrency: concurrency}

	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan int, ops)

	for i := 0; i < ops; i++ {
		jobs <- i
	}
	close(jobs)

	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				began := time.Now()
				err := work(i)
				took := time.Since(began)

				mu.Lock()
				if err != nil {
					r.Errors++
				} else {
					r.Latencies = append(r.Latencies, took)
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	r.Elapsed = time.Since(start)
	return r
}

func runAppend(ctx context.Context, pool *pgxpool.Pool, name string, ops, concurrency int, target func(int) string) (result, error) {
	repo := ledger.NewRepo(pool)

	r := drive(name, ops, concurrency, func(i int) error {
		posting, err := ledger.NewMerchantPayment(ledger.MerchantPayment{
			TransactionID:    id.New("txn"),
			PayerAccountID:   "acc_bench_payer",
			PayableAccountID: target(i),
			FeeAccountID:     "acc_bench_fee",
			Amount:           paymentSize,
			FeeBps:           feeBps,
		})
		if err != nil {
			return err
		}
		return repo.Post(ctx, posting)
	})
	return r, nil
}

func runColumn(ctx context.Context, pool *pgxpool.Pool, name string, ops, concurrency int,
	payer, target, fee func(int) string) (result, error) {
	feeAmount, err := money.FeeHalfUp(paymentSize, feeBps)
	if err != nil {
		return result{}, err
	}
	net := paymentSize - feeAmount

	r := drive(name, ops, concurrency, func(i int) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx,
			`UPDATE bench_balances SET balance_minor = balance_minor - $1 WHERE id = $2`,
			int64(paymentSize), payer(i)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE bench_balances SET balance_minor = balance_minor + $1 WHERE id = $2`,
			int64(net), target(i)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE bench_balances SET balance_minor = balance_minor + $1 WHERE id = $2`,
			int64(feeAmount), fee(i)); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
	return r, nil
}

func report(results []result) {
	fmt.Printf("%-26s %10s %10s %10s %10s %8s\n",
		"scenario", "ops/s", "p50", "p95", "p99", "errors")
	fmt.Println("---------------------------------------------------------------------------------")
	for _, r := range results {
		fmt.Printf("%-26s %10.0f %10s %10s %10s %8d\n",
			r.Name, r.rate(),
			round(r.percentile(0.50)), round(r.percentile(0.95)),
			round(r.percentile(0.99)), r.Errors)
	}
	fmt.Println()
}

func reportReads(ctx context.Context, pool *pgxpool.Pool, appendHot, columnHot string) error {
	const reads = 500

	appendRead := drive("append balance read", reads, 8, func(int) error {
		var v int64
		return pool.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries WHERE account_id = $1`,
			appendHot).Scan(&v)
	})

	columnRead := drive("column balance read", reads, 8, func(int) error {
		var v int64
		return pool.QueryRow(ctx,
			`SELECT balance_minor FROM bench_balances WHERE id = $1`, columnHot).Scan(&v)
	})

	fmt.Println("Reading one account balance:")
	fmt.Printf("%-26s %10s %10s %10s\n", "", "reads/s", "p50", "p99")
	fmt.Println("------------------------------------------------------------")
	for _, r := range []result{appendRead, columnRead} {
		fmt.Printf("%-26s %10.0f %10s %10s\n",
			r.Name, r.rate(), round(r.percentile(0.50)), round(r.percentile(0.99)))
	}
	fmt.Println()
	return nil
}

func verify(ctx context.Context, pool *pgxpool.Pool, appendHot, columnHot string, ops int) error {
	feeAmount, err := money.FeeHalfUp(paymentSize, feeBps)
	if err != nil {
		return err
	}
	expected := int64(paymentSize-feeAmount) * int64(ops)

	var appendBalance, columnBalance, globalSum int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries WHERE account_id = $1`,
		appendHot).Scan(&appendBalance); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx,
		`SELECT balance_minor FROM bench_balances WHERE id = $1`, columnHot).Scan(&columnBalance); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries`).Scan(&globalSum); err != nil {
		return err
	}

	fmt.Println("Correctness after the hot-account runs:")
	fmt.Printf("  expected credit on the hot account : %d\n", expected)
	fmt.Printf("  append  result                     : %d\n", appendBalance)
	fmt.Printf("  column  result                     : %d\n", columnBalance)
	fmt.Printf("  ledger global sum                  : %d\n", globalSum)

	if appendBalance != expected || columnBalance != expected {
		return errors.New("one of the implementations lost an update")
	}
	if globalSum != 0 {
		return errors.New("the ledger no longer sums to zero")
	}

	fmt.Println("\nBoth implementations are correct. The difference is throughput, not accuracy.")
	return nil
}

func round(d time.Duration) string {
	switch {
	case d > time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d > time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(time.Microsecond).String()
	}
}

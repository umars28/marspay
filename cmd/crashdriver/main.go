package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/ledger"
	"github.com/umars28/marspay/internal/money"
)

const (
	payerAccount   = "acc_crash_wallet"
	payableAccount = "acc_crash_payable"
	feeAccount     = "acc_crash_fee"
	feeBps         = 70
)

func main() {
	phase := flag.String("phase", "", "load or verify")
	dsn := flag.String("dsn", "", "postgres connection string")
	journal := flag.String("journal", "", "path to the acknowledged-commit journal")
	workers := flag.Int("workers", 16, "concurrent writers")
	duration := flag.Duration("duration", 20*time.Second, "how long to keep writing")
	flag.Parse()

	if *dsn == "" || *journal == "" {
		fatal(errors.New("both -dsn and -journal are required"))
	}

	switch *phase {
	case "seed":
		fatal(seed(*dsn))
	case "load":
		fatal(load(*dsn, *journal, *workers, *duration))
	case "verify":
		fatal(verify(*dsn, *journal))
	default:
		fatal(fmt.Errorf("unknown phase %q", *phase))
	}
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "crashdriver:", err)
		os.Exit(1)
	}
}

func connect(dsn string) (*pgxpool.Pool, context.Context, error) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	return pool, ctx, pool.Ping(ctx)
}

func seed(dsn string) error {
	pool, ctx, err := connect(dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	repo := ledger.NewRepo(pool)
	accounts := [][4]string{
		{payerAccount, ledger.OwnerUser, "usr_crash", ledger.TypeUserWallet},
		{payableAccount, ledger.OwnerMerchant, "merch_crash", ledger.TypeMerchantPayable},
		{feeAccount, ledger.OwnerPlatform, "", ledger.TypeFeeRevenue},
	}
	for _, a := range accounts {
		if err := repo.EnsureAccount(ctx, a[0], a[1], a[2], a[3]); err != nil {
			return err
		}
	}
	fmt.Println("seeded")
	return nil
}

func load(dsn, journalPath string, workers int, duration time.Duration) error {
	pool, ctx, err := connect(dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	repo := ledger.NewRepo(pool)
	deadline := time.Now().Add(duration)

	var journalMu sync.Mutex
	var acked, failed atomic.Int64
	var wg sync.WaitGroup

	const giveUpAfter = 50

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			consecutive := 0

			for i := 0; time.Now().Before(deadline); i++ {
				if consecutive >= giveUpAfter {
					return
				}
				amount := money.Minor(100_000 + (w*1_000+i)%90_000)
				txID := id.New("txn")

				posting, err := ledger.NewMerchantPayment(ledger.MerchantPayment{
					TransactionID:    txID,
					PayerAccountID:   payerAccount,
					PayableAccountID: payableAccount,
					FeeAccountID:     feeAccount,
					Amount:           amount,
					FeeBps:           feeBps,
				})
				if err != nil {
					failed.Add(1)
					consecutive++
					continue
				}

				if err := repo.Post(ctx, posting); err != nil {
					failed.Add(1)
					consecutive++
					time.Sleep(20 * time.Millisecond)
					continue
				}
				consecutive = 0

				journalMu.Lock()
				_, writeErr := fmt.Fprintf(f, "%s %d\n", txID, amount)
				if writeErr == nil {
					writeErr = f.Sync()
				}
				journalMu.Unlock()

				if writeErr != nil {
					return
				}
				acked.Add(1)
			}
		}(w)
	}

	wg.Wait()
	fmt.Printf("acknowledged=%d rejected=%d\n", acked.Load(), failed.Load())
	return nil
}

func verify(dsn, journalPath string) error {
	pool, ctx, err := connect(dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	claimed, total, err := readJournal(journalPath)
	if err != nil {
		return err
	}
	if len(claimed) == 0 {
		return errors.New("the journal is empty, so the run proves nothing")
	}

	repo := ledger.NewRepo(pool)

	var problems []string

	missing := 0
	unbalanced := 0
	for txID, amount := range claimed {
		entries, err := repo.Entries(ctx, txID)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			missing++
			if missing <= 5 {
				problems = append(problems,
					fmt.Sprintf("acknowledged %s (%s) is absent after recovery", txID, amount))
			}
			continue
		}

		var sum, debit money.Minor
		for _, e := range entries {
			sum += e.Amount
			if e.Amount < 0 {
				debit -= e.Amount
			}
		}
		if sum != 0 || debit != amount {
			unbalanced++
			problems = append(problems,
				fmt.Sprintf("%s recovered with sum=%d debit=%s want debit=%s", txID, sum, debit, amount))
		}
	}

	var globalSum int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries`).Scan(&globalSum); err != nil {
		return err
	}

	var orphanTransactions, orphanEntries int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_transactions t
		 WHERE NOT EXISTS (SELECT 1 FROM ledger_entries e WHERE e.transaction_id = t.id)`).
		Scan(&orphanTransactions); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_entries e
		 WHERE NOT EXISTS (SELECT 1 FROM ledger_transactions t WHERE t.id = e.transaction_id)`).
		Scan(&orphanEntries); err != nil {
		return err
	}

	var recovered int64
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger_transactions`).Scan(&recovered); err != nil {
		return err
	}

	fmt.Println("acknowledged by the driver :", len(claimed))
	fmt.Println("transactions after recovery:", recovered)
	fmt.Println("acknowledged but missing   :", missing)
	fmt.Println("recovered unbalanced       :", unbalanced)
	fmt.Println("transactions without entries:", orphanTransactions)
	fmt.Println("entries without transaction :", orphanEntries)
	fmt.Println("global sum                  :", globalSum)
	fmt.Println("total acknowledged value    :", total)

	if globalSum != 0 {
		problems = append(problems, fmt.Sprintf("global sum is %d, want 0", globalSum))
	}
	if orphanTransactions != 0 {
		problems = append(problems, fmt.Sprintf("%d transactions have no entries", orphanTransactions))
	}
	if orphanEntries != 0 {
		problems = append(problems, fmt.Sprintf("%d entries have no transaction", orphanEntries))
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  FAIL:", p)
		}
		return fmt.Errorf("%d problems found after crash recovery", len(problems))
	}

	fmt.Println()
	fmt.Println("PASS: every acknowledged commit survived, nothing partial, nothing invented")
	return nil
}

func readJournal(path string) (map[string]money.Minor, money.Minor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	claimed := make(map[string]money.Minor)
	var total money.Minor

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		amount, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		claimed[fields[0]] = money.Minor(amount)
		total += money.Minor(amount)
	}
	return claimed, total, scanner.Err()
}

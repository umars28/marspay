package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/merchant"
)

const (
	consumerID    = "usr_demo"
	consumerPhone = "081200000001"
	consumerPin   = "294715"
	merchantID    = "merch_demo"
	operatorID    = "usr_ops"
	operatorPhone = "081200000009"
	walletAccount = "acc_usr_demo_user_wallet"
	openingMinor  = 250_000_00
)

func main() {
	dsn := flag.String("dsn", "", "postgres connection string")
	redisAddr := flag.String("redis", "", "redis address")
	flag.Parse()

	if *dsn == "" || *redisAddr == "" {
		fatal(errors.New("-dsn and -redis are required"))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
	defer func() { _ = rdb.Close() }()

	pinHash, err := auth.HashPin(consumerPin)
	if err != nil {
		fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ($1, $2, 'Demo Consumer', $3, 'verified', 'active')
		 ON CONFLICT (id) DO UPDATE SET pin_hash = EXCLUDED.pin_hash`,
		consumerID, consumerPhone, pinHash); err != nil {
		fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO user_limits (user_id, max_balance_minor, max_per_txn_minor, max_monthly_minor)
		 VALUES ($1, 2000000000, 2000000000, 2000000000)
		 ON CONFLICT (user_id) DO NOTHING`, consumerID); err != nil {
		fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
		 VALUES ('usr_demo_payee', '081200000002', 'Demo Friend', $1, 'verified', 'active')
		 ON CONFLICT (id) DO NOTHING`, pinHash); err != nil {
		fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status, role)
		 VALUES ($1, $2, 'Demo Operator', $3, 'verified', 'active', 'operator')
		 ON CONFLICT (id) DO UPDATE SET pin_hash = EXCLUDED.pin_hash, role = 'operator'`,
		operatorID, operatorPhone, pinHash); err != nil {
		fatal(err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps, payout_mode,
		                        payout_bank, payout_account, payout_name)
		 VALUES ($1, 'PT Kopi Demo', 'Kopi Demo Senopati', 'food', 'active', 70, 'instant',
		         'bca', '1234567890', 'PT Kopi Demo')
		 ON CONFLICT (id) DO NOTHING`, merchantID); err != nil {
		fatal(err)
	}

	accounts := [][3]string{
		{walletAccount, "user", consumerID},
		{"acc_usr_demo_payee_user_wallet", "user", "usr_demo_payee"},
	}
	for _, a := range accounts {
		if _, err := pool.Exec(ctx,
			`INSERT INTO accounts (id, owner_type, owner_id, account_type)
			 VALUES ($1, $2, $3, 'user_wallet') ON CONFLICT (id) DO NOTHING`,
			a[0], a[1], a[2]); err != nil {
			fatal(err)
		}
	}

	platform := [][3]string{
		{"acc_merch_demo_merchant_payable", "merchant", "merchant_payable"},
		{"acc_merch_demo_merchant_holdback", "merchant", "merchant_holdback"},
		{"acc_platform_platform_fee_revenue", "platform", "platform_fee_revenue"},
		{"acc_platform_platform_float", "platform", "platform_float"},
	}
	for _, a := range platform {
		owner := any(merchantID)
		if a[1] == "platform" {
			owner = nil
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO accounts (id, owner_type, owner_id, account_type)
			 VALUES ($1, $2, $3, $4) ON CONFLICT (id) DO NOTHING`,
			a[0], a[1], owner, a[2]); err != nil {
			fatal(err)
		}
	}

	if err := openBalance(ctx, pool, walletAccount, openingMinor); err != nil {
		fatal(err)
	}
	if err := rdb.Set(ctx, "marspay:balance:"+walletAccount, openingMinor, 0).Err(); err != nil {
		fatal(err)
	}

	keys := merchant.NewKeys(pool, compliance.NewAudit(pool))
	key, err := keys.Create(ctx, merchantID, merchant.CreateKeyRequest{
		Name:   "demo",
		Mode:   "test",
		Scopes: []string{merchant.ScopeRead, merchant.ScopeWrite},
	}, "demo-seed")
	if err != nil {
		fatal(err)
	}

	fmt.Printf(`
  Consumer
    phone            %s
    PIN              %s
    opening balance  Rp %s

  Operator (admin, ops, risk screens)
    phone            %s
    PIN              %s

  Merchant
    id               %s
    API key          %s

  The one-time code is returned by POST /v1/auth/otp because this is a demo;
  in any other mode that field is empty and the code would go out by SMS.
`, consumerPhone, consumerPin, rupiah(openingMinor),
		operatorPhone, consumerPin, merchantID, key.Secret)
}

func openBalance(ctx context.Context, pool *pgxpool.Pool, account string, amount int64) error {
	var existing int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries WHERE account_id = $1`,
		account).Scan(&existing); err != nil {
		return err
	}
	if existing >= amount {
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO accounts (id, owner_type, owner_id, account_type)
		 VALUES ('acc_platform_platform_float', 'platform', NULL, 'platform_float')
		 ON CONFLICT (id) DO NOTHING`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger_transactions (id, kind, description)
		 VALUES ('txn_demo_opening', 'adjustment', 'demo opening balance')
		 ON CONFLICT (id) DO NOTHING`); err != nil {
		return err
	}

	rows := [][2]any{
		{account, amount - existing},
		{"acc_platform_platform_float", -(amount - existing)},
	}
	for i, r := range rows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor)
			 VALUES ($1, 'txn_demo_opening', $2, $3)`,
			fmt.Sprintf("led_demo_opening_%d", i), r[0], r[1]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func rupiah(minor int64) string {
	whole := minor / 100
	out := ""
	digits := fmt.Sprint(whole)
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out += "."
		}
		out += string(c)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "demoseed:", err)
	os.Exit(1)
}

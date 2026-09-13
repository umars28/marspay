package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/money"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Post(ctx context.Context, p Posting) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return r.post(ctx, p)
}

func (r *Repo) PostTx(ctx context.Context, tx pgx.Tx, p Posting) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return writePosting(ctx, tx, p)
}

func (r *Repo) post(ctx context.Context, p Posting) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ledger: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := writePosting(ctx, tx, p); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return TranslateCommitError(err)
	}
	return nil
}

func writePosting(ctx context.Context, tx pgx.Tx, p Posting) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO ledger_transactions (id, kind, reference_id, description)
		 VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))`,
		p.TransactionID, string(p.Kind), p.ReferenceID, p.Description)
	if err != nil {
		return fmt.Errorf("ledger: insert transaction: %w", err)
	}

	batch := &pgx.Batch{}
	for _, e := range p.Entries {
		batch.Queue(
			`INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor)
			 VALUES ($1, $2, $3, $4)`,
			id.New("led"), p.TransactionID, e.AccountID, int64(e.Amount))
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("ledger: insert entries: %w", err)
	}
	return nil
}

func TranslateCommitError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.Contains(pgErr.Message, "is unbalanced") {
		return fmt.Errorf("%w: rejected by database at commit: %s", ErrUnbalanced, pgErr.Message)
	}
	return fmt.Errorf("ledger: commit: %w", err)
}

func (r *Repo) Balance(ctx context.Context, accountID string) (money.Minor, error) {
	var balance int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries WHERE account_id = $1`,
		accountID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("ledger: balance for %s: %w", accountID, err)
	}
	return money.Minor(balance), nil
}

func (r *Repo) GlobalSum(ctx context.Context) (money.Minor, error) {
	var sum int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("ledger: global sum: %w", err)
	}
	return money.Minor(sum), nil
}

func (r *Repo) Entries(ctx context.Context, transactionID string) ([]Entry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT account_id, amount_minor FROM ledger_entries
		 WHERE transaction_id = $1 ORDER BY id`,
		transactionID)
	if err != nil {
		return nil, fmt.Errorf("ledger: entries for %s: %w", transactionID, err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var amount int64
		if err := rows.Scan(&e.AccountID, &amount); err != nil {
			return nil, fmt.Errorf("ledger: scan entry: %w", err)
		}
		e.Amount = money.Minor(amount)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (r *Repo) EnsureAccount(ctx context.Context, accountID, ownerType, ownerID, accountType string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO accounts (id, owner_type, owner_id, account_type)
		 VALUES ($1, $2, NULLIF($3, ''), $4)
		 ON CONFLICT (id) DO NOTHING`,
		accountID, ownerType, ownerID, accountType)
	if err != nil {
		return fmt.Errorf("ledger: ensure account %s: %w", accountID, err)
	}
	return nil
}

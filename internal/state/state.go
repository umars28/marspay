package state

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	KindTopup      = "topup"
	KindWithdrawal = "withdrawal"
	KindBillPay    = "bill_payment"
	KindPayout     = "payout"
	KindDispute    = "dispute"
	KindKYC        = "kyc_submission"
	KindCharge     = "charge"
	KindAccount    = "account"

	ActorProvider = "provider-callback"
	ActorSystem   = "system"
)

type Transition struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"operation_kind"`
	Operation string    `json:"operation_id"`
	From      string    `json:"from_status,omitempty"`
	To        string    `json:"to_status"`
	Actor     string    `json:"actor"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Recorder struct {
	pool *pgxpool.Pool
}

func NewRecorder(pool *pgxpool.Pool) *Recorder {
	return &Recorder{pool: pool}
}

func RecordTx(ctx context.Context, tx pgx.Tx, kind, operationID, from, to, actor, reason string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO operation_state_transitions
		   (operation_kind, operation_id, from_status, to_status, actor, reason)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''))`,
		kind, operationID, from, to, actor, reason)
	if err != nil {
		return fmt.Errorf("state: record %s %s: %w", kind, operationID, err)
	}
	return nil
}

func (r *Recorder) Record(ctx context.Context, kind, operationID, from, to, actor, reason string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO operation_state_transitions
		   (operation_kind, operation_id, from_status, to_status, actor, reason)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''))`,
		kind, operationID, from, to, actor, reason)
	if err != nil {
		return fmt.Errorf("state: record %s %s: %w", kind, operationID, err)
	}
	return nil
}

func (r *Recorder) History(ctx context.Context, kind, operationID string) ([]Transition, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, operation_kind, operation_id, COALESCE(from_status, ''), to_status,
		        actor, COALESCE(reason, ''), created_at
		 FROM operation_state_transitions
		 WHERE ($1 = '' OR operation_kind = $1) AND operation_id = $2
		 ORDER BY created_at, id`, kind, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Transition{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.Kind, &t.Operation, &t.From, &t.To,
			&t.Actor, &t.Reason, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *Recorder) Recent(ctx context.Context, kind string, limit int) ([]Transition, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := r.pool.Query(ctx,
		`SELECT id, operation_kind, operation_id, COALESCE(from_status, ''), to_status,
		        actor, COALESCE(reason, ''), created_at
		 FROM operation_state_transitions
		 WHERE ($1 = '' OR operation_kind = $1)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Transition{}
	for rows.Next() {
		var t Transition
		if err := rows.Scan(&t.ID, &t.Kind, &t.Operation, &t.From, &t.To,
			&t.Actor, &t.Reason, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

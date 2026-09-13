package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoActor  = errors.New("compliance: an audited action needs an actor")
	ErrNoReason = errors.New("compliance: an audited action needs a written reason")
	ErrNoObject = errors.New("compliance: an audited action needs an object")
)

type Entry struct {
	ID         int64     `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	ObjectType string    `json:"object_type"`
	ObjectID   string    `json:"object_id"`
	Before     any       `json:"before,omitempty"`
	After      any       `json:"after,omitempty"`
	Reason     string    `json:"reason"`
	IP         string    `json:"ip,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Audit struct {
	pool *pgxpool.Pool
}

func NewAudit(pool *pgxpool.Pool) *Audit {
	return &Audit{pool: pool}
}

func (a *Audit) Record(ctx context.Context, e Entry) error {
	if e.Actor == "" {
		return ErrNoActor
	}
	if e.Reason == "" {
		return ErrNoReason
	}
	if e.ObjectType == "" || e.ObjectID == "" {
		return ErrNoObject
	}

	before, err := encode(e.Before)
	if err != nil {
		return err
	}
	after, err := encode(e.After)
	if err != nil {
		return err
	}

	_, err = a.pool.Exec(ctx,
		`INSERT INTO audit_log
		   (actor, action, object_type, object_id, before_value, after_value, reason, ip)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet)`,
		e.Actor, e.Action, e.ObjectType, e.ObjectID, before, after, e.Reason, e.IP)
	if err != nil {
		return fmt.Errorf("compliance: record audit entry: %w", err)
	}
	return nil
}

func (a *Audit) For(ctx context.Context, objectType, objectID string) ([]Entry, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id, actor, action, object_type, object_id, reason,
		        COALESCE(host(ip), ''), created_at
		 FROM audit_log
		 WHERE object_type = $1 AND object_id = $2
		 ORDER BY created_at DESC, id DESC`,
		objectType, objectID)
	if err != nil {
		return nil, fmt.Errorf("compliance: read audit log: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.ObjectType,
			&e.ObjectID, &e.Reason, &e.IP, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("compliance: scan audit entry: %w", err)
		}
		e.CreatedAt = e.CreatedAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func (a *Audit) Recent(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := a.pool.Query(ctx,
		`SELECT id, actor, action, object_type, object_id, reason,
		        COALESCE(host(ip), ''), created_at
		 FROM audit_log ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("compliance: read audit log: %w", err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.ObjectType,
			&e.ObjectID, &e.Reason, &e.IP, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("compliance: scan audit entry: %w", err)
		}
		e.CreatedAt = e.CreatedAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func encode(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("compliance: encode audit value: %w", err)
	}
	return b, nil
}

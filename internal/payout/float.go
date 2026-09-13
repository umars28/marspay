package payout

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/money"
)

const (
	DegradeHighRiskBps = 7_000
	DegradeAllBps      = 8_500
	IncidentBps        = 9_500

	HighRiskHoldbackBps = 1_000

	DefaultCollectionWindow = 24 * time.Hour
)

type Position struct {
	OutstandingMinor money.Minor
	LimitMinor       money.Minor
	UtilisationBps   int
	InstantEnabled   bool
}

type Float struct {
	pool             *pgxpool.Pool
	limit            money.Minor
	collectionWindow time.Duration
}

func NewFloat(pool *pgxpool.Pool, limit money.Minor) *Float {
	return &Float{pool: pool, limit: limit, collectionWindow: DefaultCollectionWindow}
}

func (f *Float) Position(ctx context.Context) (Position, error) {
	var outstanding int64

	err := f.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(net_minor), 0) FROM payouts
		 WHERE mode = 'instant'
		   AND status IN ('queued', 'sending', 'settled')
		   AND created_at > now() - $1::interval`,
		f.collectionWindow.String()).Scan(&outstanding)
	if err != nil {
		return Position{}, fmt.Errorf("payout: float position: %w", err)
	}

	pos := Position{OutstandingMinor: money.Minor(outstanding), LimitMinor: f.limit}
	if f.limit > 0 {
		pos.UtilisationBps = int(outstanding * 10_000 / int64(f.limit))
	}
	pos.InstantEnabled = pos.UtilisationBps < DegradeAllBps
	return pos, nil
}

func (f *Float) Record(ctx context.Context, pos Position) error {
	_, err := f.pool.Exec(ctx,
		`INSERT INTO float_positions
		   (outstanding_minor, limit_minor, utilisation_bps, instant_enabled)
		 VALUES ($1, $2, $3, $4)`,
		int64(pos.OutstandingMinor), int64(pos.LimitMinor),
		pos.UtilisationBps, pos.InstantEnabled)
	if err != nil {
		return fmt.Errorf("payout: record float: %w", err)
	}
	return nil
}

func ModeFor(pos Position, holdbackBps int) (mode string, reason string) {
	switch {
	case pos.UtilisationBps >= DegradeAllBps:
		return "batch", "platform float above 85 percent"
	case pos.UtilisationBps >= DegradeHighRiskBps && holdbackBps > HighRiskHoldbackBps:
		return "batch", "platform float above 70 percent and merchant holdback above 10 percent"
	default:
		return "instant", ""
	}
}

func (f *Float) ExposureExceeded(ctx context.Context, merchantID string, amount money.Minor) (bool, error) {
	var outstanding, limit int64

	err := f.pool.QueryRow(ctx,
		`SELECT outstanding_minor, limit_minor FROM merchant_exposure WHERE merchant_id = $1`,
		merchantID).Scan(&outstanding, &limit)
	if err != nil {
		return false, nil
	}
	if limit <= 0 {
		return false, nil
	}
	return outstanding+int64(amount) > limit, nil
}

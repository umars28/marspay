package risk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/httpx"
)

type Snapshot struct {
	MerchantID  string         `json:"merchant_id"`
	Score       int            `json:"score"`
	HoldbackBps int            `json:"holdback_bps"`
	Mode        string         `json:"mode"`
	Components  map[string]int `json:"components"`
	ComputedAt  time.Time      `json:"computed_at"`
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Record(ctx context.Context, merchantID string, score Score) (bool, error) {
	latest, err := s.Latest(ctx, merchantID)
	if err != nil && !errors.Is(err, ErrNoScore) {
		return false, err
	}
	if latest != nil && latest.Score == score.Total && latest.HoldbackBps == score.HoldbackBps {
		return false, nil
	}

	components, err := json.Marshal(map[string]any{
		"factors": score.Components,
		"mode":    score.Mode,
	})
	if err != nil {
		return false, fmt.Errorf("risk: encode components: %w", err)
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO merchant_risk_scores (merchant_id, score, holdback_bps, components)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (merchant_id, computed_at) DO NOTHING`,
		merchantID, score.Total, score.HoldbackBps, components)
	if err != nil {
		return false, fmt.Errorf("risk: record score: %w", err)
	}
	return true, nil
}

var ErrNoScore = errors.New("risk: this merchant has never been scored")

func (s *Store) Latest(ctx context.Context, merchantID string) (*Snapshot, error) {
	var snap Snapshot
	var raw []byte

	err := s.pool.QueryRow(ctx,
		`SELECT merchant_id, score, holdback_bps, components, computed_at
		 FROM merchant_risk_scores WHERE merchant_id = $1
		 ORDER BY computed_at DESC LIMIT 1`,
		merchantID).Scan(&snap.MerchantID, &snap.Score, &snap.HoldbackBps,
		&raw, &snap.ComputedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoScore
	}
	if err != nil {
		return nil, fmt.Errorf("risk: latest score: %w", err)
	}

	decode(raw, &snap)
	snap.ComputedAt = snap.ComputedAt.UTC()
	return &snap, nil
}

func (s *Store) History(ctx context.Context, merchantID string, limit int) ([]Snapshot, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx,
		`SELECT merchant_id, score, holdback_bps, components, computed_at
		 FROM merchant_risk_scores WHERE merchant_id = $1
		 ORDER BY computed_at DESC LIMIT $2`, merchantID, limit)
	if err != nil {
		return nil, fmt.Errorf("risk: score history: %w", err)
	}
	defer rows.Close()

	var out []Snapshot
	for rows.Next() {
		var snap Snapshot
		var raw []byte
		if err := rows.Scan(&snap.MerchantID, &snap.Score, &snap.HoldbackBps,
			&raw, &snap.ComputedAt); err != nil {
			return nil, fmt.Errorf("risk: scan score: %w", err)
		}
		decode(raw, &snap)
		snap.ComputedAt = snap.ComputedAt.UTC()
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (s *Store) MustLatest(ctx context.Context, merchantID string) (*Snapshot, error) {
	snap, err := s.Latest(ctx, merchantID)
	if errors.Is(err, ErrNoScore) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Merchant %s has not been scored yet. Scores appear after the first payout.",
			merchantID)
	}
	return snap, err
}

func decode(raw []byte, snap *Snapshot) {
	var wrapper struct {
		Factors map[string]int `json:"factors"`
		Mode    string         `json:"mode"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil {
		snap.Components = wrapper.Factors
		snap.Mode = wrapper.Mode
	}
}

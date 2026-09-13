package velocity

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/id"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) SyncRules(ctx context.Context, rules []Rule) error {
	for _, r := range rules {
		_, err := s.pool.Exec(ctx,
			`INSERT INTO velocity_rules (code, description, window_sec, threshold, action, mode)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (code) DO UPDATE SET
			   description = EXCLUDED.description,
			   window_sec  = EXCLUDED.window_sec,
			   threshold   = EXCLUDED.threshold,
			   action      = EXCLUDED.action,
			   updated_at  = now()`,
			r.Code, r.Description, int(r.Window.Seconds()),
			clampThreshold(r.Threshold), string(r.Action), string(r.Mode))
		if err != nil {
			return fmt.Errorf("velocity: sync rule %s: %w", r.Code, err)
		}
	}
	return nil
}

func (s *Store) Load(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx, `SELECT code, mode FROM velocity_rules`)
	if err != nil {
		return nil, fmt.Errorf("velocity: load rules: %w", err)
	}
	defer rows.Close()

	modes := map[string]Mode{}
	for rows.Next() {
		var code, mode string
		if err := rows.Scan(&code, &mode); err != nil {
			return nil, fmt.Errorf("velocity: scan rule: %w", err)
		}
		modes[code] = Mode(mode)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rules := DefaultRules()
	for i := range rules {
		if mode, ok := modes[rules[i].Code]; ok {
			rules[i].Mode = mode
		}
	}
	return rules, nil
}

func (s *Store) RecordAlert(ctx context.Context, subj Subject, trip Trip) error {
	severity := severityFor(trip.Action)

	autoAction := string(trip.Action)
	if trip.Mode == ModeMonitor {
		autoAction = "none"
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO risk_alerts
		   (id, subject_type, subject_id, rule_code, detail, observed_minor, severity, auto_action)
		 VALUES ($1, 'user', $2, $3, $4, $5, $6, $7)`,
		id.New("alert"), subj.UserID, trip.Code,
		fmt.Sprintf("%s (observed %d, threshold %d)", trip.Detail, trip.Observed, trip.Threshold),
		int64(subj.Amount), severity, autoAction)
	if err != nil {
		return fmt.Errorf("velocity: record alert %s: %w", trip.Code, err)
	}
	return nil
}

func (s *Store) RecordDecision(ctx context.Context, subj Subject, d Decision) error {
	for _, trip := range d.Trips {
		if err := s.RecordAlert(ctx, subj, trip); err != nil {
			return err
		}
	}
	return nil
}

func severityFor(a Action) string {
	switch a {
	case ActionFreeze:
		return "high"
	case ActionHoldWithdrawal, ActionRequireOTP:
		return "medium"
	default:
		return "low"
	}
}

func clampThreshold(v int64) int {
	if v > 1_000_000_000 {
		return 1_000_000_000
	}
	if v < 1 {
		return 1
	}
	return int(v)
}

package console

import (
	"context"
	"time"
)

type Rule struct {
	Code        string    `json:"code"`
	Description string    `json:"description"`
	WindowSec   int       `json:"window_seconds"`
	Threshold   int       `json:"threshold"`
	Action      string    `json:"action"`
	Mode        string    `json:"mode"`
	UpdatedBy   string    `json:"updated_by,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Trips       int64     `json:"trips_7d"`
}

func (s *Store) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.code, r.description, r.window_sec, r.threshold, r.action, r.mode,
		        COALESCE(r.updated_by, ''), r.updated_at,
		        count(a.id) FILTER (WHERE a.created_at > now() - interval '7 days')
		 FROM velocity_rules r
		 LEFT JOIN risk_alerts a ON a.rule_code = r.code
		 GROUP BY r.code, r.description, r.window_sec, r.threshold, r.action, r.mode,
		          r.updated_by, r.updated_at
		 ORDER BY r.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Code, &r.Description, &r.WindowSec, &r.Threshold,
			&r.Action, &r.Mode, &r.UpdatedBy, &r.UpdatedAt, &r.Trips); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type Alert struct {
	ID          string     `json:"id"`
	SubjectType string     `json:"subject_type"`
	SubjectID   string     `json:"subject_id"`
	SubjectName string     `json:"subject_name,omitempty"`
	RuleCode    string     `json:"rule_code"`
	RuleText    string     `json:"rule"`
	Detail      string     `json:"detail"`
	Observed    *int64     `json:"observed,omitempty"`
	Severity    string     `json:"severity"`
	AutoAction  string     `json:"auto_action,omitempty"`
	ReviewedBy  string     `json:"reviewed_by,omitempty"`
	Outcome     *string    `json:"outcome,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
}

type AlertPage struct {
	Data    []Alert `json:"data"`
	Open    int64   `json:"open"`
	High    int64   `json:"high_severity"`
	Last24h int64   `json:"last_24h"`
}

func (s *Store) Alerts(ctx context.Context, severity string, limit int) (*AlertPage, error) {
	page := &AlertPage{Data: []Alert{}}

	rows, err := s.pool.Query(ctx,
		`SELECT a.id, a.subject_type, a.subject_id,
		        COALESCE(u.full_name, m.display_name, ''),
		        a.rule_code, r.description, a.detail, a.observed_minor, a.severity,
		        COALESCE(a.auto_action, ''), COALESCE(a.reviewed_by, ''), a.outcome,
		        a.created_at, a.reviewed_at
		 FROM risk_alerts a
		 JOIN velocity_rules r ON r.code = a.rule_code
		 LEFT JOIN users u ON a.subject_type = 'user' AND u.id = a.subject_id
		 LEFT JOIN merchants m ON a.subject_type = 'merchant' AND m.id = a.subject_id
		 WHERE ($1 = '' OR a.severity = $1)
		 ORDER BY a.created_at DESC
		 LIMIT $2`, severity, clamp(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.SubjectType, &a.SubjectID, &a.SubjectName,
			&a.RuleCode, &a.RuleText, &a.Detail, &a.Observed, &a.Severity,
			&a.AutoAction, &a.ReviewedBy, &a.Outcome, &a.CreatedAt, &a.ReviewedAt); err != nil {
			return nil, err
		}
		page.Data = append(page.Data, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE reviewed_at IS NULL),
		        count(*) FILTER (WHERE severity = 'high'),
		        count(*) FILTER (WHERE created_at > now() - interval '24 hours')
		 FROM risk_alerts`).Scan(&page.Open, &page.High, &page.Last24h)
	if err != nil {
		return nil, err
	}
	return page, nil
}

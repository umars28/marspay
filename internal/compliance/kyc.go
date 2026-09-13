package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/state"
)

const (
	TierUnverified = "unverified"
	TierVerified   = "verified"

	KYCSLA = 24 * time.Hour
)

var kycDecisions = map[string]bool{
	"approved":  true,
	"rejected":  true,
	"resubmit":  true,
	"escalated": true,
}

type Submission struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id"`
	TargetTier   string     `json:"target_tier"`
	MatchScore   *float64   `json:"match_score,omitempty"`
	Status       string     `json:"status"`
	ReviewedBy   string     `json:"reviewed_by,omitempty"`
	ReviewReason string     `json:"review_reason,omitempty"`
	SubmittedAt  time.Time  `json:"submitted_at"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	SLADueAt     time.Time  `json:"sla_due_at"`
	Overdue      bool       `json:"overdue"`
}

type KYC struct {
	pool  *pgxpool.Pool
	audit *Audit
	now   func() time.Time
}

func NewKYC(pool *pgxpool.Pool, audit *Audit) *KYC {
	return &KYC{pool: pool, audit: audit, now: time.Now}
}

type SubmitRequest struct {
	IDNumber   string   `json:"id_number"`
	MatchScore *float64 `json:"match_score,omitempty"`
}

func (k *KYC) Submit(ctx context.Context, userID string, req SubmitRequest) (*Submission, error) {
	if req.IDNumber == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field id_number is required.")
	}

	var tier string
	err := k.pool.QueryRow(ctx, `SELECT kyc_tier FROM users WHERE id = $1`, userID).Scan(&tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound, "Account not found.")
	}
	if err != nil {
		return nil, fmt.Errorf("compliance: load tier: %w", err)
	}
	if tier == TierVerified {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This account is already verified.")
	}

	var pending int
	if err := k.pool.QueryRow(ctx,
		`SELECT count(*) FROM kyc_submissions WHERE user_id = $1 AND status = 'pending'`,
		userID).Scan(&pending); err != nil {
		return nil, fmt.Errorf("compliance: count pending: %w", err)
	}
	if pending > 0 {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"A submission for this account is already under review.")
	}

	hash := sha256.Sum256([]byte(req.IDNumber))

	s := &Submission{
		ID:         id.New("kyc"),
		UserID:     userID,
		TargetTier: TierVerified,
		MatchScore: req.MatchScore,
		Status:     "pending",
	}

	err = k.pool.QueryRow(ctx,
		`INSERT INTO kyc_submissions
		   (id, user_id, target_tier, id_number_hash, match_score, status)
		 VALUES ($1, $2, $3, $4, $5, 'pending')
		 RETURNING submitted_at`,
		s.ID, userID, TierVerified, hex.EncodeToString(hash[:]), req.MatchScore).
		Scan(&s.SubmittedAt)
	if err != nil {
		return nil, fmt.Errorf("compliance: insert submission: %w", err)
	}

	s.SubmittedAt = s.SubmittedAt.UTC()
	s.SLADueAt = s.SubmittedAt.Add(KYCSLA)
	return s, nil
}

func (k *KYC) Queue(ctx context.Context, limit int) ([]Submission, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	rows, err := k.pool.Query(ctx,
		`SELECT id, user_id, target_tier, match_score, status, submitted_at
		 FROM kyc_submissions WHERE status = 'pending'
		 ORDER BY submitted_at LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("compliance: kyc queue: %w", err)
	}
	defer rows.Close()

	now := k.now().UTC()
	var out []Submission
	for rows.Next() {
		var s Submission
		if err := rows.Scan(&s.ID, &s.UserID, &s.TargetTier,
			&s.MatchScore, &s.Status, &s.SubmittedAt); err != nil {
			return nil, fmt.Errorf("compliance: scan submission: %w", err)
		}
		s.SubmittedAt = s.SubmittedAt.UTC()
		s.SLADueAt = s.SubmittedAt.Add(KYCSLA)
		s.Overdue = now.After(s.SLADueAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

type ReviewRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (k *KYC) Review(ctx context.Context, submissionID string, req ReviewRequest, actor string) (*Submission, error) {
	if actor == "" {
		return nil, ErrNoActor
	}
	if req.Reason == "" {
		return nil, ErrNoReason
	}
	if !kycDecisions[req.Decision] {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field decision must be approved, rejected, resubmit or escalated.")
	}

	var userID, status string
	err := k.pool.QueryRow(ctx,
		`SELECT user_id, status FROM kyc_submissions WHERE id = $1`,
		submissionID).Scan(&userID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Submission %s was not found.", submissionID)
	}
	if err != nil {
		return nil, fmt.Errorf("compliance: load submission: %w", err)
	}
	if status != "pending" {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Submission %s was already reviewed as %s.", submissionID, status)
	}

	tx, err := k.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("compliance: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`UPDATE kyc_submissions
		 SET status = $1, reviewed_by = $2, review_reason = $3, reviewed_at = now()
		 WHERE id = $4`,
		req.Decision, actor, req.Reason, submissionID)
	if err != nil {
		return nil, fmt.Errorf("compliance: review submission: %w", err)
	}

	if err := state.RecordTx(ctx, tx, state.KindKYC, submissionID,
		"pending", req.Decision, actor, req.Reason); err != nil {
		return nil, err
	}

	if req.Decision == "approved" {
		if _, err := tx.Exec(ctx,
			`UPDATE users SET kyc_tier = $1, updated_at = now() WHERE id = $2`,
			TierVerified, userID); err != nil {
			return nil, fmt.Errorf("compliance: upgrade tier: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("compliance: commit: %w", err)
	}

	if k.audit != nil {
		before := map[string]any{"status": "pending"}
		after := map[string]any{"status": req.Decision}
		if req.Decision == "approved" {
			after["kyc_tier"] = TierVerified
		}
		if err := k.audit.Record(ctx, Entry{
			Actor: actor, Action: "review_kyc",
			ObjectType: "kyc_submission", ObjectID: submissionID,
			Before: before, After: after, Reason: req.Reason,
		}); err != nil {
			return nil, err
		}
	}

	return k.load(ctx, submissionID)
}

func (k *KYC) load(ctx context.Context, submissionID string) (*Submission, error) {
	var s Submission
	var reviewedBy, reason *string

	err := k.pool.QueryRow(ctx,
		`SELECT id, user_id, target_tier, match_score, status,
		        reviewed_by, review_reason, submitted_at, reviewed_at
		 FROM kyc_submissions WHERE id = $1`,
		submissionID).Scan(&s.ID, &s.UserID, &s.TargetTier, &s.MatchScore,
		&s.Status, &reviewedBy, &reason, &s.SubmittedAt, &s.ReviewedAt)
	if err != nil {
		return nil, fmt.Errorf("compliance: load submission: %w", err)
	}

	if reviewedBy != nil {
		s.ReviewedBy = *reviewedBy
	}
	if reason != nil {
		s.ReviewReason = *reason
	}
	s.SubmittedAt = s.SubmittedAt.UTC()
	s.SLADueAt = s.SubmittedAt.Add(KYCSLA)
	return &s, nil
}

package loyalty

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
)

const DefaultRequestTTL = 3 * 24 * time.Hour

type MoneyRequest struct {
	ID            string     `json:"id"`
	RequesterID   string     `json:"requester_id"`
	RequesterName string     `json:"requester_name,omitempty"`
	PayerID       string     `json:"payer_id"`
	SplitID       string     `json:"split_id,omitempty"`
	Amount        int64      `json:"amount"`
	Currency      string     `json:"currency"`
	Note          string     `json:"note,omitempty"`
	Status        string     `json:"status"`
	TransferID    string     `json:"transfer_id,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Split struct {
	ID           string         `json:"id"`
	OwnerID      string         `json:"owner_id"`
	Title        string         `json:"title"`
	TotalMinor   int64          `json:"total"`
	Participants int            `json:"participants"`
	ShareMinor   int64          `json:"share_each"`
	Currency     string         `json:"currency"`
	Requests     []MoneyRequest `json:"requests"`
	CreatedAt    time.Time      `json:"created_at"`
}

type CreateRequestInput struct {
	PayerID  string `json:"payer_id"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Note     string `json:"note,omitempty"`
}

func (s *Service) RequestMoney(ctx context.Context, requesterID string, in CreateRequestInput) (*MoneyRequest, error) {
	if in.Amount <= 0 {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field amount must be positive.")
	}
	if in.Currency != "IDR" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field currency must be IDR.")
	}

	payerID, err := s.resolveUser(ctx, in.PayerID)
	if err != nil {
		return nil, err
	}
	if payerID == requesterID {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"You cannot request money from yourself.")
	}

	expires := s.now().Add(DefaultRequestTTL)
	result := &MoneyRequest{
		ID:          id.New("mrq"),
		RequesterID: requesterID,
		PayerID:     payerID,
		Amount:      in.Amount,
		Currency:    "IDR",
		Note:        in.Note,
		Status:      "pending",
		ExpiresAt:   &expires,
	}

	err = s.pool.QueryRow(ctx,
		`INSERT INTO money_requests
		   (id, requester_id, payer_id, amount_minor, note, status, expires_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), 'pending', $6)
		 RETURNING created_at`,
		result.ID, requesterID, payerID, in.Amount, in.Note, expires).Scan(&result.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("loyalty: insert money request: %w", err)
	}
	result.CreatedAt = result.CreatedAt.UTC()
	return result, nil
}

func (s *Service) Incoming(ctx context.Context, payerID string) ([]MoneyRequest, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.requester_id, COALESCE(u.full_name, ''), r.payer_id,
		        COALESCE(r.split_id, ''), r.amount_minor, COALESCE(r.note, ''),
		        r.status, COALESCE(r.transfer_id, ''), r.expires_at, r.created_at
		 FROM money_requests r
		 LEFT JOIN users u ON u.id = r.requester_id
		 WHERE r.payer_id = $1 AND r.status = 'pending'
		 ORDER BY r.created_at DESC`, payerID)
	if err != nil {
		return nil, fmt.Errorf("loyalty: incoming requests: %w", err)
	}
	defer rows.Close()

	var out []MoneyRequest
	for rows.Next() {
		var v MoneyRequest
		if err := rows.Scan(&v.ID, &v.RequesterID, &v.RequesterName, &v.PayerID,
			&v.SplitID, &v.Amount, &v.Note, &v.Status, &v.TransferID,
			&v.ExpiresAt, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("loyalty: scan request: %w", err)
		}
		v.Currency = "IDR"
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) Decline(ctx context.Context, payerID, requestID string) (*MoneyRequest, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE money_requests SET status = 'declined'
		 WHERE id = $1 AND payer_id = $2 AND status = 'pending'`,
		requestID, payerID)
	if err != nil {
		return nil, fmt.Errorf("loyalty: decline request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Request %s is not waiting for you.", requestID)
	}
	return s.loadRequest(ctx, requestID)
}

func (s *Service) MarkPaid(ctx context.Context, payerID, requestID, transferID string) (*MoneyRequest, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE money_requests SET status = 'paid', transfer_id = $1
		 WHERE id = $2 AND payer_id = $3 AND status = 'pending'`,
		transferID, requestID, payerID)
	if err != nil {
		return nil, fmt.Errorf("loyalty: mark request paid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Request %s is not waiting for you.", requestID)
	}
	return s.loadRequest(ctx, requestID)
}

func (s *Service) ExpireStaleRequests(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE money_requests SET status = 'expired'
		 WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("loyalty: expire requests: %w", err)
	}
	return tag.RowsAffected(), nil
}

type CreateSplitInput struct {
	Title    string   `json:"title"`
	Total    int64    `json:"total"`
	Currency string   `json:"currency"`
	Payers   []string `json:"payers"`
}

func (s *Service) CreateSplit(ctx context.Context, ownerID string, in CreateSplitInput) (*Split, error) {
	if in.Title == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field title is required.")
	}
	if in.Total <= 0 {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field total must be positive.")
	}
	if in.Currency != "IDR" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field currency must be IDR.")
	}
	if len(in.Payers) == 0 {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"A split needs at least one other person.")
	}

	participants := len(in.Payers) + 1
	share := in.Total / int64(participants)
	if share <= 0 {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"The total is too small to split between %d people.", participants)
	}

	split := &Split{
		ID:           id.New("spl"),
		OwnerID:      ownerID,
		Title:        in.Title,
		TotalMinor:   in.Total,
		Participants: participants,
		ShareMinor:   share,
		Currency:     "IDR",
	}

	err := s.pool.QueryRow(ctx,
		`INSERT INTO bill_splits (id, owner_id, title, total_minor, participants)
		 VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		split.ID, ownerID, in.Title, in.Total, participants).Scan(&split.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("loyalty: insert split: %w", err)
	}
	split.CreatedAt = split.CreatedAt.UTC()

	for _, payer := range in.Payers {
		req, err := s.RequestMoney(ctx, ownerID, CreateRequestInput{
			PayerID: payer, Amount: share, Currency: "IDR", Note: in.Title,
		})
		if err != nil {
			return nil, err
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE money_requests SET split_id = $1 WHERE id = $2`,
			split.ID, req.ID); err != nil {
			return nil, fmt.Errorf("loyalty: attach request to split: %w", err)
		}
		req.SplitID = split.ID
		split.Requests = append(split.Requests, *req)
	}

	return split, nil
}

func (s *Service) loadRequest(ctx context.Context, requestID string) (*MoneyRequest, error) {
	var v MoneyRequest
	err := s.pool.QueryRow(ctx,
		`SELECT id, requester_id, payer_id, COALESCE(split_id, ''), amount_minor,
		        COALESCE(note, ''), status, COALESCE(transfer_id, ''), expires_at, created_at
		 FROM money_requests WHERE id = $1`, requestID).
		Scan(&v.ID, &v.RequesterID, &v.PayerID, &v.SplitID, &v.Amount,
			&v.Note, &v.Status, &v.TransferID, &v.ExpiresAt, &v.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("loyalty: load request: %w", err)
	}
	v.Currency = "IDR"
	v.CreatedAt = v.CreatedAt.UTC()
	return &v, nil
}

func (s *Service) resolveUser(ctx context.Context, ref string) (string, error) {
	var userID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM users WHERE id = $1 OR phone = $1`, ref).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No Marspay account matches %q.", ref)
	}
	if err != nil {
		return "", fmt.Errorf("loyalty: resolve user: %w", err)
	}
	return userID, nil
}

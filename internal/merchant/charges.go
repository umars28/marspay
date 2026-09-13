package merchant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/state"
)

const (
	MinChargeWindow = 5 * time.Minute
	MaxChargeWindow = 30 * 24 * time.Hour
)

type Charge struct {
	ID          string     `json:"id"`
	MerchantID  string     `json:"merchant_id"`
	OutletID    string     `json:"outlet_id,omitempty"`
	Reference   string     `json:"reference"`
	Description string     `json:"description"`
	Amount      int64      `json:"amount"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	PaymentID   string     `json:"payment_id,omitempty"`
	PaidBy      string     `json:"paid_by,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Expired     bool       `json:"expired"`
}

type Charges struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewCharges(pool *pgxpool.Pool) *Charges {
	return &Charges{pool: pool, now: time.Now}
}

type CreateChargeRequest struct {
	Reference   string `json:"reference"`
	Description string `json:"description"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	OutletID    string `json:"outlet_id,omitempty"`
	ExpiresIn   string `json:"expires_in,omitempty"`
}

func (c *Charges) Create(ctx context.Context, merchantID string, req CreateChargeRequest) (*Charge, error) {
	req.Reference = strings.TrimSpace(req.Reference)
	req.Description = strings.TrimSpace(req.Description)

	switch {
	case req.Description == "":
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field description is required so the payer knows what they are paying for.")
	case req.Amount <= 0:
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field amount must be a positive integer in minor units.")
	case req.Currency != "" && req.Currency != "IDR":
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Only IDR is supported.")
	}

	window := 24 * time.Hour
	if req.ExpiresIn != "" {
		parsed, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
				"Field expires_in must be a duration such as 2h or 30m.")
		}
		window = parsed
	}
	if window < MinChargeWindow || window > MaxChargeWindow {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"A payment link must live between %s and %s.", MinChargeWindow, MaxChargeWindow)
	}

	if req.Reference == "" {
		req.Reference = id.New("ref")
	}

	charge := &Charge{
		ID:          id.New("chg"),
		MerchantID:  merchantID,
		OutletID:    req.OutletID,
		Reference:   req.Reference,
		Description: req.Description,
		Amount:      req.Amount,
		Currency:    "IDR",
		Status:      "open",
		ExpiresAt:   c.now().Add(window),
		CreatedAt:   c.now(),
	}

	_, err := c.pool.Exec(ctx,
		`INSERT INTO charges (id, merchant_id, outlet_id, reference, description,
		                      amount_minor, status, expires_at)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, 'open', $7)`,
		charge.ID, merchantID, req.OutletID, charge.Reference, charge.Description,
		charge.Amount, charge.ExpiresAt)
	if isUnique(err) {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Reference %q is already in use by another link.", req.Reference)
	}
	if err != nil {
		return nil, err
	}
	return charge, nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

var ErrChargeNotFound = errors.New("merchant: no such charge")

func (c *Charges) Get(ctx context.Context, merchantID, chargeID string) (*Charge, error) {
	var ch Charge
	err := c.pool.QueryRow(ctx,
		`SELECT id, merchant_id, COALESCE(outlet_id, ''), reference, description,
		        amount_minor, currency, status, COALESCE(payment_id, ''), COALESCE(paid_by, ''),
		        expires_at, paid_at, created_at
		 FROM charges
		 WHERE id = $1 AND ($2 = '' OR merchant_id = $2)`, chargeID, merchantID).
		Scan(&ch.ID, &ch.MerchantID, &ch.OutletID, &ch.Reference, &ch.Description,
			&ch.Amount, &ch.Currency, &ch.Status, &ch.PaymentID, &ch.PaidBy,
			&ch.ExpiresAt, &ch.PaidAt, &ch.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrChargeNotFound
	}
	if err != nil {
		return nil, err
	}

	ch.Expired = ch.Status == "open" && c.now().After(ch.ExpiresAt)
	return &ch, nil
}

func (c *Charges) List(ctx context.Context, merchantID, status string, limit int) ([]Charge, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	rows, err := c.pool.Query(ctx,
		`SELECT id, merchant_id, COALESCE(outlet_id, ''), reference, description,
		        amount_minor, currency, status, COALESCE(payment_id, ''), COALESCE(paid_by, ''),
		        expires_at, paid_at, created_at
		 FROM charges
		 WHERE merchant_id = $1 AND ($2 = '' OR status = $2)
		 ORDER BY created_at DESC
		 LIMIT $3`, merchantID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Charge{}
	for rows.Next() {
		var ch Charge
		if err := rows.Scan(&ch.ID, &ch.MerchantID, &ch.OutletID, &ch.Reference, &ch.Description,
			&ch.Amount, &ch.Currency, &ch.Status, &ch.PaymentID, &ch.PaidBy,
			&ch.ExpiresAt, &ch.PaidAt, &ch.CreatedAt); err != nil {
			return nil, err
		}
		ch.Expired = ch.Status == "open" && c.now().After(ch.ExpiresAt)
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (c *Charges) Cancel(ctx context.Context, merchantID, chargeID string) (*Charge, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE charges SET status = 'cancelled', cancelled_at = now()
		 WHERE id = $1 AND merchant_id = $2 AND status = 'open'`, chargeID, merchantID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		if err := state.RecordTx(ctx, tx, state.KindCharge, chargeID,
			"open", "cancelled", merchantID, "cancelled by the merchant"); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return c.Get(ctx, merchantID, chargeID)
	}
	if tag.RowsAffected() == 0 {
		existing, err := c.Get(ctx, merchantID, chargeID)
		if err != nil {
			return nil, err
		}
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This link is %s and can no longer be cancelled.", existing.Status)
	}
	return c.Get(ctx, merchantID, chargeID)
}

func (c *Charges) MarkPaid(ctx context.Context, tx pgx.Tx, chargeID, paymentID, payerID string) error {
	var status string
	var expiresAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT status, expires_at FROM charges WHERE id = $1 FOR UPDATE`, chargeID).
		Scan(&status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrChargeNotFound
	}
	if err != nil {
		return err
	}

	switch {
	case status == "paid":
		return httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link has already been paid.")
	case status != "open":
		return httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link is %s.", status)
	case c.now().After(expiresAt):
		return httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link expired at %s.", expiresAt.Format(time.RFC3339))
	}

	if _, err := tx.Exec(ctx,
		`UPDATE charges SET status = 'paid', payment_id = $2, paid_by = $3, paid_at = now()
		 WHERE id = $1`, chargeID, paymentID, payerID); err != nil {
		return err
	}
	return state.RecordTx(ctx, tx, state.KindCharge, chargeID,
		"open", "paid", payerID, paymentID)
}

func (c *Charges) Lookup(ctx context.Context, chargeID string) (string, string, int64, error) {
	ch, err := c.Get(ctx, "", chargeID)
	if errors.Is(err, ErrChargeNotFound) {
		return "", "", 0, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Payment link %s was not found.", chargeID)
	}
	if err != nil {
		return "", "", 0, err
	}

	switch {
	case ch.Status == "paid":
		return "", "", 0, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link has already been paid.")
	case ch.Status != "open":
		return "", "", 0, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link is %s.", ch.Status)
	case ch.Expired:
		return "", "", 0, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"This payment link expired at %s.", ch.ExpiresAt.Format(time.RFC3339))
	}
	return ch.MerchantID, ch.OutletID, ch.Amount, nil
}

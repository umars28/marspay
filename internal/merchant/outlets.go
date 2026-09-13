package merchant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/id"
)

var staffRoles = map[string]bool{
	"owner":      true,
	"supervisor": true,
	"cashier":    true,
}

type Outlet struct {
	ID         string    `json:"id"`
	MerchantID string    `json:"merchant_id"`
	Name       string    `json:"name"`
	NMID       string    `json:"nmid"`
	Address    string    `json:"address,omitempty"`
	Status     string    `json:"status"`
	StaffCount int       `json:"staff_count"`
	CreatedAt  time.Time `json:"created_at"`
}

type Staff struct {
	ID         string     `json:"id"`
	MerchantID string     `json:"merchant_id"`
	OutletID   string     `json:"outlet_id,omitempty"`
	FullName   string     `json:"full_name"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Outlets struct {
	pool  *pgxpool.Pool
	audit *compliance.Audit
}

func NewOutlets(pool *pgxpool.Pool, audit *compliance.Audit) *Outlets {
	return &Outlets{pool: pool, audit: audit}
}

type CreateOutletRequest struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
}

func (o *Outlets) Create(ctx context.Context, merchantID string, req CreateOutletRequest) (*Outlet, error) {
	if req.Name == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field name is required.")
	}

	outlet := &Outlet{
		ID:         id.New("out"),
		MerchantID: merchantID,
		Name:       req.Name,
		NMID:       "ID" + id.Secret(12),
		Address:    req.Address,
		Status:     "active",
	}

	err := o.pool.QueryRow(ctx,
		`INSERT INTO outlets (id, merchant_id, name, nmid, address, status)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), 'active')
		 RETURNING created_at`,
		outlet.ID, merchantID, req.Name, outlet.NMID, req.Address).Scan(&outlet.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("merchant: insert outlet: %w", err)
	}
	outlet.CreatedAt = outlet.CreatedAt.UTC()
	return outlet, nil
}

func (o *Outlets) List(ctx context.Context, merchantID string) ([]Outlet, error) {
	rows, err := o.pool.Query(ctx,
		`SELECT o.id, o.merchant_id, o.name, COALESCE(o.nmid, ''), COALESCE(o.address, ''),
		        o.status, o.created_at,
		        (SELECT count(*) FROM merchant_users mu
		          WHERE mu.outlet_id = o.id AND mu.revoked_at IS NULL)
		 FROM outlets o WHERE o.merchant_id = $1 ORDER BY o.created_at`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant: list outlets: %w", err)
	}
	defer rows.Close()

	var out []Outlet
	for rows.Next() {
		var v Outlet
		if err := rows.Scan(&v.ID, &v.MerchantID, &v.Name, &v.NMID,
			&v.Address, &v.Status, &v.CreatedAt, &v.StaffCount); err != nil {
			return nil, fmt.Errorf("merchant: scan outlet: %w", err)
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

type AddStaffRequest struct {
	OutletID string `json:"outlet_id,omitempty"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

func (o *Outlets) AddStaff(ctx context.Context, merchantID string, req AddStaffRequest) (*Staff, error) {
	if req.FullName == "" || req.Email == "" {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Fields full_name and email are required.")
	}
	if !staffRoles[req.Role] {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field role must be owner, supervisor or cashier.")
	}

	if req.OutletID != "" {
		var owner string
		err := o.pool.QueryRow(ctx,
			`SELECT merchant_id FROM outlets WHERE id = $1`, req.OutletID).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != merchantID) {
			return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
				"Outlet %s was not found.", req.OutletID)
		}
		if err != nil {
			return nil, fmt.Errorf("merchant: load outlet: %w", err)
		}
	}

	staff := &Staff{
		ID:         id.New("mu"),
		MerchantID: merchantID,
		OutletID:   req.OutletID,
		FullName:   req.FullName,
		Email:      req.Email,
		Role:       req.Role,
	}

	err := o.pool.QueryRow(ctx,
		`INSERT INTO merchant_users (id, merchant_id, outlet_id, full_name, email, role)
		 VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)
		 RETURNING created_at`,
		staff.ID, merchantID, req.OutletID, req.FullName, req.Email, req.Role).
		Scan(&staff.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("merchant: insert staff: %w", err)
	}
	staff.CreatedAt = staff.CreatedAt.UTC()
	return staff, nil
}

func (o *Outlets) ListStaff(ctx context.Context, merchantID string) ([]Staff, error) {
	rows, err := o.pool.Query(ctx,
		`SELECT id, merchant_id, COALESCE(outlet_id, ''), full_name, email, role,
		        last_seen_at, revoked_at, created_at
		 FROM merchant_users WHERE merchant_id = $1 ORDER BY created_at`, merchantID)
	if err != nil {
		return nil, fmt.Errorf("merchant: list staff: %w", err)
	}
	defer rows.Close()

	var out []Staff
	for rows.Next() {
		var v Staff
		if err := rows.Scan(&v.ID, &v.MerchantID, &v.OutletID, &v.FullName,
			&v.Email, &v.Role, &v.LastSeenAt, &v.RevokedAt, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("merchant: scan staff: %w", err)
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (o *Outlets) RevokeStaff(ctx context.Context, merchantID, staffID, actor, reason string) error {
	if actor == "" {
		return compliance.ErrNoActor
	}
	if reason == "" {
		return compliance.ErrNoReason
	}

	tag, err := o.pool.Exec(ctx,
		`UPDATE merchant_users SET revoked_at = now()
		 WHERE id = $1 AND merchant_id = $2 AND revoked_at IS NULL`,
		staffID, merchantID)
	if err != nil {
		return fmt.Errorf("merchant: revoke staff: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Staff member %s was not found or already revoked.", staffID)
	}

	if o.audit != nil {
		return o.audit.Record(ctx, compliance.Entry{
			Actor: actor, Action: "revoke_staff",
			ObjectType: "merchant", ObjectID: merchantID,
			After:  map[string]any{"staff_id": staffID, "status": "revoked"},
			Reason: reason,
		})
	}
	return nil
}

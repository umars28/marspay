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
	"github.com/umars28/marspay/internal/money"
)

type Promo struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	ValueBps        *int      `json:"value_bps,omitempty"`
	ValueMinor      *int64    `json:"value,omitempty"`
	MaxBenefitMinor *int64    `json:"max_benefit,omitempty"`
	MinSpendMinor   int64     `json:"min_spend"`
	TotalQuota      *int      `json:"total_quota,omitempty"`
	RemainingQuota  int64     `json:"remaining_quota"`
	PerUserQuota    int       `json:"per_user_quota"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	Status          string    `json:"status"`
	Currency        string    `json:"currency"`
}

type Benefit struct {
	PromoID    string `json:"promo_id"`
	PromoCode  string `json:"promo_code"`
	Kind       string `json:"kind"`
	ValueMinor int64  `json:"value"`
	Points     int64  `json:"points"`
}

func quotaKey(promoID string) string {
	return "promo:" + promoID
}

func (s *Service) Promos(ctx context.Context) ([]Promo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, code, name, kind, value_bps, value_minor, max_benefit_minor,
		        min_spend_minor, total_quota, per_user_quota, starts_at, ends_at, status
		 FROM promos WHERE status = 'active' AND now() BETWEEN starts_at AND ends_at
		 ORDER BY ends_at`)
	if err != nil {
		return nil, fmt.Errorf("loyalty: list promos: %w", err)
	}
	defer rows.Close()

	var out []Promo
	for rows.Next() {
		var p Promo
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Kind, &p.ValueBps,
			&p.ValueMinor, &p.MaxBenefitMinor, &p.MinSpendMinor, &p.TotalQuota,
			&p.PerUserQuota, &p.StartsAt, &p.EndsAt, &p.Status); err != nil {
			return nil, fmt.Errorf("loyalty: scan promo: %w", err)
		}
		p.Currency = "IDR"
		p.StartsAt = p.StartsAt.UTC()
		p.EndsAt = p.EndsAt.UTC()

		if s.quota != nil && p.TotalQuota != nil {
			left, err := s.quota.Remaining(ctx, quotaKey(p.ID))
			if err != nil {
				return nil, err
			}
			p.RemainingQuota = left
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) SeedQuota(ctx context.Context, promoID string, total int64, until time.Time) error {
	if s.quota == nil {
		return nil
	}
	ttl := time.Until(until)
	if ttl <= 0 {
		ttl = time.Hour
	}
	return s.quota.Seed(ctx, quotaKey(promoID), total, ttl)
}

func (s *Service) Apply(ctx context.Context, userID, promoCode, paymentID string, spend money.Minor) (*Benefit, error) {
	if promoCode == "" {
		return nil, nil
	}

	var p Promo
	err := s.pool.QueryRow(ctx,
		`SELECT id, code, name, kind, value_bps, value_minor, max_benefit_minor,
		        min_spend_minor, total_quota, per_user_quota, starts_at, ends_at, status
		 FROM promos WHERE code = $1`, promoCode).
		Scan(&p.ID, &p.Code, &p.Name, &p.Kind, &p.ValueBps, &p.ValueMinor,
			&p.MaxBenefitMinor, &p.MinSpendMinor, &p.TotalQuota, &p.PerUserQuota,
			&p.StartsAt, &p.EndsAt, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"Offer %s was not found.", promoCode)
	}
	if err != nil {
		return nil, fmt.Errorf("loyalty: load promo: %w", err)
	}

	now := s.now()
	if p.Status != "active" || now.Before(p.StartsAt) || now.After(p.EndsAt) {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"Offer %s is not running right now.", promoCode)
	}
	if int64(spend) < p.MinSpendMinor {
		return nil, httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"This offer needs a minimum spend.").
			WithDetails(map[string]any{"min_spend": p.MinSpendMinor, "spend": int64(spend)})
	}

	var used int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM promo_redemptions WHERE promo_id = $1 AND user_id = $2`,
		p.ID, userID).Scan(&used); err != nil {
		return nil, fmt.Errorf("loyalty: count redemptions: %w", err)
	}
	if used >= p.PerUserQuota {
		return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
			"You have already used this offer.")
	}

	if s.quota != nil && p.TotalQuota != nil {
		taken, err := s.quota.Take(ctx, quotaKey(p.ID), time.Until(p.EndsAt))
		if err != nil {
			return nil, err
		}
		if !taken {
			return nil, httpx.Errorf(http.StatusConflict, httpx.TypeConflict,
				"This offer has run out.")
		}
	}

	benefit := s.benefitFor(p, spend)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("loyalty: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx,
		`INSERT INTO promo_redemptions (id, promo_id, user_id, payment_id, benefit_minor)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5)`,
		id.New("prd"), p.ID, userID, paymentID, benefit.ValueMinor)
	if err != nil {
		return nil, fmt.Errorf("loyalty: record redemption: %w", err)
	}

	if benefit.Points > 0 {
		accountID, err := s.account(ctx, userID)
		if err != nil {
			return nil, err
		}
		if err := s.award(ctx, tx, accountID, benefit.Points, "promo", p.ID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("loyalty: commit: %w", err)
	}

	return &benefit, nil
}

func (s *Service) benefitFor(p Promo, spend money.Minor) Benefit {
	b := Benefit{PromoID: p.ID, PromoCode: p.Code, Kind: p.Kind}

	switch p.Kind {
	case "cashback":
		if p.ValueBps != nil {
			value := int64(spend) * int64(*p.ValueBps) / 10_000
			if p.MaxBenefitMinor != nil && value > *p.MaxBenefitMinor {
				value = *p.MaxBenefitMinor
			}
			b.ValueMinor = value
			b.Points = value / money.MinorPerRupiah
		}
	case "discount":
		if p.ValueMinor != nil {
			b.ValueMinor = *p.ValueMinor
		}
	case "point_multiplier":
		base := int64(spend) / money.MinorPerRupiah / 100
		multiplier := int64(2)
		if p.ValueBps != nil {
			multiplier = int64(*p.ValueBps) / 10_000
		}
		if multiplier < 1 {
			multiplier = 1
		}
		b.Points = base * multiplier
	}

	return b
}

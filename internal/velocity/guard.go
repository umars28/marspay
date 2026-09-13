package velocity

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/umars28/marspay/internal/httpx"
)

const (
	TypeVelocityBlocked = "velocity_blocked"
	TypeStepUpRequired  = "step_up_required"
)

type Guard struct {
	engine *Engine
	store  *Store
}

func NewGuard(engine *Engine, store *Store) *Guard {
	return &Guard{engine: engine, store: store}
}

func (g *Guard) Check(ctx context.Context, subj Subject) error {
	if g == nil || g.engine == nil {
		return nil
	}

	decision, err := g.engine.Evaluate(ctx, subj)
	if err != nil {
		return fmt.Errorf("velocity: hot path check failed, refusing to proceed blind: %w", err)
	}

	if g.store != nil && len(decision.Trips) > 0 {
		if recErr := g.store.RecordDecision(ctx, subj, decision); recErr != nil {
			slog.ErrorContext(ctx, "could not record a velocity alert",
				"user", subj.UserID, "error", recErr)
		}
	}

	switch decision.Action {
	case ActionFreeze:
		return httpx.Errorf(http.StatusForbidden, TypeVelocityBlocked,
			"This transaction was blocked by a risk rule and the account is under review.").
			WithDetails(details(decision))
	case ActionHoldWithdrawal:
		return httpx.Errorf(http.StatusForbidden, TypeVelocityBlocked,
			"Withdrawals are held for a short period after a top up.").
			WithDetails(details(decision))
	case ActionRequireOTP:
		return httpx.Errorf(http.StatusForbidden, TypeStepUpRequired,
			"Confirm this transaction with a one-time password.").
			WithDetails(details(decision))
	default:
		return nil
	}
}

func details(d Decision) map[string]any {
	codes := make([]string, 0, len(d.Trips))
	for _, t := range d.Trips {
		if t.Mode == ModeActive {
			codes = append(codes, t.Code)
		}
	}
	return map[string]any{"rules": codes}
}

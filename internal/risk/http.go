package risk

import (
	"net/http"
	"strconv"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
)

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

type Config struct {
	MerchantID  string         `json:"merchant_id"`
	Score       int            `json:"score"`
	HoldbackBps int            `json:"holdback_bps"`
	Mode        string         `json:"payout_mode"`
	Components  map[string]int `json:"components"`
	Floor       int            `json:"holdback_floor_bps"`
	Ceiling     int            `json:"holdback_ceiling_bps"`
	Formula     string         `json:"formula"`
	ComputedAt  string         `json:"computed_at"`
}

func (h *Handler) PayoutConfig(w http.ResponseWriter, r *http.Request) {
	merchantID := auth.MerchantID(r.Context())
	if merchantID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A merchant API key is required."))
		return
	}

	snap, err := h.store.MustLatest(r.Context(), merchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, Config{
		MerchantID:  snap.MerchantID,
		Score:       snap.Score,
		HoldbackBps: snap.HoldbackBps,
		Mode:        snap.Mode,
		Components:  snap.Components,
		Floor:       MinHoldbackBps,
		Ceiling:     MaxHoldbackBps,
		Formula:     "holdback = clamp(1.5%, 45%, expected_loss * 200)",
		ComputedAt:  snap.ComputedAt.Format("2006-01-02T15:04:05Z"),
	})
}

func (h *Handler) MerchantScore(w http.ResponseWriter, r *http.Request) {
	if auth.UserID(r.Context()) == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An operator token is required."))
		return
	}
	if !auth.IsOperator(r.Context()) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusForbidden,
			httpx.TypeForbidden, "This endpoint is for operators."))
		return
	}

	merchantID := r.PathValue("id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	latest, err := h.store.MustLatest(r.Context(), merchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	history, err := h.store.History(r.Context(), merchantID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"current": latest,
		"history": history,
	})
}

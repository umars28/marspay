package loyalty

import (
	"net/http"
	"strconv"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/money"
)

func minor(v int64) money.Minor {
	return money.Minor(v)
}

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) Points(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	balance, err := h.service.Balance(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	history, err := h.service.History(r.Context(), userID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"balance": balance,
		"history": history,
	})
}

func (h *Handler) Redeem(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req RedeemRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.Redeem(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) Promos(w http.ResponseWriter, r *http.Request) {
	promos, err := h.service.Promos(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": promos})
}

type ApplyRequest struct {
	Code      string `json:"code"`
	PaymentID string `json:"payment_id,omitempty"`
	Spend     int64  `json:"spend"`
}

func (h *Handler) ApplyPromo(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req ApplyRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	benefit, err := h.service.Apply(r.Context(), userID, req.Code, req.PaymentID, minor(req.Spend))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, benefit)
}

func (h *Handler) RequestMoney(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req CreateRequestInput
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.RequestMoney(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) Incoming(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	requests, err := h.service.Incoming(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": requests})
}

func (h *Handler) Decline(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	result, err := h.service.Decline(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) CreateSplit(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req CreateSplitInput
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.CreateSplit(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := auth.UserID(r.Context())
	if userID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A valid access token is required."))
		return "", false
	}
	return userID, true
}

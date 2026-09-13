package txn

import (
	"net/http"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
)

type Handler struct {
	service *Service
}

func NewHandler(s *Service) *Handler {
	return &Handler{service: s}
}

func (h *Handler) CreateTransfer(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req TransferRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.CreateTransfer(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) CreateWithdrawal(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req WithdrawalRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.CreateWithdrawal(r.Context(), userID, req)
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

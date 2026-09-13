package payment

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

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	if userID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A valid access token is required."))
		return
	}

	var req CreateRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	payment, err := h.service.Create(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, payment)
}

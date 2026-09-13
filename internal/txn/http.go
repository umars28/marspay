package txn

import (
	"net/http"
	"strconv"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
	"github.com/umars28/marspay/internal/money"
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

func (h *Handler) CreateTopup(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req TopupRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.CreateTopup(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) ListBillers(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": Billers()})
}

func (h *Handler) Inquire(w http.ResponseWriter, r *http.Request) {
	if _, ok := caller(w, r); !ok {
		return
	}

	var req InquiryRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.Inquire(r.Context(), r.PathValue("code"), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) PayBill(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	var req BillPaymentRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.PayBill(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

type CallbackRequest struct {
	ExternalRef string `json:"external_ref"`
	TopupID     string `json:"topup_id"`
	Amount      int64  `json:"amount"`
	Outcome     string `json:"outcome"`
}

func (h *Handler) ProviderCallback(w http.ResponseWriter, r *http.Request) {
	var req CallbackRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.ExternalRef == "" || req.TopupID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
			httpx.TypeInvalidRequest, "Fields external_ref and topup_id are required."))
		return
	}

	result, err := h.service.ConfirmTopup(r.Context(), ProviderCallback{
		ProviderCode: r.PathValue("provider"),
		ExternalRef:  req.ExternalRef,
		TopupID:      req.TopupID,
		Amount:       money.Minor(req.Amount),
		Outcome:      req.Outcome,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) CreateRefund(w http.ResponseWriter, r *http.Request) {
	if _, ok := caller(w, r); !ok {
		return
	}

	var req RefundRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.service.CreateRefund(r.Context(), req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) Balance(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	result, err := h.service.Balance(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	result, err := h.service.History(r.Context(), userID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) Profile(w http.ResponseWriter, r *http.Request) {
	userID, ok := caller(w, r)
	if !ok {
		return
	}

	result, err := h.service.Profile(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
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

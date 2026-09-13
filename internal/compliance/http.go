package compliance

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
)

type Handler struct {
	blocks   *Blocks
	audit    *Audit
	kyc      *KYC
	disputes *Disputes
}

func NewHandler(blocks *Blocks, audit *Audit, kyc *KYC, disputes *Disputes) *Handler {
	return &Handler{blocks: blocks, audit: audit, kyc: kyc, disputes: disputes}
}

func (h *Handler) SubmitKYC(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedIn(w, r)
	if !ok {
		return
	}

	var req SubmitRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.kyc.Submit(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) KYCQueue(w http.ResponseWriter, r *http.Request) {
	if _, ok := operator(w, r); !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	queue, err := h.kyc.Queue(r.Context(), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": queue})
}

func (h *Handler) ReviewKYC(w http.ResponseWriter, r *http.Request) {
	actor, ok := operator(w, r)
	if !ok {
		return
	}

	var req ReviewRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.kyc.Review(r.Context(), r.PathValue("id"), req, actor)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) OpenDispute(w http.ResponseWriter, r *http.Request) {
	userID, ok := signedIn(w, r)
	if !ok {
		return
	}

	var req OpenRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.disputes.Open(r.Context(), userID, req)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) DisputeQueue(w http.ResponseWriter, r *http.Request) {
	if _, ok := operator(w, r); !ok {
		return
	}

	queue, err := h.disputes.Queue(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": queue})
}

func (h *Handler) ResolveDispute(w http.ResponseWriter, r *http.Request) {
	actor, ok := operator(w, r)
	if !ok {
		return
	}

	var req ResolveRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := h.disputes.Resolve(r.Context(), r.PathValue("id"), req, actor)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) ListBlocks(w http.ResponseWriter, r *http.Request) {
	if _, ok := operator(w, r); !ok {
		return
	}

	blocks, err := h.blocks.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": blocks})
}

func (h *Handler) Block(w http.ResponseWriter, r *http.Request) {
	actor, ok := operator(w, r)
	if !ok {
		return
	}

	var req BlockRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	block, err := h.blocks.Block(r.Context(), req, actor)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, block)
}

type UnblockRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) Unblock(w http.ResponseWriter, r *http.Request) {
	actor, ok := operator(w, r)
	if !ok {
		return
	}

	var req UnblockRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	err := h.blocks.Unblock(r.Context(),
		r.PathValue("subject_type"), r.PathValue("subject_id"), actor, req.Reason)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "lifted"})
}

func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) {
	if _, ok := operator(w, r); !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.audit.Recent(r.Context(), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": entries})
}

func (h *Handler) AuditFor(w http.ResponseWriter, r *http.Request) {
	if _, ok := operator(w, r); !ok {
		return
	}

	entries, err := h.audit.For(r.Context(),
		r.PathValue("object_type"), r.PathValue("object_id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": entries})
}

func signedIn(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := auth.UserID(r.Context())
	if userID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A valid access token is required."))
		return "", false
	}
	return userID, true
}

func operator(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor := auth.UserID(r.Context())
	if actor == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An operator token is required."))
		return "", false
	}
	if !auth.IsOperator(r.Context()) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusForbidden,
			httpx.TypeForbidden, "This endpoint is for operators."))
		return "", false
	}
	return actor, true
}

func translate(err error) error {
	switch {
	case errors.Is(err, ErrNoReason):
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field reason is required. Privileged actions are rejected without a written reason.")
	case errors.Is(err, ErrNoActor):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"An operator token is required.")
	case errors.Is(err, ErrNoObject):
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Fields subject_type and subject_id are required.")
	default:
		return err
	}
}

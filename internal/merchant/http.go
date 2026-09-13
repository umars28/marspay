package merchant

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/compliance"
	"github.com/umars28/marspay/internal/httpx"
)

type ctxKey int

const keyCtx ctxKey = iota

func FromContext(ctx context.Context) (*Key, bool) {
	k, ok := ctx.Value(keyCtx).(*Key)
	return k, ok
}

func WithKey(ctx context.Context, k *Key) context.Context {
	return context.WithValue(ctx, keyCtx, k)
}

func APIKeyAuth(keys *Keys) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := auth.BearerToken(r)
			if !strings.HasPrefix(token, auth.MerchantLive) &&
				!strings.HasPrefix(token, auth.MerchantTest) {
				next.ServeHTTP(w, r)
				return
			}

			key, err := keys.Verify(r.Context(), token)
			if errors.Is(err, ErrKeyNotFound) {
				httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
					httpx.TypeUnauthorized, "That API key is not valid."))
				return
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}

			ctx := WithKey(r.Context(), key)
			ctx = auth.WithMerchantID(ctx, key.MerchantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type Handler struct {
	keys    *Keys
	outlets *Outlets
}

func NewHandler(keys *Keys, outlets *Outlets) *Handler {
	return &Handler{keys: keys, outlets: outlets}
}

func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	merchantID, actor, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req CreateKeyRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	key, err := h.keys.Create(r.Context(), merchantID, req, actor)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, key)
}

func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeRead)
	if !ok {
		return
	}

	keys, err := h.keys.List(r.Context(), merchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": keys})
}

type RevokeRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	merchantID, actor, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req RevokeRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if err := h.keys.Revoke(r.Context(), merchantID, r.PathValue("id"), actor, req.Reason); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (h *Handler) CreateOutlet(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req CreateOutletRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	outlet, err := h.outlets.Create(r.Context(), merchantID, req)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, outlet)
}

func (h *Handler) ListOutlets(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeRead)
	if !ok {
		return
	}

	outlets, err := h.outlets.List(r.Context(), merchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": outlets})
}

func (h *Handler) AddStaff(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req AddStaffRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	staff, err := h.outlets.AddStaff(r.Context(), merchantID, req)
	if err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, staff)
}

func (h *Handler) ListStaff(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeRead)
	if !ok {
		return
	}

	staff, err := h.outlets.ListStaff(r.Context(), merchantID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": staff})
}

func (h *Handler) RevokeStaff(w http.ResponseWriter, r *http.Request) {
	merchantID, actor, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req RevokeRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if err := h.outlets.RevokeStaff(r.Context(), merchantID, r.PathValue("id"), actor, req.Reason); err != nil {
		httpx.WriteError(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func caller(w http.ResponseWriter, r *http.Request, scope string) (merchantID, actor string, ok bool) {
	if key, found := FromContext(r.Context()); found {
		if !key.Can(scope) {
			httpx.WriteError(w, r, httpx.Errorf(http.StatusForbidden, httpx.TypeForbidden,
				"This API key does not have the %s scope.", scope))
			return "", "", false
		}
		return key.MerchantID, "key:" + key.Prefix, true
	}

	if merchantID := auth.MerchantID(r.Context()); merchantID != "" {
		return merchantID, auth.UserID(r.Context()), true
	}

	httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
		httpx.TypeUnauthorized, "A merchant API key is required."))
	return "", "", false
}

func translate(err error) error {
	switch {
	case errors.Is(err, compliance.ErrNoReason):
		return httpx.Errorf(http.StatusUnprocessableEntity, httpx.TypeInvalidRequest,
			"Field reason is required.")
	case errors.Is(err, compliance.ErrNoActor):
		return httpx.Errorf(http.StatusUnauthorized, httpx.TypeUnauthorized,
			"A merchant API key is required.")
	default:
		return err
	}
}

type ChargeHandler struct {
	charges *Charges
}

func NewChargeHandler(charges *Charges) *ChargeHandler {
	return &ChargeHandler{charges: charges}
}

func (h *ChargeHandler) Create(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	var req CreateChargeRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	charge, err := h.charges.Create(r.Context(), merchantID, req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, charge)
}

func (h *ChargeHandler) List(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeRead)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	charges, err := h.charges.List(r.Context(), merchantID, r.URL.Query().Get("status"), limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": charges})
}

func (h *ChargeHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	merchantID, _, ok := caller(w, r, ScopeWrite)
	if !ok {
		return
	}

	charge, err := h.charges.Cancel(r.Context(), merchantID, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, charge)
}

func (h *ChargeHandler) Show(w http.ResponseWriter, r *http.Request) {
	if auth.UserID(r.Context()) == "" && auth.MerchantID(r.Context()) == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A payment link is only readable by a signed-in payer."))
		return
	}

	scope := auth.MerchantID(r.Context())
	charge, err := h.charges.Get(r.Context(), scope, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	charge.PaidBy = ""
	httpx.WriteJSON(w, http.StatusOK, charge)
}

func (h *ChargeHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrChargeNotFound) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No payment link with that id."))
		return
	}
	httpx.WriteError(w, r, err)
}

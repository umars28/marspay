package console

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/umars28/marspay/internal/auth"
	"github.com/umars28/marspay/internal/httpx"
)

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

func limitOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

func merchantOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := auth.MerchantID(r.Context())
	if id == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "A merchant API key is required."))
		return "", false
	}
	return id, true
}

func operatorOf(w http.ResponseWriter, r *http.Request) bool {
	if auth.UserID(r.Context()) == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An operator token is required."))
		return false
	}
	if !auth.IsOperator(r.Context()) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusForbidden,
			httpx.TypeForbidden, "This endpoint is for operators."))
		return false
	}
	return true
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
			"No such record, or it is not visible to this caller."))
		return
	}
	httpx.WriteError(w, r, err)
}

func (h *Handler) Payments(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	page, err := h.store.Payments(r.Context(), merchantID,
		r.URL.Query().Get("status"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) Payment(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	payment, err := h.store.Payment(r.Context(), merchantID, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, payment)
}

func (h *Handler) Payouts(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	page, err := h.store.Payouts(r.Context(), merchantID,
		r.URL.Query().Get("mode"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) Settlements(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	batches, err := h.store.Settlements(r.Context(), merchantID, limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": batches})
}

func (h *Handler) Endpoints(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	endpoints, err := h.store.Endpoints(r.Context(), merchantID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": endpoints})
}

func (h *Handler) Deliveries(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	deliveries, err := h.store.Deliveries(r.Context(), merchantID,
		r.URL.Query().Get("status"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": deliveries})
}

func (h *Handler) Float(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	view, err := h.store.Float(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *Handler) Engine(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	window, err := time.ParseDuration(r.URL.Query().Get("window"))
	if err != nil {
		window = 24 * time.Hour
	}

	engine, err := h.store.Engine(r.Context(), window)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, engine)
}

func (h *Handler) Reconciliation(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	view, err := h.store.Reconciliation(r.Context(), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *Handler) Ledger(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	view, err := h.store.Ledger(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	hits, err := h.store.Search(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": hits})
}

func (h *Handler) OpsPayouts(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	page, err := h.store.Payouts(r.Context(), r.URL.Query().Get("merchant_id"),
		r.URL.Query().Get("mode"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) Rules(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	rules, err := h.store.Rules(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": rules})
}

func (h *Handler) Alerts(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	page, err := h.store.Alerts(r.Context(), r.URL.Query().Get("severity"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) Account(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	view, err := h.store.Account(r.Context(), r.PathValue("id"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *Handler) Hourly(w http.ResponseWriter, r *http.Request) {
	merchantID, ok := merchantOf(w, r)
	if !ok {
		return
	}

	hours, err := h.store.Hourly(r.Context(), merchantID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": hours})
}

func (h *Handler) Queues(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	view, err := h.store.Queues(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *Handler) Transitions(w http.ResponseWriter, r *http.Request) {
	if !operatorOf(w, r) {
		return
	}

	transitions, err := h.store.Transitions(r.Context(),
		r.URL.Query().Get("kind"), r.URL.Query().Get("id"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": transitions})
}

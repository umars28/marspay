package auth

import (
	"net/http"
	"strings"

	"github.com/umars28/marspay/internal/httpx"
)

type Handler struct {
	store     *Store
	revealOTP bool
}

func NewHandler(store *Store, revealOTP bool) *Handler {
	return &Handler{store: store, revealOTP: revealOTP}
}

type otpRequest struct {
	Phone string `json:"phone"`
}

func (h *Handler) RequestOTP(w http.ResponseWriter, r *http.Request) {
	var req otpRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	req.Phone = strings.TrimSpace(req.Phone)
	if req.Phone == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
			httpx.TypeInvalidRequest, "A phone number is required."))
		return
	}

	challenge, err := h.store.StartOTP(r.Context(), req.Phone)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if !h.revealOTP {
		challenge.Code = ""
	}
	httpx.WriteJSON(w, http.StatusCreated, challenge)
}

type tokenRequest struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
	Pin         string `json:"pin"`
	DeviceID    string `json:"device_id"`
	Platform    string `json:"platform"`
	Model       string `json:"model"`
}

func (h *Handler) Token(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	switch {
	case req.ChallengeID == "" || req.Code == "":
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
			httpx.TypeInvalidRequest, "A challenge_id and code are required."))
		return
	case req.Platform != "ios" && req.Platform != "android" && req.Platform != "web":
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
			httpx.TypeInvalidRequest, "platform must be one of ios, android, web."))
		return
	}

	if err := ValidPin(req.Pin); err != nil {
		httpx.WriteError(w, r, TranslateError(err))
		return
	}

	phone, err := h.store.VerifyOTP(r.Context(), req.ChallengeID, req.Code)
	if err != nil {
		httpx.WriteError(w, r, TranslateError(err))
		return
	}

	tokens, err := h.store.Login(r.Context(), LoginRequest{
		ChallengeID: req.ChallengeID,
		Phone:       phone,
		Pin:         req.Pin,
		Platform:    req.Platform,
		Model:       req.Model,
		DeviceID:    req.DeviceID,
	})
	if err != nil {
		httpx.WriteError(w, r, TranslateError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, tokens)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeStrict(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	if !strings.HasPrefix(req.RefreshToken, RefreshPrefix) {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnprocessableEntity,
			httpx.TypeInvalidRequest, "A refresh token is required."))
		return
	}

	tokens, err := h.store.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		httpx.WriteError(w, r, TranslateError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, tokens)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sessionID := SessionID(r.Context())
	if sessionID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An access token is required."))
		return
	}

	if err := h.store.Logout(r.Context(), sessionID); err != nil {
		httpx.WriteError(w, r, TranslateError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r.Context())
	if userID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An access token is required."))
		return
	}

	devices, err := h.store.Devices(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	current := DeviceID(r.Context())
	type view struct {
		Device
		Current bool `json:"current"`
	}
	out := make([]view, 0, len(devices))
	for _, d := range devices {
		out = append(out, view{Device: d, Current: d.ID == current})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *Handler) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	userID := UserID(r.Context())
	if userID == "" {
		httpx.WriteError(w, r, httpx.Errorf(http.StatusUnauthorized,
			httpx.TypeUnauthorized, "An access token is required."))
		return
	}

	deviceID := r.PathValue("id")
	if err := h.store.RevokeDevice(r.Context(), userID, deviceID); err != nil {
		if err == ErrNoSession {
			httpx.WriteError(w, r, httpx.Errorf(http.StatusNotFound, httpx.TypeNotFound,
				"No active device with that id belongs to you."))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusNoContent, nil)
}

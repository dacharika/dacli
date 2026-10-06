package service

import (
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/deviceoauth"
	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"net/http"
	"strconv"
	"time"
)

type deviceHTTP struct {
	api     *API
	service *deviceoauth.Service
}

func (a *API) EnableDeviceAPI(s *deviceoauth.Service) error {
	if a == nil || s == nil {
		return errors.New("device service required")
	}
	a.devices = &deviceHTTP{a, s}
	return nil
}
func (h *deviceHTTP) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if h.api.identityLimit == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	decision, limitError := h.api.identityLimit.Allow(r.Context(), ratelimit.NetworkKey(h.api.limitSecret, r.RemoteAddr))
	if limitError != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	}
	if !decision.Allowed {
		seconds := int64((decision.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		if seconds > 3600 {
			seconds = 3600
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow_down"})
		return
	}
	var out any
	var e error
	if r.URL.Path == "/v1/auth/whoami" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		out, e = h.service.Who(r.Context(), r.Header.Get("Authorization"))
	} else {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/v1/auth/device":
			var input deviceoauth.BeginRequest
			if decodeClosed(r.Body, &input) != nil {
				e = deviceoauth.ErrDenied
			} else {
				out, e = h.service.Begin(r.Context(), input)
			}
		case "/v1/auth/token":
			var input deviceoauth.TokenRequest
			if decodeClosed(r.Body, &input) != nil {
				e = deviceoauth.ErrDenied
			} else {
				out, e = h.service.Token(r.Context(), input)
			}
		case "/v1/auth/revoke":
			var input deviceoauth.RevokeRequest
			if decodeClosed(r.Body, &input) != nil {
				e = deviceoauth.ErrDenied
			} else {
				e = h.service.Revoke(r.Context(), input)
				out = map[string]bool{"revoked": e == nil}
			}
		}
	}
	if e != nil {
		code := "access_denied"
		status := http.StatusForbidden
		var oauth deviceoauth.OAuthError
		if errors.As(e, &oauth) {
			code = string(oauth)
			status = http.StatusBadRequest
			if code == "temporarily_unavailable" {
				status = http.StatusServiceUnavailable
			}
		}
		writeJSON(w, status, map[string]string{"error": code})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

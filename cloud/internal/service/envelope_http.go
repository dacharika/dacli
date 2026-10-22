package service

import (
	"context"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"net/http"
	"strconv"
)

type envelopeStatus interface {
	InboxStatus(context.Context, tenant.Scope, tenant.ProjectID, int) ([]envelopeworker.InboxJob, error)
	DeadLetters(context.Context, tenant.Scope, tenant.ProjectID, int) ([]envelopeworker.DeadLetter, error)
}
type envelopeHTTP struct {
	api    *API
	inbox  *envelopeworker.Service
	auth   envelopeworker.Authenticator
	status envelopeStatus
}

func (a *API) EnableEnvelopeAPI(inbox *envelopeworker.Service, auth envelopeworker.Authenticator, status envelopeStatus) error {
	if a == nil || inbox == nil || auth == nil || status == nil {
		return errors.New("envelope API requires explicit authenticated repositories")
	}
	a.envelopes = &envelopeHTTP{a, inbox, auth, status}
	return nil
}
func (h *envelopeHTTP) receive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires POST", false)
		return
	}
	var input envelopeworker.Request
	if decodeClosed(r.Body, &input) != nil {
		h.api.writeError(w, r, http.StatusBadRequest, "invalid_envelope", "closed signed envelope required", false)
		return
	}
	input.Credential = r.Header.Get("Authorization")
	outcome, e := h.inbox.Receive(r.Context(), input)
	if e != nil {
		var limit envelopeworker.RateLimitError
		if errors.As(e, &limit) {
			seconds := int64((limit.RetryAfter + 999999999) / 1000000000)
			if seconds < 1 {
				seconds = 1
			}
			if seconds > 3600 {
				seconds = 3600
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			h.api.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded", true)
			return
		}
		if errors.Is(e, envelopeworker.ErrDenied) {
			h.api.writeError(w, r, http.StatusForbidden, "envelope_denied", "signed envelope authority refused", false)
		} else {
			h.api.writeError(w, r, http.StatusServiceUnavailable, "envelope_unavailable", "durable envelope inbox unavailable", true)
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Schema         string `json:"schema"`
		Outcome        any    `json:"outcome"`
		EventID        string `json:"event_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}{"controlplane-envelope/v1", outcome, input.Envelope.EventID, input.Envelope.IdempotencyKey})
}
func (h *envelopeHTTP) jobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.api.writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "endpoint requires GET", false)
		return
	}
	identity, e := h.auth.VerifyWork(r.Context(), r.Header.Get("Authorization"))
	if e != nil {
		h.api.writeError(w, r, http.StatusForbidden, "identity_denied", "verified identity required", false)
		return
	}
	if !h.api.allow(w, r, h.api.tenantReadLimit, ratelimit.IdentityKey(h.api.limitSecret, identity, "envelope-jobs")) {
		return
	}
	project, e := tenant.NewProjectID(r.URL.Query().Get("project"))
	if e != nil {
		h.api.writeError(w, r, http.StatusBadRequest, "invalid_project", "exact project required", false)
		return
	}
	inbox, e := h.status.InboxStatus(r.Context(), identity.Scope, project, 100)
	if e != nil {
		h.api.writeError(w, r, http.StatusServiceUnavailable, "jobs_unavailable", "durable job state unavailable", true)
		return
	}
	dead, e := h.status.DeadLetters(r.Context(), identity.Scope, project, 100)
	if e != nil {
		h.api.writeError(w, r, http.StatusServiceUnavailable, "jobs_unavailable", "durable job state unavailable", true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": "controlplane-jobs/v1", "inbox": inbox, "outbox_dead_letters": dead})
}

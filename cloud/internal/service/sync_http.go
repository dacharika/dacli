package service

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/ratelimit"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SyncBindingResolver interface {
	Resolve(context.Context, tenant.VerifiedIdentity, cloudsync.SyncRoute) (cloudsync.ProjectBindingResponse, error)
	DeviceKey(context.Context, tenant.VerifiedIdentity) (string, error)
	DevicePublicKey(context.Context, tenant.VerifiedIdentity) ([]byte, error)
}
type SyncPull interface {
	Pull(context.Context, tenant.Scope, tenant.ProjectID, uint64, uint64, []string) (envelopeworker.PullPage, error)
	Confirmed(context.Context, tenant.Scope, cloudsync.Envelope) (bool, error)
}
type syncHTTP struct {
	api      *API
	auth     IdentityVerifier
	bindings SyncBindingResolver
	inbox    *envelopeworker.Service
	pull     SyncPull
	secret   []byte
}
type deliveryCursor struct {
	Route          cloudsync.SyncRoute `json:"route"`
	Account        string              `json:"account"`
	BindingVersion uint64              `json:"binding_version"`
	After          uint64              `json:"after"`
	Snapshot       uint64              `json:"snapshot"`
}

func (a *API) EnableSyncAPI(auth IdentityVerifier, binding SyncBindingResolver, inbox *envelopeworker.Service, pull SyncPull, secret []byte) error {
	if a == nil || auth == nil || binding == nil || inbox == nil || pull == nil || len(secret) < 32 {
		return errors.New("explicit sync dependencies required")
	}
	a.sync = &syncHTTP{a, auth, binding, inbox, pull, append([]byte(nil), secret...)}
	return nil
}
func (h *syncHTTP) binding(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !h.api.allow(w, r, h.api.identityLimit, ratelimit.NetworkKey(h.api.limitSecret, r.RemoteAddr)) {
		return
	}
	identity, e := h.auth.Verify(r.Context(), r.Header.Get("Authorization"))
	if e != nil {
		h.deny(w, r)
		return
	}
	if !h.api.allow(w, r, h.api.tenantReadLimit, ratelimit.IdentityKey(h.api.limitSecret, identity, "sync-binding")) {
		return
	}
	q := r.URL.Query()
	route := cloudsync.SyncRoute{WorkspaceID: q.Get("workspace_id"), TenantID: q.Get("tenant_id"), ProjectID: q.Get("project_id"), EnvironmentID: q.Get("environment_id"), RepositoryID: q.Get("repository_id"), DeviceID: q.Get("device_id")}
	out, e := h.bindings.Resolve(r.Context(), identity, route)
	if e != nil {
		h.deny(w, r)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *syncHTTP) deny(w http.ResponseWriter, r *http.Request) {
	h.api.writeError(w, r, http.StatusForbidden, "sync_denied", "authenticated project binding refused", false)
}
func (h *syncHTTP) cursor(raw string, want deliveryCursor) (deliveryCursor, error) {
	if raw == "" {
		return want, nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 8192 {
		return want, ErrCursorDenied
	}
	data, e := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || e2 != nil || !hmac.Equal(sig, sign(h.secret, "sync-delivery/v1", data)) {
		return want, ErrCursorDenied
	}
	var got deliveryCursor
	if json.Unmarshal(data, &got) != nil || got.Route != want.Route || got.Account != want.Account || got.BindingVersion != want.BindingVersion || got.Snapshot > 0 && got.After > got.Snapshot {
		return want, ErrCursorDenied
	}
	return got, nil
}
func (h *syncHTTP) encode(c deliveryCursor) (string, error) {
	raw, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sign(h.secret, "sync-delivery/v1", raw)), nil
}
func (h *syncHTTP) exchange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !h.api.allow(w, r, h.api.identityLimit, ratelimit.NetworkKey(h.api.limitSecret, r.RemoteAddr)) {
		return
	}
	var input cloudsync.ExchangeRequest
	if decodeClosed(r.Body, &input) != nil || input.SchemaVersion != 1 || len(input.Envelopes) > 100 {
		h.deny(w, r)
		return
	}
	identity, e := h.auth.Verify(r.Context(), r.Header.Get("Authorization"))
	if e != nil {
		h.deny(w, r)
		return
	}
	// Empty exchanges still resolve bindings and pull the signed outbox (core/629).
	if !h.api.allow(w, r, h.api.tenantReadLimit, ratelimit.IdentityKey(h.api.limitSecret, identity, "sync-exchange")) {
		return
	}
	binding, e := h.bindings.Resolve(r.Context(), identity, input.Route)
	if e != nil || input.BindingVersion != binding.BindingVersion {
		h.deny(w, r)
		return
	}
	deviceKey, e := h.bindings.DeviceKey(r.Context(), identity)
	if e != nil {
		h.deny(w, r)
		return
	}
	cursor, e := h.cursor(input.DeliveryCursor, deliveryCursor{Route: input.Route, Account: string(identity.Account), BindingVersion: binding.BindingVersion})
	if e != nil {
		h.deny(w, r)
		return
	}
	out := cloudsync.ExchangeResponse{SchemaVersion: 1, Acknowledged: []cloudsync.EnvelopeAcknowledgment{}, Inbound: []cloudsync.Envelope{}}
	for _, envelope := range input.Envelopes {
		if envelope.TenantID != input.Route.TenantID || envelope.ProjectID != input.Route.ProjectID || envelope.Integrity.KeyID != deviceKey {
			h.deny(w, r)
			return
		}
		outcome, e := h.inbox.Receive(r.Context(), envelopeworker.Request{Credential: r.Header.Get("Authorization"), Envelope: envelope})
		if e != nil {
			// Invalid signatures and policy denials are final, not outages (core/635).
			if errors.Is(e, envelopeworker.ErrDenied) {
				h.deny(w, r)
				return
			}
			var limit envelopeworker.RateLimitError
			if errors.As(e, &limit) {
				seconds := int64((limit.RetryAfter + time.Second - 1) / time.Second)
				seconds = max(1, min(3600, seconds))
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
				h.api.writeError(w, r, http.StatusTooManyRequests, "rate_limited", "request rate limit exceeded", true)
				return
			}
			h.api.writeError(w, r, http.StatusServiceUnavailable, "sync_unavailable", "signed inbox processing unavailable", true)
			return
		}
		switch outcome {
		case cloudsync.OutcomeDuplicate, cloudsync.OutcomeAccepted, cloudsync.OutcomeReordered:
		default:
			h.deny(w, r)
			return
		}
		confirmed, ce := h.pull.Confirmed(r.Context(), identity.Scope, envelope)
		if ce != nil || !confirmed {
			h.deny(w, r)
			return
		}
		out.Acknowledged = append(out.Acknowledged, cloudsync.EnvelopeAcknowledgment{EventID: envelope.EventID, IdempotencyKey: envelope.IdempotencyKey, Sequence: envelope.ProducerSequence, Outcome: outcome})
	}
	project, _ := tenant.NewProjectID(input.Route.ProjectID)
	allowedKeys := make([]string, 0, len(binding.TrustedKeys)+1)
	for key := range binding.TrustedKeys {
		allowedKeys = append(allowedKeys, key)
	}
	allowedKeys = append(allowedKeys, deviceKey)
	ownPublic, e := h.bindings.DevicePublicKey(r.Context(), identity)
	if e != nil {
		h.deny(w, r)
		return
	}
	page, e := h.pull.Pull(r.Context(), identity.Scope, project, cursor.After, cursor.Snapshot, allowedKeys)
	if e != nil {
		if errors.Is(e, envelopeworker.ErrPullTooLarge) {
			h.api.writeError(w, r, http.StatusRequestEntityTooLarge, "response_too_large", "signed outbox item exceeds bounded response capacity", false)
			return
		}
		h.api.writeError(w, r, http.StatusServiceUnavailable, "sync_unavailable", "signed outbox unavailable", true)
		return
	}
	for _, envelope := range page.Envelopes {
		pub, e := base64.StdEncoding.DecodeString(binding.TrustedKeys[envelope.Integrity.KeyID])
		if envelope.Integrity.KeyID == deviceKey {
			pub = ownPublic
			e = nil
		}
		if e != nil || len(pub) != ed25519.PublicKeySize || !cloudsync.Verify(envelope, map[string]ed25519.PublicKey{envelope.Integrity.KeyID: pub}) {
			h.deny(w, r)
			return
		}
	}
	out.Inbound = page.Envelopes
	cursor.After = page.After
	cursor.Snapshot = page.Snapshot
	if !page.More {
		cursor.Snapshot = 0
	}
	out.DeliveryCursor, e = h.encode(cursor)
	if e != nil {
		h.deny(w, r)
		return
	}
	// Reauthenticate after durable writes/slow pull. Partial inbox effects remain
	// honestly retained if auth changed; replay uses exact original envelopes.
	if _, e = h.auth.Verify(r.Context(), r.Header.Get("Authorization")); e != nil {
		h.deny(w, r)
		return
	}
	if _, e = h.bindings.Resolve(r.Context(), identity, input.Route); e != nil {
		h.deny(w, r)
		return
	}
	raw, e := json.Marshal(out)
	if e != nil || len(raw) > 1<<20 {
		h.api.writeError(w, r, http.StatusServiceUnavailable, "sync_unavailable", "bounded response unavailable", true)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

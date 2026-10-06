package composition

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/cloud/internal/envelopeworker"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
	"io"
	"net"
	"net/http"
	"time"
)

type httpSender struct {
	tenant                  tenant.OrganizationID
	destination, credential string
	client                  *http.Client
	keys                    projectKeys
}

func (s *httpSender) Send(ctx context.Context, envelope cloudsync.Envelope) error {
	if envelope.TenantID != string(s.tenant) {
		return errors.New("outbox route denied")
	}
	scope, _ := tenant.NewScope(s.tenant)
	keys, e := s.keys.TrustedKeys(ctx, scope, tenant.ProjectID(envelope.ProjectID))
	if e != nil || !cloudsync.Verify(envelope, keys) || cloudsync.ValidatePayload(envelope.EventType, envelope.Payload) != nil {
		return errors.New("outbox signed envelope denied")
	}
	raw, e := json.Marshal(struct {
		Envelope cloudsync.Envelope `json:"envelope"`
	}{envelope})
	if e != nil || len(raw) > 1<<20 {
		return errors.New("outbox payload invalid")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, s.destination, bytes.NewReader(raw))
	if e != nil {
		return errors.New("outbox destination invalid")
	}
	req.Header.Set("Authorization", "Bearer "+s.credential)
	req.Header.Set("Content-Type", "application/json")
	response, e := s.client.Do(req)
	if e != nil {
		return errors.New("outbox delivery unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return errors.New("outbox remote refused")
	}
	raw, e = io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return errors.New("outbox acknowledgement unavailable")
	}
	var ack struct {
		Schema         string            `json:"schema"`
		Outcome        cloudsync.Outcome `json:"outcome"`
		EventID        string            `json:"event_id"`
		IdempotencyKey string            `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ack) != nil || decoder.Decode(&struct{}{}) != io.EOF || ack.Schema != "controlplane-envelope/v1" || ack.EventID != envelope.EventID || ack.IdempotencyKey != envelope.IdempotencyKey || (ack.Outcome != cloudsync.OutcomeAccepted && ack.Outcome != cloudsync.OutcomeReordered && ack.Outcome != cloudsync.OutcomeDuplicate) {
		return errors.New("outbox acknowledgement invalid")
	}
	return nil
}
func newDeliveryClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("delivery redirects forbidden") }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 65536, DisableKeepAlives: true}}
}

// Cycle processes only explicit operator-selected tenants. It never enumerates
// cross-tenant state with an elevated role or reads a tenant from queued data.
func (r *Runtime) Cycle(ctx context.Context) error {
	if e := r.Ready(ctx); e != nil {
		return e
	}
	tenants := r.hosted.Tenants()
	var failures []error
	var e error
	for _, t := range tenants {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		scope, _ := tenant.NewScope(t.Tenant)
		if e = r.repository.ProcessInbox(ctx, scope, r.keys, 5); e != nil {
			failures = append(failures, errors.New("inbox cycle failed"))
		}
		if t.Destination == "" {
			continue
		}
		sender := &httpSender{tenant: t.Tenant, destination: t.Destination, credential: r.hosted.DeliveryCredential(t.Tenant), client: newDeliveryClient(r.cfg.RequestTimeout), keys: r.keys}
		boundedRepo, e := r.repository.WithDeliveryAttemptLimit(5)
		if e != nil {
			return e
		}
		deliverer, e := envelopeworker.NewDeliverer(boundedRepo, sender, envelopeworker.DeliveryConfig{BatchSize: 25, MaxAttempts: 5, Lease: 2 * time.Minute, BaseBackoff: time.Second, MaxBackoff: time.Minute, Limiter: r.deliveryLimiter, LimitSecret: []byte(r.cfg.ServiceSecret())})
		if e != nil {
			return e
		}
		if e = deliverer.RunTenant(ctx, scope); e != nil {
			failures = append(failures, errors.New("outbox cycle failed"))
		}
	}
	return errors.Join(failures...)
}

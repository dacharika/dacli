package cloudsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SyncRoute is explicit comparison data, never authority. The hosted adapter
// derives tenant/device authority from the authenticated device session.
type SyncRoute struct {
	WorkspaceID   string `json:"workspace_id"`
	TenantID      string `json:"tenant_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	RepositoryID  string `json:"repository_id"`
	DeviceID      string `json:"device_id"`
}
type ProjectBindingRequest struct {
	SchemaVersion int       `json:"schema_version"`
	Route         SyncRoute `json:"route"`
}
type ProjectBindingResponse struct {
	SchemaVersion        int               `json:"schema_version"`
	Route                SyncRoute         `json:"route"`
	BindingVersion       uint64            `json:"binding_version"`
	RepositoryURL        string            `json:"repository_url"`
	DefaultBranch        string            `json:"default_branch"`
	MinimumSchemaVersion int               `json:"minimum_schema_version"`
	MaximumSchemaVersion int               `json:"maximum_schema_version"`
	TrustedKeys          map[string]string `json:"trusted_keys"`
}
type ExchangeRequest struct {
	SchemaVersion  int        `json:"schema_version"`
	Route          SyncRoute  `json:"route"`
	BindingVersion uint64     `json:"binding_version"`
	Envelopes      []Envelope `json:"envelopes"`
	Cursor         Cursor     `json:"cursor"`
	DeliveryCursor string     `json:"delivery_cursor"`
}
type EnvelopeAcknowledgment struct {
	EventID        string  `json:"event_id"`
	IdempotencyKey string  `json:"idempotency_key"`
	Sequence       uint64  `json:"sequence"`
	Outcome        Outcome `json:"outcome"`
}
type ExchangeResponse struct {
	SchemaVersion  int                      `json:"schema_version"`
	Acknowledged   []EnvelopeAcknowledgment `json:"acknowledged"`
	Inbound        []Envelope               `json:"inbound"`
	DeliveryCursor string                   `json:"delivery_cursor"`
}

var ErrTransportDenied = errors.New("cloud sync authority or schema refused")
var ErrTransportOffline = errors.New("cloud sync temporarily unavailable; immutable queue retained")

const maxRetryAfter = 30 * time.Second

// HTTPTransport consumes only an in-memory authorization header. It never
// interprets incoming metadata as local authority or sends credentials in JSON.
type HTTPTransport struct {
	Provider             string
	Authorization        string `json:"-"`
	Binding              ProjectBindingResponse
	DeliveryCursor       string
	AcknowledgedSequence uint64
	Client               *http.Client
}

func (t *HTTPTransport) request(ctx context.Context, method, path string, input, output any) error {
	u, e := url.Parse(t.Provider)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return ErrTransportDenied
	}
	if !strings.HasPrefix(t.Authorization, "Bearer ") || strings.ContainsAny(t.Authorization, "\r\n") {
		return ErrTransportDenied
	}
	var body []byte
	if input != nil {
		body, e = json.Marshal(input)
		if e != nil {
			return e
		}
	}
	if len(body) > 1<<20 {
		return ErrInvalidPayload
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.Provider, "/")+path, bytes.NewReader(body))
		if err != nil {
			return ErrTransportDenied
		}
		req.Header.Set("Authorization", t.Authorization)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		response, err := client.Do(req)
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
			closeErr := response.Body.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if readErr != nil || closeErr != nil || len(raw) > 1<<20 {
					return ErrTransportOffline
				}
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
					return ErrTransportDenied
				}
				return nil
			}
			if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
				return ErrTransportDenied
			}
			delay, ok := retryDelay(response.Header.Get("Retry-After"), attempt, time.Now())
			if !ok {
				return ErrTransportOffline
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == 2 {
				return ErrTransportOffline
			}
			if err := waitForRetry(ctx, delay); err != nil {
				return err
			}
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 2 {
			return ErrTransportOffline
		}
		if err := waitForRetry(ctx, time.Duration(1<<attempt)*time.Second); err != nil {
			return err
		}
	}
	return ErrTransportOffline
}

func retryDelay(header string, attempt int, now time.Time) (time.Duration, bool) {
	fallback := time.Duration(1<<attempt) * time.Second
	value := strings.TrimSpace(header)
	if value == "" {
		return fallback, true
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(maxRetryAfter/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	} else if allDigits(value) {
		// An overflowing decimal delta is still a valid but unbounded request.
		return 0, false
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := when.Sub(now)
		if delay < 0 {
			delay = 0
		}
		if delay > maxRetryAfter {
			return 0, false
		}
		return delay, true
	}
	return fallback, true
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Add(delay).Before(deadline) {
		return ErrTransportOffline
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *HTTPTransport) ObserveBinding(ctx context.Context, route SyncRoute) (ProjectBindingResponse, error) {
	// Query values are route comparison metadata. No bearer or signing material
	// is present in URL, JSON or durable connection state.
	values := url.Values{"workspace_id": {route.WorkspaceID}, "tenant_id": {route.TenantID}, "project_id": {route.ProjectID}, "environment_id": {route.EnvironmentID}, "repository_id": {route.RepositoryID}, "device_id": {route.DeviceID}}
	var out ProjectBindingResponse
	if e := t.request(ctx, http.MethodGet, "/v1/sync/binding?"+values.Encode(), nil, &out); e != nil {
		return out, e
	}
	if out.SchemaVersion != 1 || out.Route != route || out.BindingVersion == 0 || out.MinimumSchemaVersion > 1 || out.MaximumSchemaVersion < 1 || out.MinimumSchemaVersion < 1 || len(out.TrustedKeys) == 0 || out.RepositoryURL == "" || out.DefaultBranch == "" {
		return out, ErrTransportDenied
	}
	return out, nil
}
func (t *HTTPTransport) Exchange(ctx context.Context, pending []Envelope, cursor Cursor) (ExchangeResult, error) {
	// Keep requests bounded. Remaining rows remain immutable for the next sync.
	if len(pending) > 100 {
		pending = pending[:100]
	}
	wireCursor := Cursor{LastSequence: cursor.LastSequence, MinimumSchemaVersion: cursor.MinimumSchemaVersion, MaximumSchemaVersion: cursor.MaximumSchemaVersion, ReplayFloor: cursor.ReplayFloor}
	request := ExchangeRequest{SchemaVersion: 1, Route: t.Binding.Route, BindingVersion: t.Binding.BindingVersion, Envelopes: pending, Cursor: wireCursor, DeliveryCursor: t.DeliveryCursor}
	var out ExchangeResponse
	if e := t.request(ctx, http.MethodPost, "/v1/sync/exchange", request, &out); e != nil {
		return ExchangeResult{}, e
	}
	if out.SchemaVersion != 1 || len(out.Acknowledged) > len(pending) || len(out.Inbound) > 100 || len(out.DeliveryCursor) > 8192 {
		return ExchangeResult{}, ErrTransportDenied
	}
	expected := map[uint64]Envelope{}
	for _, row := range pending {
		expected[row.ProducerSequence] = row
	}
	accepted := map[uint64]bool{}
	for _, ack := range out.Acknowledged {
		row, ok := expected[ack.Sequence]
		if !ok || accepted[ack.Sequence] || ack.EventID != row.EventID || ack.IdempotencyKey != row.IdempotencyKey || (ack.Outcome != OutcomeAccepted && ack.Outcome != OutcomeReordered && ack.Outcome != OutcomeDuplicate) {
			return ExchangeResult{}, ErrTransportDenied
		}
		accepted[ack.Sequence] = true
	}
	sequences := make([]uint64, 0, len(accepted))
	for seq := range accepted {
		sequences = append(sequences, seq)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	ack := t.AcknowledgedSequence
	for _, seq := range sequences {
		if seq == ack+1 {
			ack = seq
		} else if seq > ack+1 {
			break
		}
	}
	// Caller persists this cursor only after Client.Sync validates and durably
	// receives all signed inbound envelopes and commits its acknowledged cursor.
	t.DeliveryCursor = out.DeliveryCursor
	return ExchangeResult{AcknowledgedSequence: ack, Inbound: out.Inbound}, nil
}

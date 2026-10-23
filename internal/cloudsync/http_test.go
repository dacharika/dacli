package cloudsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestRetriesSameBodyAndHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var firstBody, secondBody string
	started := make(chan time.Time, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		switch calls.Add(1) {
		case 1:
			firstBody = string(body)
			started <- time.Now()
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "private provider details")
		default:
			secondBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer server.Close()
	transport := HTTPTransport{Provider: server.URL, Authorization: "Bearer test-token"}
	var output struct {
		OK bool `json:"ok"`
	}
	err := transport.request(context.Background(), http.MethodPost, "/retry", map[string]string{"event": "same"}, &output)
	if err != nil || !output.OK {
		t.Fatalf("request = %+v, %v", output, err)
	}
	firstAt := <-started
	if got := calls.Load(); got != 2 {
		t.Fatalf("request attempts = %d, want 2", got)
	}
	if firstBody != secondBody || firstBody != `{"event":"same"}` {
		t.Fatalf("retry body changed: first=%q second=%q", firstBody, secondBody)
	}
	if elapsed := time.Since(firstAt); elapsed < 1900*time.Millisecond {
		t.Fatalf("retried after %s, before Retry-After 2 seconds", elapsed)
	}
}

func TestRequestDoesNotRetryBeforeRetryAfterOrPastDeadline(t *testing.T) {
	t.Run("retry after exceeds deadline", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		transport := HTTPTransport{Provider: server.URL, Authorization: "Bearer test-token"}
		start := time.Now()
		err := transport.request(ctx, http.MethodGet, "/retry", nil, &struct{}{})
		if !errors.Is(err, ErrTransportOffline) {
			t.Fatalf("request error = %v, want bounded offline refusal", err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("attempts = %d, want one before deadline", got)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("request waited %s for a retry that cannot fit the context deadline", elapsed)
		}
	})

	t.Run("cancellation during retry wait", func(t *testing.T) {
		var calls atomic.Int32
		responded := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
			responded <- struct{}{}
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		transport := HTTPTransport{Provider: server.URL, Authorization: "Bearer test-token"}
		done := make(chan error, 1)
		go func() { done <- transport.request(ctx, http.MethodGet, "/retry", nil, &struct{}{}) }()
		<-responded
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("request error = %v, want context cancellation", err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("attempts = %d, want one after cancellation", got)
		}
	})
}

func TestRequestCancellationDuringExchangeDoesNotAcknowledgeOrLeak(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	transport := HTTPTransport{Provider: server.URL, Authorization: "Bearer private-test-value", DeliveryCursor: "cursor-before", Binding: ProjectBindingResponse{Route: SyncRoute{TenantID: "tenant-a", ProjectID: "project-a"}, BindingVersion: 1}}
	client, err := Open(t.TempDir(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enqueue("run_summary", "cancel:1", summaryPayload()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.Sync(ctx, &transport) }()
	<-started
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync error = %v, want context cancellation", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want one after cancellation", got)
	}
	if got := transport.DeliveryCursor; got != "cursor-before" {
		t.Fatalf("unsuccessful exchange advanced delivery cursor to %q", got)
	}
	if pending := client.Pending(); len(pending) != 1 {
		t.Fatalf("cancelled exchange acknowledged local queue: pending=%d", len(pending))
	}
}

func TestRetryDelayParsesDateAndBoundsProviderDelay(t *testing.T) {
	now := time.Date(2026, time.October, 23, 16, 0, 0, 0, time.UTC)
	delay, ok := retryDelay(now.Add(5*time.Second).Format(http.TimeFormat), 0, now)
	if !ok || delay != 5*time.Second {
		t.Fatalf("date Retry-After = %s, %v; want 5s, true", delay, ok)
	}
	if _, ok := retryDelay("31", 0, now); ok {
		t.Fatal("accepted Retry-After beyond the 30 second bound")
	}
	delay, ok = retryDelay("not-a-date", 1, now)
	if !ok || delay != 2*time.Second {
		t.Fatalf("invalid Retry-After fallback = %s, %v; want 2s, true", delay, ok)
	}
}

func TestExchangeRequiresExactAcknowledgmentsAndAdvancesOnlyContiguousPrefix(t *testing.T) {
	client, err := Open(t.TempDir(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var pending []Envelope
	for _, key := range []string{"ack:1", "ack:2", "ack:3"} {
		e, err := client.Enqueue("run_summary", key, summaryPayload())
		if err != nil {
			t.Fatal(err)
		}
		pending = append(pending, e)
	}
	ack := func(sequence int, eventID string) EnvelopeAcknowledgment {
		return EnvelopeAcknowledgment{EventID: eventID, IdempotencyKey: pending[sequence-1].IdempotencyKey, Sequence: uint64(sequence), Outcome: OutcomeAccepted}
	}
	run := func(t *testing.T, acknowledgments []EnvelopeAcknowledgment, want uint64, wantErr error) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request ExchangeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if len(request.Envelopes) != len(pending) {
				t.Errorf("sent %d envelopes, want %d", len(request.Envelopes), len(pending))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ExchangeResponse{SchemaVersion: 1, Acknowledged: acknowledgments, Inbound: []Envelope{}, DeliveryCursor: "cursor-next"})
		}))
		defer server.Close()
		transport := HTTPTransport{Provider: server.URL, Authorization: "Bearer test-token", Binding: ProjectBindingResponse{Route: SyncRoute{TenantID: "tenant-a", ProjectID: "project-a"}, BindingVersion: 1}}
		result, err := transport.Exchange(context.Background(), pending, Cursor{})
		if wantErr != nil {
			if !errors.Is(err, wantErr) {
				t.Fatalf("Exchange error = %v, want %v", err, wantErr)
			}
			return
		}
		if err != nil || result.AcknowledgedSequence != want {
			t.Fatalf("Exchange = %+v, %v; want ack %d", result, err, want)
		}
	}
	t.Run("hole does not advance", func(t *testing.T) { run(t, []EnvelopeAcknowledgment{ack(2, pending[1].EventID)}, 0, nil) })
	t.Run("contiguous prefix advances", func(t *testing.T) {
		run(t, []EnvelopeAcknowledgment{ack(1, pending[0].EventID), ack(2, pending[1].EventID), ack(3, pending[2].EventID)}, 3, nil)
	})
	t.Run("wrong event id refused", func(t *testing.T) {
		run(t, []EnvelopeAcknowledgment{ack(1, "01J00000000000000000000099")}, 0, ErrTransportDenied)
	})
	t.Run("unsent sequence refused", func(t *testing.T) {
		bad := ack(1, pending[0].EventID)
		bad.Sequence = 9
		run(t, []EnvelopeAcknowledgment{bad}, 0, ErrTransportDenied)
	})
}

func TestRequestRedactsProviderErrorAndBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "secret-provider-response")
	}))
	defer server.Close()
	const bearer = "Bearer private-test-value"
	transport := HTTPTransport{Provider: server.URL, Authorization: bearer}
	err := transport.request(context.Background(), http.MethodGet, "/denied", nil, &struct{}{})
	if !errors.Is(err, ErrTransportDenied) {
		t.Fatalf("request error = %v, want transport denial", err)
	}
	if strings.Contains(err.Error(), "secret-provider-response") || strings.Contains(err.Error(), "private-test-value") {
		t.Fatalf("provider or credential data leaked in error: %v", err)
	}
}

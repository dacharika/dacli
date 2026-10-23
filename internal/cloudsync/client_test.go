package cloudsync

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, private
}

func testConfig(t *testing.T) Config {
	t.Helper()
	pub, private := testKeys(t)
	return Config{
		TenantID: "tenant-a", ProjectID: "project-a", ProducerVersion: "test",
		SigningKeyID: "local", SigningKey: private,
		TrustedKeys: map[string]ed25519.PublicKey{"local": pub},
		Cursor:      Cursor{MinimumSchemaVersion: 1, MaximumSchemaVersion: 1, ReplayFloor: 1},
	}
}

func summaryPayload() map[string]any {
	return map[string]any{
		"run_id": "run-1", "task_id": "task-1", "agent_id": "agent-1", "status": "succeeded",
		"started_at": "2026-08-13T16:00:00Z", "finished_at": "2026-08-13T16:01:00Z",
		"commit_ids": []any{"0123456789abcdef0123456789abcdef01234567"},
		"checks":     []any{map[string]any{"name": "go test", "result": "pass"}},
	}
}

func TestOutboxAndCursorSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)
	client, err := Open(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := client.Enqueue("run_summary", "run:1", summaryPayload())
	if err != nil {
		t.Fatal(err)
	}
	inbound := envelope
	inbound.EventID = "01J00000000000000000000009"
	inbound.ProducerSequence = 1
	inbound.IdempotencyKey = "remote:1"
	if err := Sign(&inbound, cfg.SigningKey); err != nil {
		t.Fatal(err)
	}
	if outcome, err := client.Receive(inbound); err != nil || outcome != OutcomeAccepted {
		t.Fatalf("receive = %q, %v", outcome, err)
	}

	reopened, err := Open(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pending := reopened.Pending()
	if len(pending) != 1 || pending[0].EventID != envelope.EventID {
		t.Fatalf("pending after restart = %+v", pending)
	}
	if got := reopened.Cursor(); got.LastSequence != 1 || len(got.SeenEventIDs) != 1 {
		t.Fatalf("cursor after restart = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "inbox", inbound.EventID+".json")); err != nil {
		t.Fatalf("verified inbound envelope was not durable: %v", err)
	}
}

type retryTransport struct {
	calls [][]Envelope
	fail  bool
}

func (r *retryTransport) Exchange(_ context.Context, outbound []Envelope, _ Cursor) (ExchangeResult, error) {
	r.calls = append(r.calls, append([]Envelope(nil), outbound...))
	if r.fail {
		r.fail = false
		return ExchangeResult{}, errors.New("offline")
	}
	if len(outbound) == 0 {
		return ExchangeResult{}, nil
	}
	return ExchangeResult{AcknowledgedSequence: outbound[len(outbound)-1].ProducerSequence}, nil
}

func TestSyncRetryUsesOriginalEnvelopeAndAcknowledgementIsIdempotent(t *testing.T) {
	client, err := Open(t.TempDir(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enqueue("run_summary", "run:retry", summaryPayload()); err != nil {
		t.Fatal(err)
	}
	remote := &retryTransport{fail: true}
	if err := client.Sync(context.Background(), remote); err == nil {
		t.Fatal("first sync unexpectedly succeeded")
	}
	if err := client.Sync(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if len(remote.calls) != 2 || len(remote.calls[0]) != 1 || !reflect.DeepEqual(remote.calls[0][0], remote.calls[1][0]) {
		t.Fatalf("retry changed immutable envelope: %+v", remote.calls)
	}
	if len(client.Pending()) != 0 {
		t.Fatal("acknowledged envelope remained pending")
	}
	if err := client.Sync(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	if len(remote.calls) != 3 || len(remote.calls[2]) != 0 {
		t.Fatalf("empty retry sent an acknowledged envelope: %+v", remote.calls)
	}
	again, err := client.Enqueue("run_summary", "run:retry", summaryPayload())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, remote.calls[0][0]) || len(client.Pending()) != 0 {
		t.Fatalf("acknowledged idempotency key created a new envelope: %+v", again)
	}
}

func TestReceiveFixturesAndSecurityChecks(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "contracts", "controlplane", "v1", "testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture struct {
				Outcome string `json:"outcome"`
				State   Cursor `json:"state"`
				Event   struct {
					EventID          string `json:"event_id"`
					IdempotencyKey   string `json:"idempotency_key"`
					ProducerSequence uint64 `json:"producer_sequence"`
					SchemaVersion    int    `json:"schema_version"`
					SignatureValid   bool   `json:"signature_valid"`
				} `json:"event"`
			}
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			cfg := testConfig(t)
			cfg.Cursor = fixture.State
			client, err := Open(t.TempDir(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			e := Envelope{SchemaVersion: fixture.Event.SchemaVersion, EventType: "run_summary", TenantID: cfg.TenantID, ProjectID: cfg.ProjectID, EventID: fixture.Event.EventID, ProducerSequence: fixture.Event.ProducerSequence, OccurredAt: time.Now().UTC(), IdempotencyKey: fixture.Event.IdempotencyKey, ProducerVersion: "remote", Payload: mustRaw(t, summaryPayload()), Integrity: Integrity{Algorithm: "Ed25519", KeyID: cfg.SigningKeyID}}
			if err := Sign(&e, cfg.SigningKey); err != nil {
				t.Fatal(err)
			}
			if !fixture.Event.SignatureValid {
				e.Payload[1] ^= 1
			}
			if fixture.Outcome == string(OutcomeDuplicate) && fixture.Event.SignatureValid {
				// A duplicate is an exact retry of a retained signed envelope. The
				// legacy fixture's seen-ID list alone cannot prove content equality.
				if err := client.persistInbound(e); err != nil {
					t.Fatal(err)
				}
				client, err = Open(client.root, cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := stateBytes(client.root)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := client.Receive(e)
			wantErr := fixture.Outcome == "refuse-tampered" || fixture.Outcome == "refuse-incompatible" || fixture.Outcome == "refuse-replay"
			if (err != nil) != wantErr || string(outcome) != fixture.Outcome {
				t.Fatalf("receive = %q, %v; want %q, error=%v", outcome, err, fixture.Outcome, wantErr)
			}
			if fixture.Outcome == "refuse-tampered" && !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("tampered error = %v, want ErrInvalidSignature", err)
			}
			if fixture.Outcome == "refuse-tampered" || fixture.Outcome == "refuse-incompatible" {
				after, err := stateBytes(client.root)
				if err != nil {
					t.Fatal(err)
				}
				if string(after) != string(before) {
					t.Fatalf("security refusal mutated state\nbefore %s\nafter  %s", before, after)
				}
				entries, err := os.ReadDir(filepath.Join(client.root, "inbox"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("security refusal mutated inbox: %v, %v", entries, err)
				}
			}
		})
	}
}

func mustRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func stateBytes(root string) ([]byte, error) { return os.ReadFile(filepath.Join(root, "state.json")) }

func TestEnqueueRejectsNonAllowlistedMetadata(t *testing.T) {
	client, err := Open(t.TempDir(), testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	payload := summaryPayload()
	payload["command_output"] = "secret output"
	if _, err := client.Enqueue("run_summary", "unsafe", payload); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("error = %v, want ErrInvalidPayload", err)
	}
	if len(client.Pending()) != 0 {
		t.Fatal("invalid payload reached outbox")
	}
}

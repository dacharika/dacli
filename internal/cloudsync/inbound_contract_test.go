package cloudsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validInbound(t *testing.T, cfg Config) Envelope {
	t.Helper()
	e := Envelope{
		SchemaVersion:    1,
		EventType:        "run_summary",
		TenantID:         cfg.TenantID,
		ProjectID:        cfg.ProjectID,
		EventID:          "01J00000000000000000000001",
		ProducerSequence: 1,
		OccurredAt:       time.Date(2026, time.October, 23, 16, 0, 0, 0, time.UTC),
		IdempotencyKey:   "remote:1",
		ProducerVersion:  "test",
		Payload:          mustRaw(t, summaryPayload()),
		Integrity:        Integrity{Algorithm: "Ed25519", KeyID: cfg.SigningKeyID},
	}
	if err := Sign(&e, cfg.SigningKey); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestReceiveRejectsMalformedEventIDBeforeInboxOrCursorWrite(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, err := stateBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	e := validInbound(t, cfg)
	e.EventID = "../../spill"
	if err := Sign(&e, cfg.SigningKey); err != nil {
		t.Fatal(err)
	}
	outcome, err := client.Receive(e)
	if outcome != OutcomeTampered || !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Receive = %q, %v; want tampered invalid-payload refusal", outcome, err)
	}
	if after, err := stateBytes(root); err != nil || string(after) != string(before) {
		t.Fatalf("malformed event changed cursor state: err=%v before=%s after=%s", err, before, after)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "spill.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed event wrote outside inbox: stat err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "inbox"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("malformed event reached inbox: entries=%v err=%v", entries, err)
	}
}

func TestReceiveRejectsMalformedRequiredV1FieldsBeforePersistence(t *testing.T) {
	mutations := []struct {
		name string
		edit func(*Envelope)
	}{
		{"event id", func(e *Envelope) { e.EventID = "bad" }},
		{"sequence", func(e *Envelope) { e.ProducerSequence = 0 }},
		{"occurred at", func(e *Envelope) { e.OccurredAt = time.Time{} }},
		{"idempotency key", func(e *Envelope) { e.IdempotencyKey = "" }},
		{"producer version", func(e *Envelope) { e.ProducerVersion = "" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := testConfig(t)
			client, err := Open(root, cfg)
			if err != nil {
				t.Fatal(err)
			}
			e := validInbound(t, cfg)
			mutation.edit(&e)
			if err := Sign(&e, cfg.SigningKey); err != nil {
				t.Fatal(err)
			}
			outcome, err := client.Receive(e)
			if outcome != OutcomeTampered || !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("Receive = %q, %v; want tampered invalid-payload refusal", outcome, err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "inbox"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid envelope reached inbox: entries=%v err=%v", entries, err)
			}
			if got := client.Cursor(); got.LastSequence != 0 || len(got.SeenEventIDs) != 0 {
				t.Fatalf("invalid envelope advanced cursor: %+v", got)
			}
		})
	}
}

package cloudsync

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInboundSequenceReservationsSurvivePayloadExpiry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sequence    uint64
		retainOther bool
		expectGaps  bool
	}{
		{name: "all rows expired", sequence: 1},
		{name: "gap reservation with another signer retained", sequence: 3, retainOther: true, expectGaps: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := testConfig(t)
			otherPrivate := secondInboundSigner(t, &cfg, "other")
			client, err := Open(root, cfg)
			if err != nil {
				t.Fatal(err)
			}
			original := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "expired:original", tc.sequence)
			requireReceive(t, client, original, OutcomeAccepted)
			other := signedInbound(t, cfg, otherPrivate, "01J00000000000000000000002", "other:1", 1)
			if tc.retainOther {
				requireReceive(t, client, other, OutcomeAccepted)
			}
			if err := os.Remove(filepath.Join(root, "inbox", original.EventID+".json")); err != nil {
				t.Fatal(err)
			}
			if !tc.retainOther {
				if err := os.Remove(filepath.Join(root, "inbox", other.EventID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			client, err = Open(root, cfg)
			if err != nil {
				t.Fatal(err)
			}

			stateBefore, err := stateBytes(root)
			if err != nil {
				t.Fatal(err)
			}
			conflict := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000003", "expired:different", tc.sequence)
			outcome, err := client.Receive(conflict)
			if !errors.Is(err, ErrReplay) && !errors.Is(err, ErrInboundCollision) {
				t.Fatalf("expired producer sequence accepted a new identity: outcome=%s err=%v", outcome, err)
			}
			stateAfter, err := stateBytes(root)
			if err != nil || string(stateAfter) != string(stateBefore) {
				t.Fatalf("expired sequence refusal mutated state: err=%v", err)
			}
			rows, err := os.ReadDir(filepath.Join(root, "inbox"))
			wantRows := 0
			if tc.retainOther {
				wantRows = 1
			}
			if err != nil || len(rows) != wantRows {
				t.Fatalf("expired sequence refusal changed inbox rows: rows=%v err=%v", rows, err)
			}

			if tc.retainOther {
				requireReceive(t, client, other, OutcomeDuplicate)
			}
			if tc.expectGaps {
				one := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000004", "expired:lower-1", 1)
				two := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000005", "expired:lower-2", 2)
				requireReceive(t, client, one, OutcomeAccepted)
				requireReceive(t, client, two, OutcomeAccepted)
				stream := inboundStreamID(original)
				if got := client.state.InboundStreams[stream].LastSequence; got != 3 {
					t.Fatalf("cursor after filling lower gaps = %d, want 3", got)
				}
				four := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000006", "expired:after-high-water", 4)
				requireReceive(t, client, four, OutcomeAccepted)
			}
		})
	}
}

func TestExpiredStreamStateIsQuarantinedWhenItsKeyIsRemoved(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "local:1", 1)
	requireReceive(t, client, original, OutcomeAccepted)
	if err := os.Remove(filepath.Join(root, "inbox", original.EventID+".json")); err != nil {
		t.Fatal(err)
	}

	withoutKey := cfg
	withoutKey.TrustedKeys = map[string]ed25519.PublicKey{}
	quarantined, err := Open(root, withoutKey)
	if err != nil {
		t.Fatal(err)
	}
	stream := inboundStreamID(original)
	if !quarantined.state.UntrustedStreams[stream] {
		t.Fatal("expired stream state was not durably quarantined after key removal")
	}
	if got := quarantined.Cursor().LastSequence; got != 0 {
		t.Fatalf("removed-key stream contributed to diagnostic cursor: got %d, want 0", got)
	}
	before := quarantined.state.InboundStreams[stream].LastSequence

	retrusted, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !retrusted.state.UntrustedStreams[stream] {
		t.Fatal("re-adding the key cleared the durable quarantine")
	}
	if got := retrusted.Cursor().LastSequence; got != 0 {
		t.Fatalf("re-trusted quarantined stream contributed to diagnostic cursor: got %d, want 0", got)
	}
	if got := retrusted.state.InboundStreams[stream].LastSequence; got != before {
		t.Fatalf("quarantined stream progress changed after re-trust: got %d, want %d", got, before)
	}
	fresh := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000002", "local:2", 2)
	if outcome, err := retrusted.Receive(fresh); outcome != OutcomeUntrusted || !errors.Is(err, ErrUntrustedHistory) {
		t.Fatalf("re-trusted quarantined stream = %q, %v; want durable quarantine", outcome, err)
	}
}

func TestExpiredHistoryPreservesIndependentSignerDiagnosticMaximum(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	otherPrivate := secondInboundSigner(t, &cfg, "other")
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"01J00000000000000000000001",
		"01J00000000000000000000002",
		"01J00000000000000000000003",
	}
	for i, id := range ids {
		event := signedInbound(t, cfg, cfg.SigningKey, id, "expired:"+id, uint64(i+1))
		requireReceive(t, client, event, OutcomeAccepted)
		if err := os.Remove(filepath.Join(root, "inbox", id+".json")); err != nil {
			t.Fatal(err)
		}
	}
	client, err = Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}

	independent := signedInbound(t, cfg, otherPrivate, "01J00000000000000000000004", "other:1", 1)
	requireReceive(t, client, independent, OutcomeAccepted)
	if got := client.Cursor().LastSequence; got != 3 {
		t.Fatalf("diagnostic maximum after independent signer sequence 1 = %d, want 3", got)
	}

	next := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000005", "local:4", 4)
	requireReceive(t, client, next, OutcomeAccepted)
	if got := client.Cursor().LastSequence; got != 4 {
		t.Fatalf("diagnostic maximum after original signer sequence 4 = %d, want 4", got)
	}
}

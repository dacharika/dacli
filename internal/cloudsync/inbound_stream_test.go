package cloudsync

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func secondInboundSigner(t *testing.T, cfg *Config, keyID string) ed25519.PrivateKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TrustedKeys[keyID] = public
	return private
}

func signedInbound(t *testing.T, cfg Config, private ed25519.PrivateKey, eventID, idempotency string, sequence uint64) Envelope {
	t.Helper()
	e := validInbound(t, cfg)
	for keyID, public := range cfg.TrustedKeys {
		if private.Public().(ed25519.PublicKey).Equal(public) {
			e.Integrity.KeyID = keyID
			break
		}
	}
	e.EventID = eventID
	e.IdempotencyKey = idempotency
	e.ProducerSequence = sequence
	if err := Sign(&e, private); err != nil {
		t.Fatal(err)
	}
	return e
}

func requireReceive(t *testing.T, client *Client, event Envelope, want Outcome) {
	t.Helper()
	got, err := client.Receive(event)
	if err != nil || got != want {
		t.Fatalf("Receive = %q, %v; want %q", got, err, want)
	}
}

func TestReceiveUsesIndependentSignerSequencesAndExactDuplicates(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	otherPrivate := secondInboundSigner(t, &cfg, "server")
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	local := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "local:1", 1)
	server := signedInbound(t, cfg, otherPrivate, "01J00000000000000000000002", "server:1", 1)
	requireReceive(t, client, local, OutcomeAccepted)
	requireReceive(t, client, server, OutcomeAccepted)
	requireReceive(t, client, local, OutcomeDuplicate)
	requireReceive(t, client, server, OutcomeDuplicate)
	if len(client.state.InboundStreams) != 2 || client.Cursor().LastSequence != 1 {
		t.Fatalf("stream state = %+v, diagnostic cursor = %+v", client.state.InboundStreams, client.Cursor())
	}
}

func TestReceiveRefusesSequenceAndIdentityContentCollisionsWithoutWrites(t *testing.T) {
	for _, collision := range []string{"sequence", "event ID", "idempotency key"} {
		t.Run(collision, func(t *testing.T) {
			root := t.TempDir()
			cfg := testConfig(t)
			client, err := Open(root, cfg)
			if err != nil {
				t.Fatal(err)
			}
			first := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "remote:1", 1)
			requireReceive(t, client, first, OutcomeAccepted)
			second := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000002", "remote:2", 2)
			switch collision {
			case "sequence":
				second.ProducerSequence = 1
			case "event ID":
				second.EventID = first.EventID
				second.ProducerSequence = 2
			case "idempotency key":
				second.IdempotencyKey = first.IdempotencyKey
				second.ProducerSequence = 2
			}
			if collision != "sequence" {
				payload := summaryPayload()
				payload["task_id"] = "changed-content"
				second.Payload = mustRaw(t, payload)
			}
			if err := Sign(&second, cfg.SigningKey); err != nil {
				t.Fatal(err)
			}
			before, err := stateBytes(root)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := client.Receive(second)
			if outcome != OutcomeCollision || !errors.Is(err, ErrInboundCollision) {
				t.Fatalf("Receive = %q, %v; want identity collision refusal", outcome, err)
			}
			after, err := stateBytes(root)
			if err != nil || string(after) != string(before) {
				t.Fatalf("collision changed state: err=%v before=%s after=%s", err, before, after)
			}
			entries, err := os.ReadDir(filepath.Join(root, "inbox"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("collision changed inbox: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestReceiveAdvancesGapsPerSigner(t *testing.T) {
	cfg := testConfig(t)
	otherPrivate := secondInboundSigner(t, &cfg, "server")
	client, err := Open(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireReceive(t, client, signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "local:3", 3), OutcomeAccepted)
	requireReceive(t, client, signedInbound(t, cfg, otherPrivate, "01J00000000000000000000002", "server:1", 1), OutcomeAccepted)
	client, err = Open(client.root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	requireReceive(t, client, signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000003", "local:1", 1), OutcomeAccepted)
	requireReceive(t, client, signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000004", "local:2", 2), OutcomeAccepted)
	localKey := inboundStreamID(signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000005", "unused", 5))
	serverKey := inboundStreamID(signedInbound(t, cfg, otherPrivate, "01J00000000000000000000006", "unused-server", 6))
	if got := client.state.InboundStreams[localKey].LastSequence; got != 3 {
		t.Fatalf("local contiguous sequence = %d, want 3", got)
	}
	if got := client.state.InboundStreams[serverKey].LastSequence; got != 1 {
		t.Fatalf("server contiguous sequence = %d, want 1", got)
	}
}

func TestReceiveRetriesStateSaveFailureWithoutReportingFalseDuplicate(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "remote:1", 1)
	statePath := filepath.Join(root, "state.json")
	backup := filepath.Join(root, "state.backup")
	if err := os.Rename(statePath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if outcome, err := client.Receive(e); err == nil || outcome != "" {
		t.Fatalf("Receive with blocked state save = %q, %v; want an error", outcome, err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, statePath); err != nil {
		t.Fatal(err)
	}
	requireReceive(t, client, e, OutcomeDuplicate)
	if got := client.Cursor().LastSequence; got != 1 {
		t.Fatalf("cursor after retry = %d, want 1", got)
	}
}

func TestOpenRecoversRowWrittenBeforeStateAndRefusesHistoricalCollisionReadOnly(t *testing.T) {
	t.Run("recover durable row", func(t *testing.T) {
		root := t.TempDir()
		cfg := testConfig(t)
		client, err := Open(root, cfg)
		if err != nil {
			t.Fatal(err)
		}
		e := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "remote:1", 1)
		if err := client.persistInbound(e); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(root, cfg)
		if err != nil {
			t.Fatal(err)
		}
		requireReceive(t, reopened, e, OutcomeDuplicate)
		if got := reopened.Cursor().LastSequence; got != 1 {
			t.Fatalf("recovered cursor = %d, want 1", got)
		}
	})

	t.Run("historical collision", func(t *testing.T) {
		root := t.TempDir()
		cfg := testConfig(t)
		client, err := Open(root, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Enqueue("run_summary", "outbox:1", summaryPayload()); err != nil {
			t.Fatal(err)
		}
		first := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "remote:1", 1)
		second := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000002", "remote:2", 1)
		for _, row := range []Envelope{first, second} {
			if err := createJSON(filepath.Join(root, "inbox", row.EventID+".json"), row); err != nil {
				t.Fatal(err)
			}
		}
		statePath := filepath.Join(root, "state.json")
		var staleState persistentState
		stateRaw, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(stateRaw, &staleState); err != nil {
			t.Fatal(err)
		}
		staleState.NextSequence = 1 // Open would normally recover this from the immutable outbox.
		stateRaw, err = json.Marshal(staleState)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, stateRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		stateBefore, err := stateBytes(root)
		if err != nil {
			t.Fatal(err)
		}
		rowBefore := make(map[string][]byte)
		for _, row := range []Envelope{first, second} {
			path := filepath.Join(root, "inbox", row.EventID+".json")
			rowBefore[row.EventID], err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Open(root, cfg); !errors.Is(err, ErrInboundCollision) {
			t.Fatalf("Open error = %v; want historical sequence collision", err)
		}
		stateAfter, err := stateBytes(root)
		if err != nil || string(stateAfter) != string(stateBefore) {
			t.Fatalf("Open changed state on collision: err=%v before=%s after=%s", err, stateBefore, stateAfter)
		}
		for id, before := range rowBefore {
			after, err := os.ReadFile(filepath.Join(root, "inbox", id+".json"))
			if err != nil || string(after) != string(before) {
				t.Fatalf("Open changed historical row %s: err=%v", id, err)
			}
		}
	})
}

func TestOpenBlocksUnknownKeyHistoryEvenAfterKeyIsAdded(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	unknownPublic, unknownPrivate := testKeys(t)
	unknownConfig := cfg
	unknownConfig.TrustedKeys = map[string]ed25519.PublicKey{"local": cfg.TrustedKeys["local"]}
	retiredConfig := Config{TenantID: cfg.TenantID, ProjectID: cfg.ProjectID, SigningKeyID: "retired", SigningKey: unknownPrivate, TrustedKeys: map[string]ed25519.PublicKey{"retired": unknownPublic}}
	row := signedInbound(t, retiredConfig, unknownPrivate, "01J00000000000000000000001", "retired:1", 1)
	row2 := signedInbound(t, retiredConfig, unknownPrivate, "01J00000000000000000000004", "retired:2", 2)
	initial, err := Open(root, unknownConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.persistInbound(row); err != nil {
		t.Fatal(err)
	}
	if err := initial.persistInbound(row2); err != nil {
		t.Fatal(err)
	}
	quarantined, err := Open(root, unknownConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := quarantined.state.InboundStreams[inboundStreamID(row)].LastSequence; got != 0 {
		t.Fatalf("unknown-key history gained cursor authority before re-trust: %d", got)
	}
	trustedAgain := unknownConfig
	healthyPublic, healthyPrivate := testKeys(t)
	trustedAgain.TrustedKeys = map[string]ed25519.PublicKey{"local": cfg.TrustedKeys["local"], "retired": unknownPublic, "healthy": healthyPublic}
	client, err := Open(root, trustedAgain)
	if err != nil {
		t.Fatal(err)
	}
	if got := client.state.InboundStreams[inboundStreamID(row)].LastSequence; got != 0 {
		t.Fatalf("re-trusted quarantined stream gained cursor authority: %d", got)
	}
	fresh := signedInbound(t, trustedAgain, unknownPrivate, "01J00000000000000000000002", "retired:3", 3)
	if outcome, err := client.Receive(fresh); outcome != OutcomeUntrusted || !errors.Is(err, ErrUntrustedHistory) {
		t.Fatalf("Receive = %q, %v; want untrusted historical stream refusal", outcome, err)
	}
	healthy := signedInbound(t, trustedAgain, healthyPrivate, "01J00000000000000000000003", "healthy:1", 1)
	requireReceive(t, client, healthy, OutcomeAccepted)
	if got := client.state.InboundStreams[inboundStreamID(healthy)].LastSequence; got != 1 {
		t.Fatalf("healthy signer cursor = %d, want 1 independent of quarantined history", got)
	}
	if got := client.state.InboundStreams[inboundStreamID(row)].LastSequence; got != 0 {
		t.Fatalf("quarantined signer gained contiguous cursor authority: %d", got)
	}
}

func TestOpenRefusesInvalidTrustedHistoryWithoutRewritingStateOrRows(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	if _, err := Open(root, cfg); err != nil {
		t.Fatal(err)
	}
	e := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000001", "remote:1", 1)
	e.EventID = "01J00000000000000000000002" // Keep valid fields while invalidating the signature.
	path := filepath.Join(root, "inbox", e.EventID+".json")
	if err := createJSON(path, e); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := stateBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	rowBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, cfg); err == nil {
		t.Fatal("Open accepted a trusted historical row with an invalid signature")
	}
	stateAfter, err := stateBytes(root)
	if err != nil || string(stateAfter) != string(stateBefore) {
		t.Fatalf("Open changed state: err=%v before=%s after=%s", err, stateBefore, stateAfter)
	}
	rowAfter, err := os.ReadFile(path)
	if err != nil || string(rowAfter) != string(rowBefore) {
		t.Fatalf("Open changed invalid historical row: err=%v", err)
	}
}

func TestOpenLegacyScalarCursorDoesNotSeedProducerAuthority(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig(t)
	legacy := persistentState{NextSequence: 1, Cursor: Cursor{LastSequence: 12, SeenEventIDs: []string{"01J00000000000000000000009"}, SeenIdempotency: []string{"legacy:reserved"}, MinimumSchemaVersion: 1, MaximumSchemaVersion: 1, ReplayFloor: 1}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	reserved := signedInbound(t, cfg, cfg.SigningKey, "01J00000000000000000000009", "new:key", 1)
	if outcome, err := client.Receive(reserved); outcome != OutcomeCollision || !errors.Is(err, ErrInboundCollision) {
		t.Fatalf("legacy reservation = %q, %v; want conservative collision refusal", outcome, err)
	}
	otherPrivate := secondInboundSigner(t, &cfg, "server")
	// Reopen to install the second signer's public key in the trust set.
	client, err = Open(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := signedInbound(t, cfg, otherPrivate, "01J00000000000000000000010", "server:1", 1)
	requireReceive(t, client, e, OutcomeReordered) // Legacy cursor is a diagnostic outcome fallback only.
	stream := inboundStreamID(e)
	if got := client.state.InboundStreams[stream].LastSequence; got != 1 {
		t.Fatalf("new stream cursor = %d, want 1 (legacy cursor must not seed authority)", got)
	}
	if got := client.Cursor().LastSequence; got != 1 {
		t.Fatalf("diagnostic cursor after new history = %d, want max per-stream cursor 1", got)
	}
}

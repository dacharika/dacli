// Package cloudsync implements the offline, metadata-only side of the signed
// control-plane v1 protocol. It owns durable transport state only; accepted
// inbound envelopes remain proposals and are never applied to a workspace.
package cloudsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlnomadpy/dacli/internal/ulid"
)

var (
	ErrInvalidSignature   = errors.New("invalid control-plane signature")
	ErrUnsupportedVersion = errors.New("unsupported control-plane schema version")
	ErrReplay             = errors.New("control-plane event is below the replay floor")
	ErrInvalidPayload     = errors.New("payload is outside the control-plane metadata allowlist")
)

type Integrity struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

type Envelope struct {
	SchemaVersion    int             `json:"schema_version"`
	EventType        string          `json:"event_type"`
	TenantID         string          `json:"tenant_id"`
	ProjectID        string          `json:"project_id"`
	EventID          string          `json:"event_id"`
	ProducerSequence uint64          `json:"producer_sequence"`
	OccurredAt       time.Time       `json:"occurred_at"`
	IdempotencyKey   string          `json:"idempotency_key"`
	ProducerVersion  string          `json:"producer_version"`
	Payload          json.RawMessage `json:"payload"`
	Integrity        Integrity       `json:"integrity"`
}

type Cursor struct {
	LastSequence         uint64   `json:"last_sequence"`
	SeenEventIDs         []string `json:"seen_event_ids"`
	SeenIdempotency      []string `json:"seen_idempotency_keys"`
	MinimumSchemaVersion int      `json:"minimum_schema_version"`
	MaximumSchemaVersion int      `json:"maximum_schema_version"`
	ReplayFloor          uint64   `json:"replay_floor"`
}

type Config struct {
	TenantID, ProjectID string
	ProducerVersion     string
	SigningKeyID        string
	SigningKey          ed25519.PrivateKey
	TrustedKeys         map[string]ed25519.PublicKey
	Cursor              Cursor
}

type persistentState struct {
	NextSequence         uint64          `json:"next_sequence"`
	AcknowledgedSequence uint64          `json:"acknowledged_sequence"`
	Cursor               Cursor          `json:"cursor"`
	AcceptedSequences    map[uint64]bool `json:"accepted_sequences,omitempty"`
}

type Client struct {
	mu     sync.Mutex
	root   string
	config Config
	state  persistentState
}

func Open(root string, config Config) (*Client, error) {
	if config.TenantID == "" || config.ProjectID == "" || config.SigningKeyID == "" || len(config.SigningKey) != ed25519.PrivateKeySize {
		return nil, errors.New("cloudsync requires route and an Ed25519 signing key")
	}
	if config.Cursor.MinimumSchemaVersion == 0 {
		config.Cursor.MinimumSchemaVersion = 1
	}
	if config.Cursor.MaximumSchemaVersion == 0 {
		config.Cursor.MaximumSchemaVersion = 1
	}
	for _, dir := range []string{"outbox", "inbox"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			return nil, err
		}
	}
	c := &Client{root: root, config: config, state: persistentState{NextSequence: 1, Cursor: config.Cursor, AcceptedSequences: make(map[uint64]bool)}}
	path := filepath.Join(root, "state.json")
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &c.state); err != nil {
			return nil, fmt.Errorf("read cloudsync state: %w", err)
		}
		if c.state.AcceptedSequences == nil {
			c.state.AcceptedSequences = make(map[uint64]bool)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := writeJSONAtomic(path, c.state); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	// Immutable rows are durable before sequence state. Recover that producer
	// prefix after a crash between row fsync and state replacement; never reuse
	// a persisted sequence for a different event (core563).
	rows, err := c.outbox()
	if err != nil {
		return nil, err
	}
	var last uint64
	keys := map[string]ed25519.PublicKey{}
	for id, key := range config.TrustedKeys {
		keys[id] = key
	}
	keys[config.SigningKeyID] = config.SigningKey.Public().(ed25519.PublicKey)
	for _, row := range rows {
		if row.ProducerSequence != last+1 || row.TenantID != config.TenantID || row.ProjectID != config.ProjectID || row.SchemaVersion != 1 || validateEnvelopeFields(row) != nil || !Verify(row, keys) || validatePayload(row.EventType, row.Payload) != nil {
			return nil, fmt.Errorf("invalid immutable producer prefix")
		}
		last = row.ProducerSequence
	}
	if c.state.AcknowledgedSequence > last || c.state.NextSequence > last+1 {
		return nil, fmt.Errorf("producer state does not match immutable outbox")
	}
	if c.state.NextSequence != last+1 {
		c.state.NextSequence = last + 1
		if err := c.save(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// PendingChecked preserves filesystem/decoding errors instead of reporting an
// empty queue. The project CLI holds a cross-process lock around mutations.
func (c *Client) PendingChecked() ([]Envelope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending()
}
func (c *Client) AcknowledgedSequence() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.AcknowledgedSequence
}

func (c *Client) Enqueue(eventType, idempotencyKey string, payload any) (Envelope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode payload: %w", err)
	}
	if err := validatePayload(eventType, raw); err != nil {
		return Envelope{}, err
	}
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return Envelope{}, errors.New("idempotency key must contain 1..256 characters")
	}
	if c.state.NextSequence > 9007199254740991 {
		return Envelope{}, errors.New("producer sequence exhausted")
	}
	// A stable idempotency key always resolves to the original immutable row.
	if old, ok, err := c.findIdempotency(idempotencyKey); err != nil {
		return Envelope{}, err
	} else if ok {
		return old, nil
	}
	e := Envelope{SchemaVersion: 1, EventType: eventType, TenantID: c.config.TenantID, ProjectID: c.config.ProjectID, EventID: ulid.New(), ProducerSequence: c.state.NextSequence, OccurredAt: time.Now().UTC(), IdempotencyKey: idempotencyKey, ProducerVersion: c.config.ProducerVersion, Payload: raw, Integrity: Integrity{Algorithm: "Ed25519", KeyID: c.config.SigningKeyID}}
	if err := Sign(&e, c.config.SigningKey); err != nil {
		return Envelope{}, err
	}
	if err := createJSON(filepath.Join(c.root, "outbox", fmt.Sprintf("%020d-%s.json", e.ProducerSequence, e.EventID)), e); err != nil {
		return Envelope{}, err
	}
	c.state.NextSequence++
	if err := c.save(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func (c *Client) Pending() []Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, _ := c.pending()
	return rows
}

func (c *Client) Cursor() Cursor {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneCursor(c.state.Cursor)
}

type Outcome string

const (
	OutcomeAccepted     Outcome = "accept-delayed"
	OutcomeReordered    Outcome = "accept-reordered"
	OutcomeDuplicate    Outcome = "ignore-duplicate"
	OutcomeIncompatible Outcome = "refuse-incompatible"
	OutcomeReplay       Outcome = "refuse-replay"
	OutcomeTampered     Outcome = "refuse-tampered"
)

func (c *Client) Receive(e Envelope) (Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Routing and authenticity precede every state lookup or write. This order
	// prevents forged incompatible envelopes from becoming a state oracle.
	if e.TenantID != c.config.TenantID || e.ProjectID != c.config.ProjectID || !Verify(e, c.config.TrustedKeys) {
		return OutcomeTampered, ErrInvalidSignature
	}
	if e.SchemaVersion < c.state.Cursor.MinimumSchemaVersion || e.SchemaVersion > c.state.Cursor.MaximumSchemaVersion {
		return OutcomeIncompatible, ErrUnsupportedVersion
	}
	if err := validateEnvelopeFields(e); err != nil {
		return OutcomeTampered, err
	}
	if err := validatePayload(e.EventType, e.Payload); err != nil {
		return OutcomeTampered, err
	}
	if contains(c.state.Cursor.SeenEventIDs, e.EventID) || contains(c.state.Cursor.SeenIdempotency, e.IdempotencyKey) {
		return OutcomeDuplicate, nil
	}
	if e.ProducerSequence < c.state.Cursor.ReplayFloor {
		return OutcomeReplay, ErrReplay
	}
	outcome := OutcomeAccepted
	if e.ProducerSequence <= c.state.Cursor.LastSequence {
		outcome = OutcomeReordered
	}
	if err := c.persistInbound(e); err != nil {
		return "", err
	}
	old := c.state
	old.Cursor = cloneCursor(c.state.Cursor)
	old.AcceptedSequences = cloneSequences(c.state.AcceptedSequences)
	c.state.Cursor.SeenEventIDs = append(c.state.Cursor.SeenEventIDs, e.EventID)
	c.state.Cursor.SeenIdempotency = append(c.state.Cursor.SeenIdempotency, e.IdempotencyKey)
	c.state.AcceptedSequences[e.ProducerSequence] = true
	for c.state.AcceptedSequences[c.state.Cursor.LastSequence+1] {
		c.state.Cursor.LastSequence++
		delete(c.state.AcceptedSequences, c.state.Cursor.LastSequence)
	}
	if err := c.save(); err != nil {
		c.state = old
		return "", err
	}
	return outcome, nil
}

// validateEnvelopeFields enforces the required v1 envelope fields before an
// event can influence replay state or become a filesystem path. Keep these
// bounds in step with envelope.schema.json.
func validateEnvelopeFields(e Envelope) error {
	if e.TenantID == "" || len(e.TenantID) > 128 || e.ProjectID == "" || len(e.ProjectID) > 128 {
		return fmt.Errorf("%w: tenant_id and project_id must contain 1..128 bytes", ErrInvalidPayload)
	}
	if !ulid.Valid(e.EventID) {
		return fmt.Errorf("%w: event_id must be a 26-character ULID", ErrInvalidPayload)
	}
	if e.ProducerSequence == 0 || e.ProducerSequence > 9007199254740991 {
		return fmt.Errorf("%w: producer_sequence is outside the v1 range", ErrInvalidPayload)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at is required", ErrInvalidPayload)
	}
	if e.IdempotencyKey == "" || len(e.IdempotencyKey) > 256 {
		return fmt.Errorf("%w: idempotency_key must contain 1..256 bytes", ErrInvalidPayload)
	}
	if e.ProducerVersion == "" || len(e.ProducerVersion) > 64 {
		return fmt.Errorf("%w: producer_version must contain 1..64 bytes", ErrInvalidPayload)
	}
	if e.Integrity.KeyID == "" || len(e.Integrity.KeyID) > 128 {
		return fmt.Errorf("%w: integrity.key_id must contain 1..128 bytes", ErrInvalidPayload)
	}
	return nil
}

type ExchangeResult struct {
	AcknowledgedSequence uint64
	Inbound              []Envelope
}

type Transport interface {
	Exchange(context.Context, []Envelope, Cursor) (ExchangeResult, error)
}

func (c *Client) Sync(ctx context.Context, transport Transport) error {
	c.mu.Lock()
	pending, err := c.pending()
	cursor := cloneCursor(c.state.Cursor)
	acknowledged := c.state.AcknowledgedSequence
	c.mu.Unlock()
	if err != nil {
		return err
	}
	result, err := transport.Exchange(ctx, pending, cursor)
	if err != nil {
		return err
	}
	maxSent := acknowledged
	if len(pending) > 0 {
		maxSent = pending[len(pending)-1].ProducerSequence
	}
	if result.AcknowledgedSequence > maxSent {
		return errors.New("remote acknowledged an unsent producer sequence")
	}
	for _, inbound := range result.Inbound {
		if _, err := c.Receive(inbound); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if result.AcknowledgedSequence > c.state.AcknowledgedSequence {
		c.state.AcknowledgedSequence = result.AcknowledgedSequence
		return c.save()
	}
	return nil
}

func Sign(e *Envelope, private ed25519.PrivateKey) error {
	if len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	e.Integrity.Algorithm = "Ed25519"
	e.Integrity.Signature = ""
	canonical, err := canonicalEnvelope(*e)
	if err != nil {
		return err
	}
	e.Integrity.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, canonical))
	return nil
}

func Verify(e Envelope, keys map[string]ed25519.PublicKey) bool {
	if e.Integrity.Algorithm != "Ed25519" {
		return false
	}
	public := keys[e.Integrity.KeyID]
	signature, err := base64.StdEncoding.DecodeString(e.Integrity.Signature)
	if err != nil || len(public) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	e.Integrity.Signature = ""
	canonical, err := canonicalEnvelope(e)
	return err == nil && ed25519.Verify(public, canonical, signature)
}

func canonicalEnvelope(e Envelope) ([]byte, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(raw)
}

// canonicalJSON covers the v1 schema's JSON domain (objects, arrays, strings,
// booleans, null, and integers). V1 deliberately has no floating-point field.
func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		encoded, _ := json.Marshal(v)
		out.Write(encoded)
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return errors.New("control-plane v1 does not permit floating-point numbers")
		}
		integer, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return err
		}
		out.WriteString(strconv.FormatInt(integer, 10))
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			if err := writeCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

func (c *Client) pending() ([]Envelope, error) {
	rows, err := c.outbox()
	if err != nil {
		return nil, err
	}
	result := rows[:0]
	for _, e := range rows {
		if e.ProducerSequence > c.state.AcknowledgedSequence {
			result = append(result, e)
		}
	}
	return result, nil
}

func (c *Client) outbox() ([]Envelope, error) {
	entries, err := os.ReadDir(filepath.Join(c.root, "outbox"))
	if err != nil {
		return nil, err
	}
	var result []Envelope
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.root, "outbox", entry.Name()))
		if err != nil {
			return nil, err
		}
		var e Envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ProducerSequence < result[j].ProducerSequence })
	return result, nil
}

func (c *Client) findIdempotency(key string) (Envelope, bool, error) {
	rows, err := c.outbox()
	if err != nil {
		return Envelope{}, false, err
	}
	for _, row := range rows {
		if row.IdempotencyKey == key {
			return row, true, nil
		}
	}
	return Envelope{}, false, nil
}

func (c *Client) persistInbound(e Envelope) error {
	if !ulid.Valid(e.EventID) {
		return fmt.Errorf("%w: invalid inbound event_id", ErrInvalidPayload)
	}
	path := filepath.Join(c.root, "inbox", e.EventID+".json")
	err := createJSON(path, e)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		return readErr
	}
	var old Envelope
	if json.Unmarshal(raw, &old) != nil || !bytes.Equal(mustJSON(old), mustJSON(e)) {
		return errors.New("inbound event id collides with different content")
	}
	return nil
}

func (c *Client) save() error { return writeJSONAtomic(filepath.Join(c.root, "state.json"), c.state) }

func createJSON(path string, value any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}

func writeJSONAtomic(path string, value any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloudsync-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	err = json.NewEncoder(tmp).Encode(value)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func cloneCursor(in Cursor) Cursor {
	in.SeenEventIDs = append([]string(nil), in.SeenEventIDs...)
	in.SeenIdempotency = append([]string(nil), in.SeenIdempotency...)
	return in
}

func cloneSequences(in map[uint64]bool) map[uint64]bool {
	out := make(map[uint64]bool, len(in))
	for sequence, accepted := range in {
		out[sequence] = accepted
	}
	return out
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

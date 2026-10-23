# Signed control-plane event protocol v1

This directory is the wire contract between a dacli workspace adapter and an
optional remote control plane. It exchanges allowlisted metadata only. The
local markdown workspace remains authoritative: inbound task, approval, and
policy events are proposals for the local Inbox and cannot directly mutate a
task, start an agent, grant a capability, or replace the append-only local
event log.

## Envelope and integrity

Each Outbox record is an immutable `envelope.schema.json` document whose
`payload` validates against the schema named by `event_type`. Producers assign
a strictly increasing `producer_sequence`; consumers persist the event and
idempotency indexes before acknowledging it. The signature is Ed25519 over
RFC 8785 canonical JSON of the complete envelope with `integrity.signature`
set to the empty string. Verification selects the tenant-scoped public key by
`integrity.key_id`; an unknown or revoked key is a tampered outcome.

The Inbox is append-only. Process in this order: verify tenant/project routing
and signature, negotiate schema compatibility, detect `event_id` or
`idempotency_key` duplicates, enforce the retained replay floor, then compare
the producer sequence. Gaps and reordered records may be stored and applied
when their object `base_version` matches. A mismatch is an optimistic-version
conflict requiring a new proposal; it never overwrites local state.

For local v1 consumers, the verified producer stream is the route tuple
`(tenant_id, project_id, integrity.key_id)`. Sequence cursors and gaps are
independent per stream; a project-wide numeric cursor is only a diagnostic
summary and never sequence identity authority. A duplicate requires exact
equality of the complete canonical signed envelope. Reuse of an event ID,
idempotency key, or same-stream sequence with different content is refused.
Consumers merge persisted per-stream cursors and gap reservations with verified
retained inbox rows before recovering state after restart. Exact identity
indexes are rebuilt from retained rows; the durable sequence reservations
survive payload expiry. A sequence already covered by a contiguous cursor or a
reserved gap cannot be reused after expiry. Because its original envelope is
no longer available for equality comparison, that attempt is refused as replay
rather than reported as an exact duplicate. Unseen lower gaps remain
admissible. A conflicting historical stream or a bad signature for a currently
trusted key refuses startup without changing history. An unknown or revoked
key's rows remain reserved and block that stream without advancing its
contiguous cursor, even when the retained sequences are consecutive. Persisted
state for a route-matching key that is no longer trusted also latches the
quarantine when its payload rows have expired. Such streams remain blocked if
the key is later configured again, until an explicit history reconciliation.
Legacy state-only event and idempotency reservations cannot establish exact
equality and therefore refuse matching new data. The HTTP `delivery_cursor` is
an independent opaque server continuation; it is not a producer sequence
cursor.

## Metadata allowlist

The payload schemas are closed with `additionalProperties: false`; their named
properties are the complete v1 allowlist. Source code, prompts, transcripts,
command output, environment names or values, credentials, tokens, and secrets
are therefore excluded by default. Adding any such class requires a new schema
version and an explicit disclosure policy. Implementations must also reject
unexpected nested fields rather than preserve or forward them.

## Compatibility, migration, and retention

- A v1 consumer accepts only schema version `1`. Unknown newer versions are
  retained as incompatible but are not interpreted or acknowledged as applied.
- Once a producer has emitted or a consumer has acknowledged version N, it
  refuses downgrade to a lower version. Migration emits a new immutable event;
  stored envelopes are never rewritten in place.
- Inbox envelopes and idempotency keys are retained for at least 90 days.
  Per-producer sequence cursors and the replay floor are retained indefinitely,
  including after payload expiry, so expired events cannot be replayed.
- Offline producers keep an ordered Outbox until acknowledgement. On reconnect
  they replay from the last acknowledged cursor with original ids, sequences,
  timestamps, and signatures. Delayed and reordered events remain deterministic;
  cursor advancement waits for gaps or an explicit operator-approved skip.
- Consumers reject events below the replay floor, invalid signatures, wrong
  tenant/project routing, and incompatible versions. They do not downgrade,
  reinterpret, or silently discard them.

The golden fixtures in `testdata` make duplicate, reordered, delayed, replayed,
incompatible, and tampered handling executable. Their outcomes are protocol
terms, not instructions to mutate the workspace.

## Payload registry and ownership

The envelope `event_type` enum, the schema files, and
`cloudsync.PayloadTypes` are one tested registry. A v1 producer must not emit a
type outside this table, and a consumer must not infer fields that are absent.

| Event type | Authority and intended direction |
|---|---|
| `device_registration` | Device identity metadata from identity service to the registered local device; never carries credentials. |
| `project_registration` | Explicit tenant/project binding shared with a local workspace; `environment_id` is opaque metadata, not environment variables. |
| `installation`, `repository` | GitHub App installation and selected-repository observations from the GitHub adapter. |
| `role_bundle`, `policy_bundle` | Versioned, signed governance inputs downloaded for local verification; receipt does not grant execution authority. |
| `task_proposal`, `approval` | Remote proposals and decisions projected into the local inbox; neither directly mutates a task or starts an agent. |
| `run_summary`, `event_summary`, `agent_state`, `gate_evidence`, `budget_state` | Privacy-filtered local observations uploaded for portfolio status and policy evaluation. |
| `sync_cursor` | Durable acknowledgement state exchanged by transport peers. |

Object payloads that include `version` use optimistic concurrency. An update is
eligible only when its declared base/current version agrees with the stored
object generation; a mismatch becomes a typed conflict and requires a newly
signed event. Envelope idempotency handles transport duplication and does not
turn a stale object update into a valid one.

Valid examples for every payload added after the original pilot contract live
in `testdata/payloads/valid.json`. Contract tests prove that the examples cover
the schema-required properties and are accepted by the runtime validator.

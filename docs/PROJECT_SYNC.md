# Connected project metadata sync

The local workspace remains authoritative. Connecting it selects a control-plane
route and synchronizes signed, allowlisted metadata; incoming envelopes remain
proposals in the local inbox. They cannot apply tasks or policy, start agents,
accept work, or grant local governance authority.

## Select an exact binding

Use a native device login as described in [device identity](DEVICE_IDENTITY.md).
The local owner selects every route field explicitly:

```sh
dacli project connect local-project --provider https://control.example \
  --tenant tenant-id --device device-id --remote-project remote-project \
  --environment environment-id --repository repository-id --json
dacli project sync local-project --json
dacli project status local-project --json
dacli project disconnect local-project --json
```

The server owner must already have configured the matching workspace, tenant,
project, environment and repository binding, public signing keys and current
account/device access. Flags compare against this authority; they do not create
membership or trust. Connect requires one unambiguous canonical Git repository,
a matching remote repository, compatible schema and exact route. An existing
binding cannot be silently replaced. A copied connection cannot authorize sync
or disconnect in another workspace or local project.

Only the OS credential store holds device secrets and signing keys. Connection,
transport, transition and health metadata live under
`.dacli/cloudsync/<local-project>/`. Project sync projects local task identities,
status and timestamps into the v1 event-summary DTO. Local titles, acceptance
prose, source, prompts, transcripts, environment values and credentials are not
part of that projection. Closed payload validation refuses extra fields.

## Offline delivery and reconnect

Sync signs and saves immutable outbound rows before attempting remote
authentication or exchange. A failed or cancelled request keeps those exact
rows. Later CLI invocations recover them and resume from the durable acknowledged
cursor. Only acknowledgments matching the event ID, idempotency key, sequence
and accepted/duplicate outcome advance a contiguous prefix. HTTP success alone
is insufficient. Each exchange sends at most 100 queued envelopes; remaining
rows stay pending for another sync.

Transport retries are bounded to three attempts. It honors valid `Retry-After`
delta seconds or HTTP dates up to 30 seconds. A longer delay, or one that cannot
fit the caller's deadline, returns unavailable while retaining the queue. Context
cancellation interrupts requests and waits. Response bodies and bearer values
are excluded from user-facing errors.

Signed inbound envelopes must match the tenant/project and trusted signing key,
supported schema, v1 field bounds and closed payload. Event IDs must be valid
ULIDs before they can become inbox paths. Malformed envelopes cannot advance
cursor state or write outside the inbox. Local acceptance remains a separate,
owner-authorized action.

Disconnect preserves a local tombstone and immutable generation history. It
queues a signed disconnect when the local signer is available; a later reconnect
signs and replays a transition that could not be queued at disconnect, in order. Reconnect accepts only
the same verified binding and advances the generation. It does not discard
pending transport rows. Local task commands remain usable while disconnected,
offline, or refused by the remote provider.

Human and JSON status report connection health, outbound acknowledgment and
inbound cursor, pending backlog, last success/failure and next action. A revoked
device reports a typed policy refusal and preserves queued rows; an offline
provider reports unavailable. Neither result establishes remote delivery.

## Local qualification

```sh
python3 scripts/qualify-project-sync.py --binary /path/to/extracted/dacli \
  --arch arm64 --evidence-dir /private/tmp/dacli-project-sync-evidence
bash scripts/qualify-hosted-postgres.sh /private/tmp/dacli-project-sync-postgres
```

Use a fresh private evidence directory for each run. Repeat the extracted
artifact check with `--arch amd64` on a host that can execute it; Apple Silicon
requires Rosetta. The CLI fixture exercises two projects, native Keychain,
response-loss/duplicate replay, malformed acknowledgment refusal, offline queue
recovery, binding-drift refusal, ordered disconnect/reconnect generations,
proposal-only inbound data, revocation and status. It scans captured output,
process arguments and local files for synthetic secrets and verifies exact
credential and child-process cleanup.

The PostgreSQL qualifier runs the production composed HTTP API and durable
client against disposable PostgreSQL 16.15 with a restricted application role.
It covers two-project bindings, partial retry, 100-item inbound pages, signed
cursor replay across runtime reopen, proposal admission and current device/member
revocation, alongside the existing crash/restore/RLS proofs. Required named tests
must exist; an omitted opt-in test cannot produce a passing qualification.

These are local proofs using controlled providers and synthetic identities.
They do not qualify an operator-selected external OAuth provider, production
service, deployment, or SLO. They publish no tag or release and do not dispatch
hosted CI. See [control-plane assembly](CONTROL_PLANE.md),
[API contracts](CONTROL_PLANE_API.md) and [trust boundaries](TRUST.md).

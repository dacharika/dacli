# ADR 0002: Define a local read-only operator protocol

- Status: accepted; Go transport and operator-read boundary implemented in #1147
- Date: 2026-10-25
- Program: [#1146](https://github.com/mlnomadpy/dacli/issues/1146)
- Prepared against: `8fdca475b4719a9b35949df4eebce66efdf1883c`

## Context

A native operator surface needs a stable, bounded contract for reading tasks,
attempts, activity, attention, and governance evidence. The contract must
preserve existing Go projections and their provenance. It must not create a
second source of truth or make display metadata into authorization.

The existing native dashboard reader authenticates a private scoped credential
at its read boundary. The Go feature producers own the other displayed facts.
A first native app has one consumer and can own a child process. No requirement
in #1146 establishes that another client needs a shared daemon or a persistent
multi-client listener.

## Decision

Define protocol major version 1 as a closed JSON frame contract under
`contracts/operator/v1/`. Keep the first host adapter app-owned stdio: the app
would launch a dacli-owned Go process, write one bounded JSON request per line,
and read one JSON response per line. Stdout would contain protocol frames only;
diagnostics would go to stderr. The adapter would call canonical Go producers
directly and would not shell out to existing command output.

The original ADR and fixtures did not implement the process, authenticate a
request, open a workspace, expose a projection, or authorize an operation. The
#1147 Go implementation adds the app-owned stdio handler, canonical projection
callbacks, and a distinct owner-provisioned `operator.read` identity. A valid
fixture still proves only contract shape. The transport authenticates at the
actual read boundary; process identity, parent process, app bundle, cwd,
hostname, executable name, or environment are not credentials.

Do not add a listener in the first adapter. Reconsider a private Unix socket
only when an independently owned client needs a shared persistent service or
multiple clients must share one process. A socket would also need safe
provisioning, directory and peer checks, stale socket handling, service
lifecycle, and replay/revocation behavior; a socket path alone is not identity.

## Existing producer inventory

| Read | Canonical producer | Boundary and caveat |
|---|---|---|
| `tasks.list`, `tasks.get` | `internal/features/dashboard/dashboard.go:buildTasks`; `detail.go:buildTaskDetail` and `summarizeTask` | Rows originate from `store.ListTasks`; detail includes acceptance, dependency, parent and task-log fields. Bound narrative fields; never copy arbitrary source files. |
| `activity.list` | `internal/features/dashboard/detail.go:buildEvents`, backed by `eventlog.List` | Existing dashboard behavior defaults to 50, caps at 500, validates cursor and reports partial/truncated/unreadable state. Preserve event IDs and source times. Event text is data, not trusted instructions. |
| `attempts.timeline` | `internal/features/dashboard/timeline.go:buildDeliveryTimeline` and `buildDeliveryAttempt` | Reads process-monitor records, phase journal, usage, verification evidence, and PR records. Malformed or project-mismatched journals are refused. The producer currently has no proven resumable page cursor; do not claim complete paging. |
| `attention.list` | `internal/features/dashboard/attention.go:buildOperatorAttention` and `native.go:buildNativeAttention` | Derived from workspace task/graph/loop facts and native governance attention. Alerts include observed times, freshness, evidence and confidence. A workspace-wide result must not be silently truncated; first adapter should require project scope and report partial state explicitly. |
| `governance.snapshot`, `evidence.list` | `internal/governance/sessions/native_dashboard.go:Authority.ReadOperatorDashboard` | Reads an immutable private authority snapshot; requires both `read` and the separately owner-issued `operator.read` action with exact workspace and project resources, reauthenticates before return, and bounds task filters to the canonical selected project. Records/attention have a deterministic 512-item cap and `limited` state. Sensitive credentials, commands, paths, prompts, transcripts and private bodies are excluded. Evidence eligibility is `historical-not-revalidated`. |

The `internal/governance/contracts` package validates portable governance
metadata only. It does not authenticate a host or replace the sessions
authority. Feature slices remain isolated; reusable projection code must move
to a lower shared layer rather than importing one feature slice into another.

## Wire contract

Each frame is one JSON object followed by a newline. Requests and responses carry
integer `schema_version: 1`; clients and servers require exact equality. There
is no implicit downgrade or optimistic acceptance of a future major. The
stdio adapter must cap a request line at 64 KiB and a response at 1 MiB. The
contract rejects malformed JSON, trailing JSON values, unknown fields,
unsupported operations, invalid scope IDs, and invalid limits without echoing
raw input or secret values.

Operations are `tasks.list`, `tasks.get`, `activity.list`,
`attempts.timeline`, `attention.list`, `governance.snapshot`, and `evidence.list`.
`evidence.list` is a filtered view of the same canonical `ReadDashboard` snapshot;
it is not a second evidence store. Requests state
the workspace and project explicitly; task reads state an exact task ID. A
future adapter resolves those IDs against the configured workspace and checks
the authenticated credential's actual workspace/project/task resources. A
host label is descriptive metadata and never a grant.

A successful response includes `schema_version`, `request_id`, `ok: true`,
resolved scope, top-level `observed_at`, source producer/schema/source
observation time, and canonical producer DTO data. Paged results include
`limit`, optional opaque `next_cursor`, `truncated`, and `partial`. Error
responses include `schema_version`, `request_id` when decoded, `ok: false`, and
stable `{code,message,retryable}`. A denied result must not reveal whether an
out-of-scope resource exists.

Request limits are positive and at most 500 when supplied; omission selects the
operation's documented default. Page response limits may be up to 512 only for
the existing governance snapshot cap. Do not silently clamp. Cursors are
producer-owned and must bind to workspace, project, filters, and the same
ordered snapshot. Until a producer supports resumption, return its bounded
result as explicitly partial/truncated or a typed `unsupported_pagination`
error; do not fabricate completeness. Preserve `historical`, `stale`, `unknown`,
and `limited` states as data. `observed_at` is when the Go producer assembled
the answer, not when its source event occurred. Reads do not refresh GitHub,
rerun preflight, poll a host, inspect child processes, or upgrade old evidence.

Stable error codes are `unsupported_version`, `invalid_request`,
`unknown_operation`, `unknown_field`, `denied`, `not_found`, `stale_source`,
`limit_exceeded`, `unsupported_pagination`, `storage_unavailable`, and
`internal`. A foreign workspace/project/task is reported as denied when a more
specific response would disclose it. Storage errors are retryable only when a
future implementation can identify a transient error class; otherwise they
are non-retryable.

## Authentication and provisioning boundary

The first adapter must use the same explicitly provisioned private native
connection and credential files as the native dashboard application. It must
validate the connection and authority schema before use, resolve the configured
workspace identity and root, and authenticate the bearer credential and exact
scope on every request. Credential revocation or expiry between requests must
be observed on the next request; authentication success is not cached.

Do not place bearer tokens in argv, environment, protocol errors, stdout, or
logs. Do not fall back to an owner credential when provisioning is missing.
Unsupported or incompatible connection/authority schemas return typed errors
and are not migrated implicitly. This protocol contract does not change native
authority, credential formats, or provisioning.

## Compatibility and privacy

The v1 request and response envelopes reject unknown fields, including nested
scope, source, page, filter, and error objects. This strict rule makes
incompatibility observable. Future compatible additions require an explicitly
versioned compatibility policy; changing response decoding to ignore unknown
fields is not an implicit v1 edit.

`data` is a canonical Go producer projection, not a separately maintained
operator model. Fixture data is representative, bounded, and redacted. It must
not contain credential material or hashes, commands, absolute profile or
authority paths, prompts, transcripts, raw event source paths, or private
evidence bodies. A contract fixture cannot prove an authority actually enforced
scope; that needs a later integration test against the real read boundary.

## Consequences and exclusions

The Go implementation supplies the versioned contract and fixtures, an owned
stdin/stdout process adapter, canonical projection dispatch, and explicit
operator reader provisioning without requiring Swift, Xcode, or Node. It does
not add a Unix socket, UI, task mutation, sync/acceptance operation, attempt
registration, lease acquisition, event write, scheduler, provider call, cloud
action, or authority migration. Native app launch integration and supported-Mac
qualification remain separate work; this Go slice does not claim either.

The contract tests should prove strict version/field/trailing-value rejection,
stable source and observation identities, error response shape, scope-denial
non-disclosure examples, stale-state preservation, privacy-field absence, and
Go-only execution. Real authorization, revocation, expiry, producer paging,
and redaction enforcement remain integration responsibilities and are not
claimed by these fixtures.

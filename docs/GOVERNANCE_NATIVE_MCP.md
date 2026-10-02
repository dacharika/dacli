# Native governance MCP

`dacli native serve --connection /absolute/private/connection.json` exposes
native session services directly. It does not wrap an agent CLI, dispatch the
legacy CLI executor, spawn providers, or require model authentication. It ignores
`DACLI_AGENT`. Missing/invalid explicit authority is a refusal, never local root.
The older `dacli mcp serve` remains a separate compatibility surface.

This authenticates a scoped session credential under cooperative local-file
integrity. It does not authenticate a human, host, or independently isolated
subagent, and it cannot prevent direct filesystem actions. Each request,
including catalog discovery and ping, reloads and authenticates the configured
session. Workspace reads additionally require `read` and the exact
`workspace:<workspace-id>` resource. Mutation methods enforce current scope,
attempt ownership, lease/fence and revision through the existing authority.
The connection is pinned at startup: changing its bindings requires restart;
credential files are read afresh. No credential or authority hash is returned.

## Private owner setup

Create a private directory outside tracked repository files. Owner provisioning
is local-only: neither native tools nor the legacy MCP CLI escape hatch expose
these commands. Owner configuration must be a regular OS-user-owned file with
mode 0600 or stricter, containing only:

```json
{
  "schema_version": 1,
  "workspace": "/absolute/workspace",
  "authority": "/absolute/private/authority.json",
  "installation_id": "owner-selected-installation",
  "owner_credential_file": "/absolute/private/owner.token"
}
```

The workspace must already exist at that exact root. Parent directories must
exist. Initialization never replaces an existing authority or credential:

```sh
dacli native bootstrap --config /absolute/private/owner.json
dacli native enroll --config /absolute/private/owner.json --task project/001 \
  --connection /absolute/private/session.json \
  --credential /absolute/private/session.token --path src --expires 1h
```

Enrollment reads the existing canonical task and repository, binds permission
to record attempts, and issues workspace reads plus task-scoped registration,
lease, revision and self-report actions. `--path` is repeatable and adds exact
repository-relative path resources; it does not grant a wildcard. Omitting paths
still permits task-only leases. Canonical readiness is checked later through
`application.NewLeaseAdmission`; recording admission is not task readiness.
Enrollment does not spawn an agent or mint trusted runner/reviewer authority.
Use distinct credentials and MCP instances for independently acting sessions.

Owner and session secrets are written only into newly created private files,
never argv, stdout, tool arguments or diagnostics. Existing output paths refuse.
Config and credential reads reject final-component symlinks, nonregular files,
other owners, permissive modes and oversized contents. Verified POSIX adapters
are required; other platforms fail closed. Protect parent directories against
other local writers. These checks are cooperative hygiene, not hostile-user
confinement. If issuance is interrupted or a private file write fails, preserve
all authority and private files for owner reconciliation; never retry by resetting
storage. The commands do not silently migrate old schemas. Use the exact owner migration
below before opening a schema-5 authority with the current binary.

Enrollment writes this connection shape (the secret stays in a separate file):

```json
{
  "schema_version": 1,
  "workspace": "/absolute/workspace",
  "authority": "/absolute/private/authority.json",
  "workspace_id": "workspace-id-from-storage",
  "installation_id": "owner-selected-installation",
  "session_id": "issued-session-id",
  "credential_file": "/absolute/private/session.token"
}
```

## Explicit schema-5-to-6 upgrade

For an existing schema-5 profile, preserve the owner configuration, authority,
connection and credential files. Run this local owner command once:

```sh
dacli native upgrade --config /absolute/private/owner.json
```

It authenticates the explicit private owner credential and migrates exactly
schema 5 to 6 through `Authority.UpgradePublication`. Existing sessions,
attempts, leases, revisions, evidence, reviews and credential files survive.
Restart the native MCP process using the same connection and credential.
An already-schema-6, unsupported or malformed authority refuses with exit 3 and
preserves authority bytes. Missing or forged owner files also refuse. Preserve
storage for owner reconciliation; never bootstrap, reset or downgrade it as a
retry. Older schema-5 binaries cannot write the upgraded authority.
This command has no token arguments, is excluded from both MCP surfaces, and
never takes migration authority from `DACLI_AGENT`. Older source schemas need
their separately documented owner API migrations; this command is not a generic
version selector or an automatic startup upgrade.

## Owner recovery and revocation

Revoke the exact `session_id` recorded in the private connection file:

```sh
dacli native revoke --config /absolute/private/owner.json --session <session-id>
```

This command requires the explicit private owner credential, independently of
`DACLI_AGENT`. It exposes no credential flags and prints no credential. Missing,
forged, or unavailable owner authority and unknown session IDs refuse with exit
3. Revoking an already-revoked session is idempotent. The command is local-only
and excluded from both MCP surfaces. It preserves credentials and connection
files for reconciliation; it does not stop a host process or undo prior work.
An already-running server refuses its next discovery or tool request after
revocation. For recovery, enroll a new scoped session into new private files and
restart the client with that connection; never reset authority storage.

## Protocol and tools

Use MCP `2025-06-18` over newline-framed stdio. Initialize before discovery or
calls. If the client offers another version, initialization responds with the
supported `2025-06-18` version so the client can decide whether to continue.
Optional protocol `_meta` (including `progressToken`) is accepted and ignored;
it is never an identity or credential. Tool argument objects remain strict.
Request IDs are non-null strings or integer numbers. Incoming frames are
bounded to 1 MiB. Supported methods are `initialize`,
`ping`, `notifications/initialized`, `tools/list`, and `tools/call`.

The exact tools are `identity`, `workspace_status`, `task_list`, `task_get`,
`register_attempt`, `acquire_lease`, `renew_lease`, `release_lease`,
`publish_revision`, and `submit_evidence`. Discover their typed schemas rather
than guessing arguments. Every tool requires `schema_version: 1`; unknown
fields, absent required fields and unsupported versions refuse. Lifecycle tools
use a typed `request` object matching the existing shared request field names
(for example `OperationKey`, `AttemptID`, `LeaseID`, `ExpectedFence`). Time values
are RFC3339 strings. Scope and operation identity are derived from credentials;
callers cannot supply a replacement authenticated session.

Successes and refusals have tool-specific object output schemas and are checked
against the emitted schema before returning. `structuredContent` and the JSON
text fallback represent the same object. Refusals contain a stable `refused`
code and `exit_code: 3`, without internal storage diagnostics. An unchanged
refusal must not be retried. Session authentication failures at the protocol
boundary use JSON-RPC error `-32003`; malformed requests use protocol errors.
Session-file startup failures exit 3 before processing input.

There is no CLI/shell escape hatch, owner enrollment, policy/grant administration,
provider spawning, acceptance or unconfigured verification execution in this
catalog. Self-report evidence always stays self-reported. Trusted verification,
independent reviewer integration, consumption and task acceptance are not exposed
by this transport. A successful protocol call is not proof of task acceptance,
strong host enforcement, or execution by Codex.

Tests use real committed Git fixtures and explicit private session files. They
exercise lifecycle bindings, dirty revision refusal, revocation during a server
lifetime, wrong scope/session/workspace, forbidden tools, private file rejection,
typed output/fallback parity and legacy-transport owner-command exclusion.

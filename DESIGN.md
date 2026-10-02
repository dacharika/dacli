# dacli — Design

## Product boundary

dacli is a governance layer for coding agents running in their native host.
The host owns agent creation, delegation, model choice, sandboxing and process
control. dacli owns authenticated transitions through its interfaces: canonical
task reads, checkout attempts, fenced leases, exact revisions, evidence and
owner-issued decisions. Coding-agent launch wrappers have been removed.

Codex is the first measured integration through the [native plugin](plugins/codex/README.md).
The protocol and authority kernel are host-neutral; second-host conformance
remains unproven. This is a cooperative local preview, not confinement against a
host process that can edit repository or authority files directly.

## Two kinds of durable state

Canonical projects, tasks, decisions and findings are Markdown with frontmatter
and wikilinks under `.dacli`. Task status is folder position. Context assembly
selects relevant records within a declared budget and announces omissions.
Events are append-only proposals; owner materialization uses serialized shared transitions for task
transitions instead of treating simultaneous Markdown edits as consensus.

Native credentials and authority live outside the repository in private state.
One locked aggregate owns session enrollment/revocation, attempts, leases,
evidence and review/publication decisions. Authentication and current binding
checks happen within the same serialized transition. Snapshot types and host
labels are not live authorization. Crash-stale locks refuse until deliberate
owner recovery; they are not stolen by guessing a process is dead.

## Identity and authority

An owner provisions an explicit workspace-bound installation and enrolls scoped
sessions. Native MCP uses only its private connection profile and reauthenticates
requests. No ambient legacy token or operator fallback grants native access.
Host child IDs and MCP metadata correlate observations; they do not authenticate
separate principals. Agents sharing a connection share one session identity.

An attempt identifies an external checkout and its observed revision, not a
spawned process. Leases have resource scope, expiry and fencing. Replaying an
old acquisition receipt is historical information, not a renewal or current
write permission. Clean committed revision publication checks the current lease.

Self-reported evidence records an agent assertion. Trusted verification needs an
owner-issued runner grant. Review needs an eligible independent credential family,
current canonical checklist/policy and exact revision evidence. A reviewer does
not acquire the worker's lease. Registering a review does not accept the task.
Owner-bound one-use publication decisions do not themselves publish or merge.

## Code boundaries

The native protocol lives in `internal/mcp/native`; explicit application adapters
connect canonical task/readiness observations to `internal/governance/sessions`.
Feature slices do not import each other. Shared policy and evidence readers live
below them, not in duplicate task-status stores.

The administrative CLI retains planning, context, review, bounded verification
commands, Git/GitHub operations and recoverable cleanup. An explicit test command
is not a model-provider launcher. Native MCP has no generic shell or CLI tool;
owner bootstrap/enrollment/revocation/migration remain local-only operations.

## Historical evidence and recovery

The evidence boundary is implemented and central to safe cleanup. Archived source
and retained receipts are recovery-critical records, not expendable caches.

Old run records, transcripts, structured review results, publication checkpoints
and sandbox projection receipts remain readable. They do not confer native
identity or permission to resume removed launchers. Unknown process identity or
missing terminal evidence protects source material from cleanup.

Cleanup previews exact evidence-bound plans. Eligible projected terminal claim
sandboxes can be archived recoverably only after byte and ancestry checks;
divergent/untracked content and referenced receipts stay protected. Restore must
not overwrite an existing source. Historical experiments remain explicitly dated
and version-bound rather than being rewritten as current support claims.

## What is still outside this delivery

Native final task acceptance, universal host interception, independently
authenticated child identity from shared host metadata, second-host conformance
and hosted multi-tenant management are not certified by the local plugin.
The owner controls exceptions, external publication and releases. See
[the architecture](docs/ARCHITECTURE.md), [native transport](docs/GOVERNANCE_NATIVE_MCP.md)
and [operator playbook](docs/OPERATOR_PLAYBOOK.md) for implemented boundaries.

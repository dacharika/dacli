---
name: governance-workflow
description: Govern native agent swarms and bounded loops with owner-enrolled scope, required verification, independent review and next-turn model routing through Codex.
---

Use the host's native coding and collaboration tools. Do not launch a nested
Codex CLI or use `dacli spawn` to establish identity.

1. Call `identity` with `schema_version: 1`. Confirm the enrolled session and
   scope. Missing, expired or revoked credentials are a refusal. Ask the owner
   to provision the explicit private native profile; never fall back to
   `DACLI_AGENT`, `--operator`, a legacy MCP server or audit-file edits.
2. Read `workspace_status`, `task_list` and `task_get` using their advertised
   schemas. Read CONTRIBUTING.md and AGENTS.md with native host tools. Canonical
   task records determine readiness; session labels and narratives do not.
3. Create or select a clean real Git checkout with native host tools, then call
   `register_attempt` with its absolute path and the canonical task ID. Call
   `acquire_lease` for the exact permitted resources before working. Preserve
   the returned attempt ID, lease ID and fence; renew with `renew_lease` before
   expiry. Historical receipts alone never authorize later work.
4. Make and commit changes through the authorized host workflow. Call
   `publish_revision` only for a clean committed checkout under a current lease.
   `submit_evidence` records an authenticated session's **self-report** bound to
   that revision. It does not turn a claimed exit code into trusted runner or
   CI evidence. Use unique operation keys and exact expected revision/fence.
5. If the owner has configured a separate runner and the catalog advertises
   verification tools, call `request_verification` for the named check and exact
   revision/lease/fence. Preserve the full operation identity. Use
   `get_verification_status` to observe progress and the final receipt; submission
   alone is not successful verification. Use `cancel_verification` to request
   cancellation, then inspect status. Keep the selected server connection alive
   until it records a terminal outcome: closing it requests cancellation. A known
   `interrupted` outcome has null evidence and an authenticated non-evidence
   `interruption` record binding the exact original request and runner. Its
   cooperative cleanup assurance is not successful verification or proof that
   escaped descendants stopped. Preserve its key and protection for owner recovery.
   An unknown restart remains pending (`reconciliation_required`) without an
   interruption record; never infer cleanup or change its key to reexecute uncertain
   work. These tools accept no command, credential, grant or directory overrides.
6. Keep the worker lease current while a separately provisioned reviewer checks
   the revision. Release with `release_lease` after review or when abandoning the
   work. Leave reviewer/runner enrollment and final acceptance to the owner;
   these authority-granting operations are not plugin tools.

For a separately owner-provisioned reviewer connection, inspect `identity` and
the canonical task using the read tools. Do not register an implementation
attempt or acquire the worker's lease. Call `propose_review` with the exact
attempt/revision/lease/fence, outcome, rationale and required trusted evidence
IDs. Then call `evaluate_review` for current eligibility. A replayed proposal is
historical; only fresh evaluation checks current policy, checklist, tree,
evidence and authority. Both approval and a request for changes require the
configured successful checks. A proposal or eligible observation never accepts,
merges or publishes the task. A worker's connection cannot substitute for the
reviewer's separate credentials and process.

Use `tools/list` for exact required fields, including `schema_version: 1` and
mutation `request` records. Policy refusals are final for unchanged requests.
Report uncertainty and recovery needs instead of substituting a CLI escape.

This 0.5.0 package exposes cooperative native session governance. All agents
sharing one MCP process share its enrolled identity; host labels, `_meta` and
thread/subagent IDs do not authenticate separate children or independent
reviewers. Independent sessions need separately provisioned credentials and
MCP processes. No hooks or containment guarantees are supplied. Direct host
filesystem/shell actions can bypass cooperative gates. Self-reports, successful
lease calls and plugin installation do not constitute acceptance or deployment
authorization.

For owner-provisioned orchestration, select a separate controller, bridge or
worker-handshake profile through `orchestration_config_file`. First call
`identity`, canonical reads and `get_orchestration_host_status`; then inspect the
role's `tools/list`. A profile or child label never grants authority. Requests
cannot override credentials, routes, grants, commands or models.

Use `plan_swarm`, `start_loop` and one bounded `step_loop` at a time. Preserve the
operation key, invocation, checkpoint and expected sequence. Only a fresh
`HostActionAuthorized` receipt permits its specific native host instruction.
Status/replay are historical facts. Use the host's actual native collaboration
tool for an authorized child action, with owner-provisioned independent private
connections; do not wrap or spawn coding CLIs. The bridge correlates the actual
child through `correlate_dispatch`. The child uses its own connection to register
its attempt and `acknowledge_dispatch`; shared sessions cannot impersonate it.
Runner and reviewer connections continue their independent named-check and
review workflows. `reconcile_loop` observes durable facts before advancing.
`pending_local_integration` requires the separate integration producer and never
means the whole loop succeeded.

`stop_loop` prevents future controller effects while retaining uncertain work.
`cancel_dispatch` requests cancellation. The bridge separately records
`acknowledge_cancellation` and `observe_dispatch`; inspect
`get_dispatch_lifecycle` for request, acknowledgement, terminal observation and
freshness. Lost heartbeat or disconnected host means unknown, not cancellation
or termination. Keep original uncertain-operation identities and protection;
recover through owner reconciliation rather than relaunching with another key.
Automatic dispatch/termination and unattended private child profile delivery
are not supplied. Other harness capability formats remain explicitly unsupported.

Use the owner-selected routing explanation in swarm/dispatch results when
choosing a permitted native child turn. Do not supply your own model override or
switch a running child's model. A harder-model correction requires the shared
bounded controller decision tied to its actual producer outcome; retained or
uncertain writers still block replacements. Historical records do not become
new host permits or provider availability/spend guarantees.

Local integration remains an owner operation through the native integration
adapter. Do not substitute raw Git operations, a source-tree green check or a
chat completion claim for its exact source/target/revision-bound outcome. When a
controller reports `pending_local_integration`, retain the durable identity and
wait for the authorized owner producer. Unknown apply effects require inspect
and reconcile; changing keys cannot make an uncertain effect safe.

A controller can call `get_dispatch_routing` with the pinned project and exact
dispatch ID or correction action operation key. Treat its selected role/model,
difficulty and explanation as a historical read for the next turn. It is never
an execution permit, actor replacement or mid-turn switch. Other profile roles
cannot borrow this tool. Use only freshly permitted native host instructions.

For required verification, preserve the full selected required-check set from the owner policy and every
check/grant/producer/revision/lease/fence identity. Submit and observe each named
check through the separately provisioned runner. Do not treat one successful
check, a self-report, or an unknown/pending check as full-set completion. Reviewer
and owner integration actions require the complete selected required successful set
for the same exact revision and their own independent authenticated connections.

---
name: governance-workflow
description: Use owner-enrolled native dacli sessions for scoped work, named verification or separately provisioned independent review through Codex.
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
   cancellation, then inspect status. An unknown or pending durable operation
   requires owner reconciliation; never change its key to reexecute uncertain
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

This 0.4.0 package exposes cooperative native session governance. All agents
sharing one MCP process share its enrolled identity; host labels, `_meta` and
thread/subagent IDs do not authenticate separate children or independent
reviewers. Independent sessions need separately provisioned credentials and
MCP processes. No hooks or containment guarantees are supplied. Direct host
filesystem/shell actions can bypass cooperative gates. Self-reports, successful
lease calls and plugin installation do not constitute acceptance or deployment
authorization.

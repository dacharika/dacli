---
name: governance-workflow
description: Use owner-enrolled native dacli sessions to inspect canonical tasks, register external checkout attempts, acquire leases and publish revision-bound self-reports through Codex.
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
5. Release with `release_lease` when done. Leave trusted verification enrollment,
   independent review and final acceptance to their separately authorized
   workflows. These authority-granting operations are not plugin tools.

Use `tools/list` for exact required fields, including `schema_version: 1` and
mutation `request` records. Policy refusals are final for unchanged requests.
Report uncertainty and recovery needs instead of substituting a CLI escape.

This 0.2.0 package exposes cooperative native session governance. All agents
sharing one MCP process share its enrolled identity; host labels, `_meta` and
thread/subagent IDs do not authenticate separate children or independent
reviewers. Independent sessions need separately provisioned credentials and
MCP processes. No hooks or containment guarantees are supplied. Direct host
filesystem/shell actions can bypass cooperative gates. Self-reports, successful
lease calls and plugin installation do not constitute acceptance or deployment
authorization.

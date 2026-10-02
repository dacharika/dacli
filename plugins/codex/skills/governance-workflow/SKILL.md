---
name: governance-workflow
description: Inspect a dacli workspace, coordinate task claims and evidence, and distinguish cooperative workspace tools from enforced native governance when working in Codex or another agent host.
---

Use the host's own coding and collaboration tools. dacli is the workspace record
and governance layer; this workflow does not require `dacli spawn` or parsing
agent transcripts.

1. Call the plugin's `whoami` with `schema_version: 1`. Confirm the returned
   identity and grant before using any mutation. Missing/invalid credentials
   are a refusal; do not use `--operator`, unset identity, or edit audit files
   to bypass it. If the server is unavailable, report setup as incomplete.
2. Call `status` and `get_context` with the schema version advertised by
   `tools/list`. Read the repository's CONTRIBUTING.md and AGENTS.md. Inspect
   existing tasks before proposing more; use explicit, observable acceptance
   criteria. Follow task and path ownership boundaries.
3. Work through native host tools. Use separate owner-provisioned credentials
   and separate MCP server instances for independently acting sessions. Native
   children sharing this server share its credential; host agent/session labels
   do not provide authenticated dacli identity. Do not claim they do.
4. Record decisions, findings and verification accurately. A read-only grant
   proposes events for the owner to materialize. A task checklist or an agent's
   narrative is not independently verified execution. Name actual commands,
   results and remaining uncertainty. Never approve your own review or claim
   revision-bound acceptance without the required authority and evidence.
5. Policy refusals (exit 3) are final for an unchanged request. Follow the
   stated recovery path. Leave final acceptance and publication to the owner
   or an explicitly authorized process.

This 0.1.0 preview connects the EXISTING cooperative workspace MCP surface.
Native scoped session enrollment, revision/lease/evidence tools, independent
review and atomic acceptance consumption are not exposed by this package yet.
Do not substitute a legacy agent token for the native authority credential.
Hooks are not bundled. With hooks disabled or unavailable, assurance remains
cooperative/advisory. Even enabled hooks are not a proven universal enforcement
boundary. Direct filesystem/shell actions can bypass the workspace MCP gates.
If a request needs unavailable guarantees, report it as unsupported and stop
that dependent action; keep independent authorized work moving.

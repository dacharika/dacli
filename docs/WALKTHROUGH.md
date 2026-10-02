# One native task, from scope to evidence

This walkthrough describes the current native host path. It does not start a
model or delegate work on the user's behalf.

## 1. Establish canonical work

In an initialized workspace, record one independently checkable task:

```bash
dacli project add "Ledger migration" --slug ledger
dacli task add "Cover the ledger write path" --project ledger \
  --accept "The named regression fails before the fix and passes afterward"
dacli task list --project ledger --json
dacli context ledger/001 --budget 4000
```

The task document is the canonical acceptance checklist. Status is its folder,
not a second status field in the native authority store.

## 2. Provision the plugin

The owner follows [private session setup](../plugins/codex/README.md). Bootstrap,
enrollment, revocation and migration are local owner operations. Credentials and
profiles stay outside the repository and plugin archive. The plugin serves only
the explicit enrolled session; it never falls back to legacy operator identity.

In the actual host, discover the native tools, inspect `identity`,
`workspace_status` and `task_get`, and verify the task scope before writing.
A successful provider login or a host child label is not dacli authentication.

## 3. Register work and hold a lease

The host or owner creates an isolated Git checkout. Use `register_attempt` for
that externally created clean checkout, then `acquire_lease` for the canonical
task and requested resources. The authority checks scope, readiness and overlap.
Renew before expiry; a stale lease or fence cannot authorize a new revision.
Do not infer takeover permission from a dead process or elapsed wall time.

## 4. Implement and verify in the host

The host uses its own native tools and sandbox. Commit the finished change, then
`publish_revision` binds the observed clean commit/tree to the attempt and lease.
Use `submit_evidence` for a revision-bound self-report. This records what the
agent reports; it does not certify that a trusted runner executed the checks.

[Trusted verification](GOVERNANCE_NATIVE_EVIDENCE.md) and
[independent review](GOVERNANCE_NATIVE_REVIEW.md) use separate owner grants.
Reviewers must have eligible independent credential families and observe the
exact revision and current policy. A reviewer does not acquire the worker lease.
Review proposal registration does not accept or close the task.

## 5. Keep delivery and publication explicit

The owner reviews exact-source test artifacts, current canonical acceptance,
GitHub checks and landing evidence through the established task/VCS commands.
Native one-use publication decisions are authorization records; they do not
publish a PR, merge a branch or consume final task acceptance.

Historical CLI `independent-review-result/v1` records remain readable with
`dacli review projection --task ledger/001`. They are not native review grants.
Preview public projection before publishing:

```bash
dacli github projection ledger --json
dacli github sync ledger --dry-run
```

Public-safe task fields are the default. Internal findings, decisions and
verdicts need explicit recorded authority. A passing self-report never closes an
issue automatically.

## 6. Stop and recover honestly

Revoke unneeded sessions with the owner's explicit `native revoke` command.
Future requests on existing connections must refuse. Preserve the private
authority and historical receipts; inspect cleanup's exact dry-run plan before
applying recoverable archival. Unknown or divergent contents remain protected.

The native host decides whether to begin another task. dacli no longer contains
a provider-driven perpetual loop or service supervisor.

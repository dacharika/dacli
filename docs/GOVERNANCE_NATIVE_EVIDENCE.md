# Fenced revisions and local verification evidence

This delivery provides records and execution, not independent review, task
acceptance, deployment authority or task closure. Those decisions must be made
by later owner/reviewer integrations. No task status database, provider registry
or process fixture is required.

Authorities now use schema 4. An owner explicitly calls `UpgradeEvidence` for
schema 3; `UpgradeLeases` for schema 2 and `Upgrade` for schema 1 migrate directly
to schema 4. Existing session IDs, hashes, revocations, bindings, attempts,
leases, operation receipts and global fences survive the schema 3 migration.
Opening never upgrades implicitly; old writers refuse the new version. Back up
and reconcile the authority under owner control before attempting a migration.

`PublishAttemptRevision` requires the owning attempt session's exact
`attempt.publish` action and `task:<id>` resource, current lease ID/fence and
expected previous revision sequence (zero initially). It directly observes the
registered checkout as clean and committed, verifies repository/locator identity,
and reauthenticates time-sensitive authority before publishing under the same
lock. Registration history stays immutable. Later revisions supersede the
eligibility of older evidence. Renewal remains identity-only and dirty-tolerant.

`SubmitEvidence` requires `evidence.submit` and the owning task scope and binds
the authenticated issuer, immutable current revision and active lease/fence.
Its provenance is always `self-reported`, including successful results and
claimed hashes. There is no caller trusted/provider/issuer field. Remote CI
execution identity is unsupported by this delivery; its claims remain
self-reported and cannot satisfy trusted local verification.

The owner separately enrolls a runner with `verification.run` and exact task
scope, then calls `GrantVerification` with the runner session, canonical task
and repository, named `CheckDescriptor` (command and deadline) and expiry.
Delegated sessions cannot receive or inherit grants. Grants are revocable and
bound to both command bytes and deadline via `CheckDigest`. Changing runner,
check, task, repository or descriptor does not preserve authority.

The trusted application owner constructs `NewNativeVerificationRunner` with
that private runner credential and its configured checks/grant IDs. Worker
requests supply their own credential, session, attempt, lease, fence, revision,
operation key and named check ID. Workers must have `verification.request` and
exact task scope. They cannot select shell text, a runner token, a PID, provider
or provenance. Constructor access is a trusted application boundary; ordinary
agent transports must not expose it as an operation.

Before execution, `BeginVerification` reserves the one-shot operation in the
authority aggregate. The command then runs outside the lock through
`store.RunAcceptanceVerificationRevisionContext`, preserving acceptance-grade semantics:
clean exact commit/tree before and after execution. A changed expected revision
is refused before executing the configured command. Its owner-configured timeout
also bounds contextual Git observations. `WaitDelay` bounds inherited output
pipes after cancellation or process exit. Output memory is bounded, hashed and
private; no artifact publication or task evidence append is performed.
Verification subprocesses discard inherited DACLI/MCP variables, token/secret/
credential variables and Git overrides. Contextual Git observations use the
same environment and disable fsmonitor; executable clean/process filters and
indexed submodules are refused before status. Legacy verification entrypoints
retain their behavior.

`CompleteVerification` checks the command/output digest, checkout, commit/tree,
current revision and lease/fence, worker session and ancestor authority, and
runner session, scopes, grant and expiry again under the durable lock. It
reobserves the checkout and repeats temporal authentication after observation.
Changes/revocations/expiry during execution cannot publish trusted successful
evidence. Nonzero command outcomes can be retained as distinctly failed records.
The read-only `EligibleVerification` observation is not independent review or
acceptance, and does not consume evidence or change any task status.

Exact authenticated operation retries recover immutable persisted records,
including across restart. Altered payloads refuse. An execution reserved without
a committed result returns `ErrReconcile`; retry never silently reruns a command
whose outcome may have been lost. Owners must investigate those unknown outcomes;
this slice does not automatically clear reservations. Historical receipts are
records, never renewed capabilities or proof of current eligibility.

A trusted issuer capability proves that the holder of an owner-enrolled local
runner credential submitted the observation. It does **not** cryptographically
prove native Codex, a specific host, CI execution, or protection against a hostile
OS user. Credentials, application construction and authority files require
cooperative local integrity. Commands can access files the OS permits; process
cancellation and inherited-pipe cleanup are not hostile descendant confinement.
The checkout observation is not a filesystem freeze. Cooperative isolation and
an owner-controlled command/configuration remain required.

Real Git tests execute checks and exercise revision advance, changed bytes,
commit changes, revocation and expiration during execution. Separate test
processes mutate authority while the command is paused outside the lock.
Additional tests cover output/check/descriptor substitutions, delegated grants,
issuer/scope substitution, replay, migration preservation and old writer refusal.

# Fenced revisions and local verification evidence

This delivery provides records and execution, not independent review, task
acceptance, deployment authority or task closure. Those decisions must be made
by later owner/reviewer integrations. No task status database, provider registry
or process fixture is required.

Evidence was introduced in schema 4; current authorities use schema 6.
An owner explicitly calls `UpgradeEvidence` for
schema 3; `UpgradeLeases` for schema 2 and `Upgrade` for schema 1 migrate directly
to the current schema. Existing session IDs, hashes, revocations, bindings, attempts,
leases, operation receipts and global fences survive the schema 3 migration.
Opening never upgrades implicitly; old writers refuse the new version. The local
`native upgrade` command supports exactly schema 5 to 6 as described in
[native MCP setup](GOVERNANCE_NATIVE_MCP.md#explicit-schema-5-to-6-upgrade). Back up
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

Async server shutdown requests cancellation and waits within the existing
six-second bound. A finished goroutine alone is not a durable outcome. If an
admitted job ends without a returned completion or an authenticated exact
completed/interrupted record, `Close` returns `ErrReconcile`, including on
repeated shutdown. A completed failed check and a durably recorded known
cancellation remain terminal observations. No successful evidence or
interruption is invented when persistence or authentication fails.

On Darwin, a second process-group cleanup signal can return `EPERM` when only
unreaped zombies remain. After reaping its direct child, the runner accepts that
specific result only if a bounded kernel snapshot of the exact owned group
shows no members or distinct nonleader zombies. A live or unknown state,
reappearing leader, mismatched identity, oversized snapshot or failed read
leaves cleanup unknown. Other Unix platforms retain their existing refusal.
This observation concerns the cooperative group at that instant; it does not
establish termination of descendants that escaped it.

Restoring storage access does not rerun or reset the original operation. Fresh
status, cancellation and request calls preserve `reconciliation_required` with
null evidence and interruption until explicit owner reconciliation. Closing a
fresh server with no admitted jobs makes no claim to resolve historical pending
operations.

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

## Private local verification setup

The owner can configure the existing runner without a coding-provider launcher.
First enroll an implementation session with `native enroll --verification` to
explicitly add `verification.request`. Existing sessions and ordinary enrollment
retain their narrower permissions. Publish the worker's clean committed revision
under its current lease before requesting verification.

Store named check definitions in a private regular file outside the repository:

```json
{
  "schema_version": 1,
  "checks": [{"id": "unit", "command": "go test ./...", "deadline": "2m"}]
}
```

Create a separate runner and its private profile using the explicit owner config:

```sh
dacli native verifier add --config /private/owner.json --task project/001 \
  --checks-file /private/checks.json --out /private/runner.json \
  --credential /private/runner.token --expires 1h
```

The task must already have an owner-attested binding. Setup reads that binding
without creating it or changing its admission policy. At most 32 check IDs are
allowed, and they must be unique; setup checks that the generated private profile
fits its 64 KiB read limit before issuing authority.
Deadlines must be positive and at most ten minutes. The runner is an independently
enrolled root session with only `verification.run` for the exact task. Each grant
binds the repository, named command bytes, deadline and expiry. Configuration and
credential files remain private; they are not part of the plugin package or worker
MCP connection. Do not give a worker the runner credential as its session identity.

The worker request file contains only the operation and exact revision bindings:

```json
{
  "schema_version": 1,
  "operation_key": "unit-first",
  "attempt_id": "issued-attempt-id",
  "lease_id": "issued-lease-id",
  "revision_id": "issued-revision-id",
  "check_id": "unit",
  "expected_fence": 1
}
```

```sh
dacli native verify --connection /private/worker.json \
  --runner-config /private/runner.json --request /private/request.json --json
```

The adapter derives the worker session from its authenticated connection and
requires matching workspace, installation and authority bindings. Requests cannot
select a command, working directory, issuer, credential, grant or provenance.
The output is a typed evidence receipt containing revision and output digests;
raw check output and private credentials are not printed. A failed check remains
failed evidence, and does not establish verification eligibility.

Both CLI commands remain local-only and excluded from both MCP surfaces. Separately owner-provisioned review profiles can reference these trusted receipts through the native review tools; self-reported evidence remains ineligible for that workflow. The
plugin's default ten-tool catalog submits only self-reported evidence. An owner
may enable asynchronous named verification by selecting this private runner
profile at native server startup with `--runner-config`, or by adding
`runner_config_file` to the plugin's private selection. This adds typed
`request_verification`, `get_verification_status` and `cancel_verification`
operations backed by the same profile validation and runner services; it does
not dispatch the CLI commands. See
[the asynchronous workflow](GOVERNANCE_NATIVE_MCP.md#protocol-and-tools).
Independent reviewer setup and canonical acceptance remain separate deliveries.
Owner-selected commands execute repository code with the local OS
user's permissions; private files are not hostile-code confinement.

Keep the exact operation key when recovering a lost response. A completed retry
returns its historical receipt without executing again. An interrupted reserved
operation with no committed result requires owner reconciliation, never an
automatic fresh key or rerun. Revoking the runner through `native revoke` prevents
future verification authority; it does not terminate the host or undo execution.

Setup validates descriptors and reserves new output files before issuance. It
never overwrites existing credentials. Session and grant issuance use durable
owner operations; they are not one atomic transaction with private file writes.
After issuance begins, an interrupted or failed setup preserves authority history
and private outputs, and reports nonsecret runner/grant IDs for reconciliation.
An incomplete profile fails closed. Preserve these files and revoke the reported
runner before deciding on a new setup; never reset the authority as recovery.

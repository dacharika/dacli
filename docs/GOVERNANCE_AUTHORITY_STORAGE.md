# Durable session authority storage

Sessions, credential hashes, revocations, owner task reference bindings and
external attempt records share one locked, atomically replaced aggregate.
Version 3 is the current schema. New bootstraps create version 3. Normal readers
and writers refuse versions 1 and 2 and unknown versions. An explicit `Upgrade(owner)`
operation accepts only version 1 and the matching owner bearer credential;
it retains binding, owner hash, session hashes, IDs and revocations exactly
and upgrades directly to version 3. UpgradeLeases(owner) separately upgrades
version 2 to version 3 and preserves task bindings and attempts as well.
Version 1 and 2 writers refuse version 3. Upgrade is not automatic on Open or auth.
Back up authority storage before the owner chooses a migration.

`BindTask` requires the owner credential and binds a canonical task ID to a
project and canonical local Git common-directory identity, with explicit
admission policy. It is a reference binding, not task-status storage. Agents cannot
create those references. Repository and project references are immutable;
the owner can change admission policy. Task IDs must be supplied canonically by the
trusted owner; no prefix resolution or ambient task discovery occurs here.

`RegisterAttempt` requires current session credentials with exact
`attempt.register` action and `task:<canonical ID>` resource. It observes the
explicit absolute checkout outside the authority lock, then revalidates auth,
expiry, ancestors, binding, scope, admission policy and repository identity while
recording the attempt under that lock. There is no snapshot-based write API.
Request IDs are authority-wide. Exact retries recover the original attempt and
observation time; changed session, request or revision is refused. Even retries
require current authority and a clean checkout. Errors redact paths and tokens.

The bounded local Git observer accepts a checkout root with committed HEAD,
a clean index and worktree including untracked files. Indexed submodules are refused. It records
canonical checkout locator, canonical local common Git directory, immutable
commit/tree object IDs and observation time. Remote URLs do not establish
repository identity. Relocating a repository requires a new owner task binding.
Unborn or unresolved HEAD and dirty checkouts are refused. Git subprocesses have
an overall five-second deadline, bounded stdout, and no inherited Git overrides.
This is a historical observation: concurrent external writers can change bytes
between observations. Registration neither freezes bytes nor grants a lease,
certifies host identity, launches providers, creates process records or changes
task status. Host labels are untrusted correlation observations only.

Cooperative single-authority limits remain: protect the authority directory and
Git checkout from hostile local writers. Atomic replacement and a shared
exclusive lock serialize participating writers, not arbitrary OS mutations.
File aliases are refused; directory aliases share canonical locks. Crash locks
are never stolen. Owners reconcile crashed writers before manual lock removal;
unknown external effects must not be replayed automatically. No provider,
network or external mutation runs under the authority lock.

Owner admission policy enables attempt recording; it does not attest to the legacy task ready frontier. Lease acquisition queries canonical task readiness separately through the trusted application adapter. The observer refuses repositories with configured clean/process filters and indexed submodules before status, disables fsmonitor, and bounds inherited output pipes with WaitDelay. Unauthenticated requests are denied before observation; current authority is rechecked inside persistence. These are cooperative observations, not protection against concurrent hostile config rewrites.


Durable fenced leases share this same aggregate. Acquire/Renew/Release authenticate
current session and registered attempt under its lock; complete resource sets,
operation receipts and the global fence counter commit together. The owner supplies
a trusted read-only admission adapter backed by ReadyFrontierForProject. Legacy
Markdown writers do not share this lock, so this is fresh graph observation rather
than strict concurrent-readiness linearization. Expired/revoked reservations remain
uncertain and cannot be silently reused. Only authenticated release frees them;
owner reconciliation of abandoned reservations is a later bounded delivery. See
internal/governance/sessions/README.md for exact scope, resource and retry semantics.

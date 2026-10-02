# Durable externally registered attempt leases

`Open(path, binding, admission)` selects an absolute authority file and an
owner-configured read-only `LeaseAdmission` adapter. Acquisition fails closed
without this adapter. Application owners construct it with
`application.NewLeaseAdmission(explicitWorkspace)`; callers cannot submit a
readiness boolean or an authenticated snapshot to lease mutation methods.
The adapter checks the complete workspace graph using
`store.ReadyFrontierForProject`. Legacy Markdown writers do not share the
sessions lock. Readiness is a fresh observation, not atomic admission against
concurrent legacy edits; shared-writer gating requires a later storage cutover.
Renewal deliberately does not require that started tasks remain open.

New authorities use schema 3. For existing schema 2 authorities the owner must
explicitly call `UpgradeLeases(ownerCredential)` before normal operations.
Sessions, credential hashes, revocations, task bindings and attempts survive.
`Upgrade` migrates schema 1 directly to schema 3. Unknown versions are refused;
opening never silently upgrades. Both migrations use the same durable lock.

`AcquireLease`, `RenewLease` and `ReleaseLease` authenticate the credential,
current session and ancestors and registered attempt ownership under that lock.
Actions are exactly `lease.acquire`, `lease.renew` and `lease.release`; task
scope is `task:<registered-task-id>`. Acquire additionally requires exact
`path:<relative-path>` or `checkout:` scope for every non-task resource. Binding,
repository, checkout and task identity come from persisted authority, never
host labels or request snapshots. No provider or process is launched.

Resource kinds are `task` (only the attempt's canonical task), `path` (repository
relative) and `checkout` (empty path). Every acquisition also reserves task
ownership, preventing sessions from splitting a task to evade exclusivity.
Paths must be clean, slash-separated lowercase ASCII names with letters,
digits, dash, underscore and dot. Absolute paths, traversal, globs, backslashes,
`.git` segments, symlink components and aliased checkout roots are refused.
Rejecting uppercase and non-ASCII names is conservative case-alias handling.
Missing path components are allowed for new files. Segment-prefix overlap is
checked over the entire sorted, deduplicated resource set in the registered
repository. A checkout has at most one unreleased lease; explicit whole-checkout
resources conservatively conflict with every lease in that repository.

Acquire allocates a monotonically increasing global uint64 fence; overflow is
refused. Renew preserves the fence. Renew/release must name the lease, owning
attempt and expected fence and refuse at expiry equality. Each stable operation
key stores its canonical request digest and receipt in the same atomic file
replacement. Keys are authority-wide: altered payloads, sessions or operations
are refused. Exact retry reauthenticates before returning identical historical
evidence, including after release; it never renews or reacquires implicitly.
A receipt is not a reusable authority capability. New mutation operations
check current state even if a caller still holds an old receipt.

Only authenticated release frees reservations. Expiry and revocation do not
prove a writer stopped and never silently transfer checkout or overlapping
resource ownership. A fresh externally registered checkout can reserve disjoint
resources under cooperative filesystem integrity and writer isolation. Recovery
of uncertain reservations requires owner reconciliation, deferred here; crashed
authority locks also remain conservative. Fences govern cooperating consumers;
this API does not stop an existing writer or protect against a hostile OS user
replacing authority files or changing symlinks between checks and writes.

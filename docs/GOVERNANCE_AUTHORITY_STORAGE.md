# Native authority storage decision

DACLI retains a hybrid authority model: canonical projects and task status stay
in the Markdown workspace, while native credentials, leases, decisions,
verification, review and producer histories stay in one private locked aggregate.
Status remains task-folder position. Cloud proposal v1 semantics do not change.
The storage experiment is a disposable projection, not a new writer or cutover.

## Decision and alternatives

| Model | Benefit | Constraint | Decision |
|---|---|---|---|
| Existing Markdown plus locked private aggregate | Preserves canonical IDs, owner workflows and current audit history; native transactions serialize credential/lease/decision changes | Markdown writers do not share the native lock; a repeated canonical readiness observation is not a transaction across both stores | Retain as current authority |
| Local transactional database | Could transact task, lease and decision changes together after all writers use one interface | Requires a verified importer, exclusive writer transition, recovery and compatibility rules; concurrent legacy direct writers would otherwise fork authority | No adoption or activation in this delivery |
| Hosted transactional authority | Could coordinate multiple machines under authenticated tenant boundaries | Requires the separately tracked hosted API, worker, tenant and synchronization contract | No local cutover; preserve cloud proposal v1 |

A future cutover must identify every writer, prove source identity/count/digest
reconciliation, stop conflicting direct writes and define recovery before it
activates a new authority. An imported projection cannot satisfy that transition.

## Versions and current observations

The installed runtime can support native authority through schema16. This is a
maximum supported format, not a claim that every workspace has migrated. The
owner's production authority remains schema10 during this delivery. Disposable
tests exercise a real schema6 bootstrap, explicit owner upgrades through16 and
nonempty session/controller history. No production migration is performed.

Normal operations never migrate storage. The existing explicit owner migration
routes preserve their required source-version checks and histories; unsupported
or already-target versions refuse with exit3. Owner credentials and private
configuration are required, never `DACLI_AGENT` or a host label. Preserve a private
backup before any separately chosen owner upgrade. Older binaries retain their
existing refusal behavior for formats they do not support.

## Owner observations and disposable import

```bash
dacli native storage observe --config <private-owner.json> --json
dacli native storage experiment --config <private-owner.json> \
  --destination <absolute-separate-private-directory> --mode projection-only --json
```

Each call rereads the same explicit owner profile and authenticates current owner
authority. Enrolled worker credentials cannot substitute. Public observations
contain actual schema, collection IDs/counts/digests, output-digest references and
retained protections; credential hashes, command bodies and raw private state are
excluded. The source aggregate is observed under its existing cooperative lock.
Its existing transaction loader is not a bounded whole-file I/O contract; the
additional snapshot read has its own size bound.

The experiment reads the complete bounded canonical task graph, verifies exact
raw task-body digests and imports into private files outside the live workspace.
It preserves original authority bytes inside a versioned snapshot envelope that
native authority readers reject, alongside task snapshots and a projection.
It reparses actual imported task identities/counts/body digests and recovers exact
authority bytes before writing its manifest. Public results identify the source
and file digests and always report `authoritative: false`.

Files publish using an atomic no-replace link. An existing file resumes only when
its exact expected bytes and private permissions match. Changed source, task
identity, imported bytes or an activation mode refuses. Four actual subprocess
fixtures interrupt after authority snapshot, task snapshot, projection and
manifest publication, then reopen and resume the same source without changing
live authority or canonical task bytes. Temporary publication hardlinks remain
for observation; the experiment never automatically deletes them.

## Cooperative concurrency and writer boundaries

Native transactions share one canonical authority path and exclusive lock. File
aliases are refused; directory aliases resolve to the same authority/lock paths.
Atomic replacement serializes participating native writers, not arbitrary local
filesystem mutations. Crash locks are not stolen automatically: an owner must
confirm that a writer has stopped before choosing lock reconciliation.

`BindTask` is an owner-attested immutable task/project/repository reference whose
admission setting is explicit; it stores no task-status authority. Attempt
registration observes a clean explicit Git checkout and reauthenticates its
binding before persistence. Fresh lease admission resolves canonical dependency
readiness separately. Expired or revoked leases can still protect uncertain
writers; authenticated release and owner recovery remain separate operations.

Markdown writers do not take the aggregate lock. The experiment repeats source
observations and refuses drift, but does not claim serializable cross-store
snapshots or protection from hostile local writers. It grants no activation,
merge, acceptance, push, deployment, process termination or release authority.
Retention is described in [artifact retention](GOVERNANCE_ARTIFACT_RETENTION.md).

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

The [2026-10-22 Dolt evaluation](research/DOLT_EVALUATION_2026_10_22.md) adds
Dolt as an explicit future candidate for versioned project knowledge, compared
with this model and SQLite. It does not select a new writer or change this
decision. Transaction/merge semantics, monotonic authority, permissions,
privacy, migration and measured growth must be qualified before any cutover.

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
Authority transaction reads and serialized replacements are bounded by
`sessions.MaxAuthorityBytes` (32 MiB). The owner observation uses the same cap.
An authority already over the cap, or a write whose full serialized result
would exceed it, is refused before replacement; the file is left intact. This
is a refusal boundary, not a retention policy. DACLI never prunes, expires, or
truncates operation, replay, accepted, consumption, or other audit histories to
fit the cap.

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

## Aggregate size and owner response

The 32 MiB maximum is a hard read/write boundary for the complete private
aggregate, including its JSON encoding. It does not promise that every aggregate
at or below the cap has the same latency on every disk or host. To reproduce the
fixture-only size and I/O measurements on the current Go toolchain, run:

```bash
go test -run '^$' -bench '^BenchmarkAuthorityGrowth' -benchtime=1x -count=1 ./internal/governance/sessions
```

The benchmark explicitly upgrades a temporary schema-6 authority to the current
schema and seeds receipt-bound pending-operation records with synthetic IDs and
repository data. It reports retained operation counts, serialized bytes,
owner-authenticated bounded-read cost, read-only transaction cost, no-op durable
write cost, and cooperative lock contention. Fixture setup and record creation
are excluded from timed measurements. It reads no owner profile or live
authority and performs no external actions. The table records one fixture-only
sample from Darwin arm64 (Apple M5 Pro), Go 1.26.5, source HEAD
`eea7e31c66b66fdd7db926974b52bd14dce31c80`, with a dirty-source manifest
SHA-256 of
`22cad5d620046a5f92cddec97fbbd1910823c6d1a0a991961c4cf2438a87f242`. The
manifest binds the benchmark and authority read, snapshot, and persistence
sources. The exact command, component digests, and raw output are preserved in a
local measurement receipt. These are descriptive single-run observations, not
a latency guarantee or a cross-host comparison.

| Retained synthetic operations | Serialized authority bytes | Authenticated read | Read-only transaction | No-op write/persist | Host / Go / source |
|---:|---:|---:|---:|---:|---|
| 100 | 405,221 | 6.628 ms | 2.180 ms | 15.815 ms | Darwin arm64; Go 1.26.5; manifest above |
| 1,000 | 4,037,621 | 57.245 ms | 22.948 ms | 36.103 ms | Darwin arm64; Go 1.26.5; manifest above |
| 5,000 | 20,181,621 | 275.039 ms | 104.342 ms | 148.711 ms | Darwin arm64; Go 1.26.5; manifest above |

A 10,000-operation fixture seed was also attempted. Its write transaction
returned the storage-unavailable refusal while adding the synthetic history;
the seed was not persisted and no timing or aggregate size is claimed for that
case. The exact failed run is kept separately in the measurement receipt. This
is consistent with the size cap, but it does not establish a universal maximum
record count: serialized size depends on retained history content.

At 1,000 retained operations, the custom interval from starting the second
transaction through its completion was 30.142 ms; the full serialized pair
benchmark iteration took 53.631 ms. Each benchmark used one iteration, so these
are reproducibility observations only. They do not qualify throughput or the
worst-case lock wait on other hosts.

When a real owner aggregate approaches or reaches the cap, stop operations that
would grow it and preserve the original private file byte-for-byte. Before any
owner-directed investigation or recovery, retain a separate immutable private
backup with restrictive permissions and verify its digest without publishing
the contents. Keep uncertain operations, replay receipts, accepted outcomes,
and consumption records in both copies. Do not edit JSON by hand, remove old
records, truncate the file, or retry a refused growth operation. There is no
general-purpose recovery command that safely shrinks this aggregate. A future
resolution must use a separately reviewed owner-approved design that preserves
the histories and recovery evidence; the current cap itself does not authorize
a migration or a new writer.

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

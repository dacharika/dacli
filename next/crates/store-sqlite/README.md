# SQLite task-transaction fixture

This adapter implements [dacli #1257](https://github.com/dacharika/dacli/issues/1257).
It is a library fixture for the new coordination core. The production command
kernel still reports `CreateTask` unavailable until an authenticated gateway is
implemented and qualified. Opening this store is not an authentication or
source-authority transition.

`store-contracts` owns the engine-neutral task command, result, error and budget
port. It uses `no_std` and `alloc`; no SQLite, filesystem, process or clock type
crosses that interface. This concrete adapter owns SQLite and monotonic clocks.
Another engine would need its own separately qualified implementation; no
automatic switching, SQLite/Dolt interchangeability or distributed transaction
is promised.

## The atomic command

`create_new` creates a new selected fixture file; `open_existing` refuses a wrong
application ID, schema version, format or authority identity before configuration
or canonical writes. Held handles recheck this identity inside each new write
transaction and checked read snapshot; a later schema/authority change is refused.
Neither opens a missing database through implicit creation,
repairs an unknown schema, discovers credentials or migrates the Go store.

A `CreateTask` validates bounded input and internally hashes a versioned,
length-prefixed encoding of authority/workspace/project/action, task ID, expected
version, title, exact body bytes and a sorted artifact-reference set. Digest
claims from a caller are never trusted. The operation key is scoped separately
by the store authority, workspace, project and action.

After `BEGIN IMMEDIATE`, replay lookup, the absent-task precondition, immutable
version, current task row, immutable audit event, stored operation result and
derived query row share one transaction. The same scoped key and digest returns
the original receipt; changed input refuses. A different key cannot recreate an
existing task. A successful receipt is returned only after SQLite reports a
successful commit. Audit/version/result rows reject ordinary update/delete.
This is a cooperating-store invariant, not protection against an OS actor who
can replace or alter every database file.

A failed commit returns `UnknownCommitOutcome` and makes the current writer
unusable for further writes. Reconciliation reads the same key and computed
digest through a fresh identity-checked connection. It reports committed,
absent or an integrity/conflict/error result; it does not silently use a new key.
Fresh fixture absence is not proof against whole-file rollback or an independent
external effect. Later authority/recovery slices must supply those witnesses.

## Reads, indexes and artifacts

Stable-ID reads check an optional expected version and verify the stored body
hash and the matching create-event input digest. Query-index rebuild verifies
canonical records before replacing the derived rows in one transaction. It does
not replay a global event log or rewrite corrupt canonical bodies. The fixture
supports at most 1,024 tasks, 256 KiB per task body, 16 artifact references and
1,024 returned query rows. These are bounded initial fixtures, not the production
paging/retention design.

`backup_to` uses SQLite's online backup API, including committed state that is
still in WAL, then verifies the destination identity and integrity. Existing
files, including the source or an existing alias, are never overwritten. A failed
backup preserves its newly reserved destination as unqualified output. It does
not package blobs, copy the main file alone or create a complete backup product.
See SQLite's [backup API](https://www.sqlite.org/backup.html) and
[WAL file rules](https://www.sqlite.org/wal.html).

Artifact references are retained even if a file is missing. After database reads
finish, `verify_artifact_files` inspects actual selected content-addressed files,
checks bounded regular-file bytes against their digests and reports missing,
corrupt or unreadable entries. On Unix it refuses a final symlink and opens
nonblocking before checking file type. It does not evaluate paths from task text,
read credentials, repair files or claim an OS sandbox against privileged actors.
Unqualified non-Unix platforms report references unreadable without opening files.
The integration test creates real content-addressed files, removes/corrupts them
and observes those failures after reopening the database backup.

## SQLite build and budgets

The workspace pins rusqlite 0.41.1 and its lockfile resolves libsqlite3-sys 0.39.1
with bundled SQLite 3.54.1. The published wrapper's declared Rust minimum is
1.89; this candidate is tested with the already installed `+1.97.1` toolchain.
This is a tested build requirement, not a final release MSRV policy. No global
Rust default is changed. The older workspace's 1.83 qualification is historical.
The patch includes [upstream FFI and PRAGMA corrections](https://github.com/rusqlite/rusqlite/compare/a60e76b5e86ee60442213efe79758a6c2ca8cf3d...18ee76dbb976d5ef7e37ad5a8fffae678ca21a78).

The adapter checks WAL, synchronous FULL and foreign-key settings after applying
them. SQLite 3.54.1 includes the documented
[WAL-reset fix](https://www.sqlite.org/wal.html#walreset), first fixed in 3.51.3
with named maintained backports. These tests do not reproduce that upstream race
or certify hardware/filesystem power-loss behavior.

A remaining budget becomes a local monotonic deadline. Expired budgets refuse
before a write; lock waits are bounded by SQLite busy timeout and the remaining
budget; the deadline is rechecked before commit. It is not a hard realtime bound
on OS open, sync, backup-step or commit latency. A completed commit is not called
rolled back because its reply arrives late. Pre-commit SQL failures return typed
busy/deadline/storage outcomes; uncertain commit errors remain unknown.

## Qualification

From `next`, with isolated `CARGO_TARGET_DIR`:

```sh
cargo +1.97.1 test --locked -p store-sqlite --test task_transaction
cargo +1.97.1 check --locked -p store-sqlite --lib --no-default-features
```

The transaction tests enable a development-only `qualification` feature. It
provides fixed before/after-commit checkpoints, a fixed SQLite commit refusal and
a SQLite page-capacity limit. No arbitrary plugin callback or ambient fault
variable enters a normal transaction. The separately named fixture executable
requires that feature and is absent from default library/production CLI builds.

The tests synchronize two independent processes before creating the same command,
kill actual child processes at both commit boundaries, reopen the database and
inspect all four canonical/result collections. SIGKILL proves application-process
interruption recovery on the tested host, not a power cut. The page limit causes
real `SQLITE_FULL`; it does not fill the host disk. Commit-hook refusal exercises
conservative unknown-outcome handling; it does not claim a physical I/O fault.

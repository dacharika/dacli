# Greenfield command kernel

The initial command kernel implements [#1256](https://github.com/dacharika/dacli/issues/1256),
based on the architecture in [#1255](https://github.com/dacharika/dacli/issues/1255).
It implements static protocol discovery and the common decoder, effect check,
context gate and typed result path. `CreateTask` is registered as unavailable.
The production CLI does not write tasks, start agents, execute plugins or replace the Go core.

The [scoped contract slice](design/scoped-contracts.md) adds a pure
`gateway-contracts` crate and a generated v2 metadata view from the same action
registry. It preserves the v1 transport/catalog and keeps all scoped v2 execution
unavailable. Bounded facts, prepared syntax and receipts are not authenticated
grants; the concrete verifier/transaction/API integration remains under #1258.

The [scoped policy slice](design/scoped-policy.md) adds pure current-fact,
lineage, delegation-attenuation and revocation predicates under [#1272](https://github.com/dacharika/dacli/issues/1272).
Its [qualification](design/scoped-policy-qualification.json) records 76 policy
tests, 54 named red/restored probes, integrated workspace and Go regression gates.
Policy success does not authenticate caller facts or enable scoped v2 execution;
credential verification and atomic backend integration remain under #1258.

## Run the slice

The current workspace uses the explicit Rust 1.97.1 qualification toolchain.
The installed default toolchain is unchanged. The final product minimum supported
Rust version remains a design decision; older compiler support is not claimed.
From this `next` directory:

```sh
cargo +1.97.1 test --locked -p core-kernel -p cli
cargo +1.97.1 run -p cli -- --help
printf '%s\n' '{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol"}}' | cargo +1.97.1 run -q -p cli
```

The last command returns the actual catalog as JSON. Action names, summaries,
effects, authentication requirements and availability in help and JSON come from
one kernel registration table. There is no separately maintained command list.
`--help` is a transport option, not a second domain dispatcher. Other arguments
are usage errors; credential values are not accepted in argv.

The CLI reads one complete JSON request from standard input with a 64 KiB byte
limit before JSON decoding. Duplicate and unknown fields, trailing documents,
invalid UTF-8 and context fields that do not apply are usage errors. It emits
one JSON result and preserves exit 0 (success), 2 (usage), 3 (final policy refusal)
and 1 (transport failure). This slice has no resource lookup that would produce
exit 4. Refused input is not automatically retried. Error output does not echo
raw input, credential bytes, parser details or unsupported arguments.

An explicit credential context with an empty token demonstrates a refusal:

```json
{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"credential","token":""}}
```

Missing, empty and whitespace-only credentials produce `empty_credential`.
Every nonempty credential produces `credential_verification_unavailable`:
there is no credential verifier or current dacli authority reuse here. Do not
submit actual credentials to this prototype. Tests use synthetic canaries only.

## The static-public discovery exception

The eventual authenticated command gateway remains the design for authority
operations. This first slice explicitly permits `public_protocol` only for
static public `InspectProtocol` metadata. It returns no workspace records,
source observations, evidence, identity, policy grants or current authority facts.
This is a deliberate bounded exception to the proposed architecture's blanket
“every route authenticated” wording, not an empty-credential fallback.

Supplying a credential context never falls through to public discovery, even for
this query. Public discovery cannot authorize the registered task mutation; it
returns `public_context_not_allowed`. The catalog still truthfully reports the
task writer as unavailable. No path treats a nonempty string as an authenticated
identity, an owner grant, a parent grant or an authorization decision.

A request's `effect` is untrusted. The kernel rejects an unknown classification
or a mismatch against its registered `ActionSpec` before invoking a handler.
Unregistered actions are final typed refusals. The successful query and refused
mutation share that exact path. An unavailable implementation also has a terminal
refusal arm rather than an empty successful handler.

## Compiler and verification boundaries

`core-types` and `core-kernel` use `#![no_std]`, `alloc` and
`#![forbid(unsafe_code)]`. The types crate has no dependencies; the kernel depends
only on the types crate. Neither has filesystem, process, database, networking,
clock, environment or UI access. `cli` owns standard I/O and JSON translation,
using exact direct versions and the committed Cargo lockfile. A standard library
or unsafe restriction is not an OS sandbox for a future external executor.

```sh
cargo +1.97.1 metadata --no-deps --format-version 1
cargo +1.97.1 tree -p core-kernel
cargo +1.97.1 fmt --all --check
cargo +1.97.1 test --locked -p core-kernel -p cli
```

Set `CARGO_TARGET_DIR` to an external build directory when working in a shared
checkout. Root-level Go gates remain separate from these focused Rust checks.
The scoped integration owner runs repository-wide gates before publication.

The semantic tests exercise actual CLI processes against in-process typed
outcomes, successful static discovery, missing/empty/unverified credentials,
unknown or mismatched effects, unknown mutations, strict context decoding,
bounded reads, output failures and redaction. A real initial failure showed that
Serde's internally tagged unit variant accepted extra context fields despite
`deny_unknown_fields`; the strict empty-struct variant fixes that behavior.
Causal checks also bypass the shared gate protecting successful discovery, so
an unavailable task handler cannot conceal an ineffective policy test.

The incident/rule dispositions and legacy command inventory live in `design/`.
Inventory coverage is not implementation coverage. Unresolved dispositions and
later slices prevent any claim that all dacli incidents have been solved.
The fixture transaction slice below is separately qualified. The real scoped
authenticator, production writes, graph, admission, effects, evidence,
observations, legacy import and cutover remain later slices. This prototype makes no legacy CLI/wire compatibility, production
activation, release or source-authority transition claim.


## Isolated task-transaction fixture

[#1257](https://github.com/dacharika/dacli/issues/1257) adds two distinct crates.
`store-contracts` defines engine-neutral transaction data, outcomes, budgets and
errors with `no_std` and `alloc`. `store-sqlite` implements an explicitly created
fixture database; it does not discover, open or migrate the Go workspace.
The pure command kernel still depends only on `core-types`; the CLI has no store
adapter and `CreateTask` remains unavailable until scoped authentication in
[#1258](https://github.com/dacharika/dacli/issues/1258).

One `BEGIN IMMEDIATE` transaction stores a task, its immutable version and event,
its operation result, and its derived query row. The store computes a canonical
input digest internally. A repeated operation key with identical input returns
the recorded result; changed input is refused. A failed or uncertain commit
leaves that writer unusable for further writes. Reconciliation observes the same
key through a fresh identity-checked connection; further writes require a
separately opened verified handle.
Held handles recheck fixture identity/schema inside transaction snapshots.
Query rebuilds verify canonical records rather than replaying events.

The fixture uses exact `rusqlite=0.41.1` with bundled SQLite 3.54.1. That pin is a
qualified implementation candidate, not a final database selection. The common
contracts permit a controlled comparison with Dolt and DoltLite in
[#1268](https://github.com/dacharika/dacli/issues/1268); see the
[storage decision](design/storage-engine-decision.md). Branching or merging data
cannot create valid runtime claims, leases, authentication or owner acceptance.
No second mutable task authority or automatic engine switching is introduced.

```sh
cargo +1.97.1 test --locked -p core-kernel -p cli -p store-contracts -p store-sqlite
cargo +1.97.1 clippy --locked -p store-contracts -p store-sqlite --all-targets -- -D warnings
cargo +1.97.1 build --locked -p store-sqlite --lib --no-default-features
```

The process fixture is excluded without the explicit `qualification` feature.
The actual test processes use barrier-synchronized duplicate writers and
SIGKILL before/after commit, and exercise replay, identity/schema drift, snapshot
counts, busy/deadline/page-capacity errors, unknown commit outcomes, query
rebuild and online backup. Artifact reads check real local files, content hashes
and final-component symlinks; the backup reports missing or corrupt artifacts.
See each crate's README for limits and
[transaction qualification](design/transaction-qualification.json)
and the historical [initial kernel report](design/kernel-qualification.json) for exact
source hashes, commands, causal failures and review scope.

This is not production task editing, privileged rollback protection, a complete
blob backup, a distributed SQLite service or power-loss proof. Current fixture
limits (1,024 tasks, 256 KiB body, 16 artifact references, 1,024 query rows) do not
qualify the eventual large-scale runtime. Existing Go behavior is unchanged.

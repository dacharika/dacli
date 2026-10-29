# Pure scoped command contracts

This implements bounded issue [#1271](https://github.com/dacharika/dacli/issues/1271)
from the [#1258 gateway plan](scoped-gateway-plan.md). Source anchor:
`574b08811ead5afa4f1c8c80e20b911279e9af76`; base:
`1142ab9cfc2d3aee57b8195c07c4ef8c9115ac45`.

## One registry, two explicit views

`core-kernel/src/registry.rs` generates action IDs, payload identities, effects,
permissions and metadata from one registration macro. V1 still exposes exactly
InspectProtocol and unavailable CreateTask, through its unchanged transport and
refusal path. The additive v2 metadata contains seven actions: InspectProtocol,
ReadTask, QueryTasks, ObserveCreateTask, CreateTask, DelegateSession and
RevokeSession. All seven remain unavailable; no v2 wire decoder or authenticated
dispatcher is supplied here.

Action discriminants are internal, not persistence or wire identities. Durable
grant framing sorts exact UTF-8 action names. Review caught ordinal-dependent
ordering; reverting that fix made the named canonical-order test fail, then the
restored code passed. Reordering registry declarations must not change logical
operation identity.

## Lower contracts

`gateway-contracts` is `no_std` plus `alloc`, forbids unsafe code and depends only
on pure kernel/types/task contracts. It owns bounded opaque identities, exact
scope, current credential/session/actor/ancestor/grant facts, validated prepared
commands, actor-bound operation identities, typed receipts and public outcomes.
Fact constructors validate shape and bounds. They do not authenticate an owner,
attest database freshness or prove that a credential comparison happened.

Canonical input uses explicit v2 domain/schema markers, checked length prefixes,
exact scope/actor/action/key and command semantics. ObserveCreateTask frames the
original CreateTask input without executing it. This crate supplies framing,
not hashing, cryptography, a schema migration or authority selection. V1 receipt
bytes/digest interpretation are not silently promoted to v2.

Raw credential borrows and private secret delivery have no Debug/Clone or
automatic serialization surface. Public receipt metadata is separated from
private delivery; a committed result with unavailable delivery is retained and
reported explicitly rather than paired with a fresh unrelated secret. These
types enforce shape, not cryptographic secret/receipt matching. The trusted
credential/transaction adapter must perform that matching.

`AtomicScopedBackend` accepts a prepared command, raw credential material and
operation budget. It accepts no caller-supplied facts, authenticated owner or
policy decision. Its implementer must authenticate current canonical rows and
admit replay/data access/writes in one selected transaction. The trait has no
default implementation and is a trusted composition contract, not an OS or
malicious-host boundary.

## Qualification and remaining work

The [qualification report](scoped-contracts-qualification.json) binds the exact
[31 Rust inputs](scoped-contracts.sources.json), focused checks, complete Rust
workspace gates, 15 named red/restored causal probes and separate required Go
regression gates. Ordinary independent host source/evidence review is not
authenticated dacli owner acceptance.

Pure policy, credential implementation, exclusive fixture enrollment, atomic
scoped SQLite admission, shared API/CLI wire-v2 transport and production
activation are still dependent work. No credential or canonical authority row
was created by this slice. The incident ledger retains 236 pending checks and
`allMistakesCovered=false`; these bounded tests do not promote unrelated ledger
rows or claim that all historical dacli defects are resolved.

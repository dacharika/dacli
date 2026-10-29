# Pure scoped authorization policy

This implements bounded [#1272](https://github.com/dacharika/dacli/issues/1272)
from the [#1258 gateway plan](scoped-gateway-plan.md). It consumes the
[scoped contracts](scoped-contracts.md) and adds `core-policy`, a `no_std` crate
that forbids unsafe code and depends only on lower contracts and the pure kernel.
The frozen source is `09880229800d3349635d3e247564fa4465e3e875`, originally based
on `574b08811ead5afa4f1c8c80e20b911279e9af76`. Its five source files are unchanged
in integrated source anchor `bebbfdd0d7108b8347cc5a048df14e6c2827e8eb`, based on
main `4dcbbd6e9c0e1f122f7406a03ed0fcffd9a24c53`.

## What the predicates check

`admit` checks the selected authority, matched credential ID/session/generation,
current actor linkage, exact authority/workspace/project scope and the explicit
registered action grant. Owner shape does not supply missing permissions.
The current session and every caller ancestor must be live at the supplied time.
Expiry is exclusive; future issuance is a typed invalid-lineage refusal.

Complete ancestors arrive immediate parent first. The chain must reach its root,
stay within eight ancestors, contain no repeated session or actor identity and
retain exact scope. Each issuing parent must have delegation permission. A child
cannot gain actions, predate its parent, outlive it or keep equal/greater remaining
delegation depth. `attenuate` independently performs the same current credential,
chain and explicit delegation checks before evaluating a proposed Agent child.
Owner actors cannot be delegated. Scope compares exact bytes without Unicode
normalization.

Revocation requires the actual target identifier, bounded complete target
lineage, exact scope and coherent overlapping caller/target rows and suffixes.
An explicitly permitted agent may revoke itself or a descendant; a root Owner
with an explicit revoke grant may remove another same-scope target. The caller
chain must be live. Structurally coherent revoked, expired or future-issued
targets remain removable; target liveness is deliberately not required.
Replay, no-op semantics and events belong to the future atomic backend.

## Authority and integration boundary

Every input is caller-supplied data. A matching fact object does not establish
that a verifier ran, rows are current, a clock is trusted, an Owner is enrolled
or a target exists in storage. Success is only `()`, never a reusable permit.
The backend port continues to accept raw credentials and prepared commands,
not policy results. Its trusted implementation must freshly authenticate and
load canonical facts in the same transaction as replay, reads and writes.

This crate is not wired into the fixture database, CLI or a production runtime.
All scoped v2 registry entries remain unavailable. The v1 transport/catalog and
Go implementation are unchanged. No canonical authority, native grant, profile,
release or task acceptance is created by the slice.

## Qualification

The [qualification report](scoped-policy-qualification.json) binds
[34 exact Rust inputs](scoped-policy.sources.json). The integrated candidate passed
Rust 1.97.1 formatting, locked offline workspace tests (116 checks: 112 runtime
and four compile-fail documentation checks), and all-target Clippy with warnings
denied. These include all 76 policy semantic tests. This run used default workspace
features; the separate SQLite process qualification feature was not rerun.
The installed default toolchain and product MSRV decision are unchanged.

Required Go formatting (empty output), vet, pinned golangci-lint 2.12.2 and
`go test -p 1 -timeout 20m ./...` were actually executed again on this candidate.
All passed. The same fixed checkout, seven-key child environment, public cache
and TMPDIR allowed 96 of 97 tested packages to use legitimate Go cache results;
one package executed and six packages have no tests. All 950 Go/module/config/
workflow inputs matched before and after. Earlier gate logs are not substituted
for the new candidate's results.

The worker recorded 54 named causal red/restored probes against these exact
source bytes: 48 guard bypasses that wrongly admit, three stable-refusal-cause
changes, one constructor-bound probe and two allowed-revocation-case probes.
They ran sequentially in the actual isolated feature worktree while an exclusive
build/delivery slot was held. Original bytes were retained in memory and restored
in `finally` for each probe; this was not a separate disposable-copy runner.
The integration lane audited the named failures, restored one-test successes
and source hashes; it did not rerun those mutations. Removing only shared-ancestor
row equality still refuses through suffix equality, so the reported effective
probe removes the overlap gate as a whole and claims no isolated suffix proof.

Ordinary independent host source/evidence review is reported with the PR handoff;
it is not authenticated dacli runner provenance or owner acceptance. The canonical
incident ledger still has 236 pending rows, ten unresolved decisions and
`allMistakesCovered=false`. This bounded predicate slice does not resolve the
remaining credential, enrollment, atomic backend, wire-v2 or activation work.

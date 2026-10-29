# Rust command-kernel migration ledger

Frozen Go source: `cd7210a02637696c3b082e2eb7e113101565d457`. This is issue #1256's proposed incident-invariant inventory. It records source observations and proposed dispositions, not reproduced historical incidents, implemented Rust adapters, authenticated approvals, or a compatibility promise.

`invariants.yaml` uses JSON syntax, valid YAML 1.2, so readers need no YAML dependency. Its 80 obligations cover the distinct rules in AGENTS.md, CONTRIBUTING.md, and the shared dispatcher/trust boundary. `command-inventory.json` assigns a disposition and proposed verification owner to all 156 current `Mutates` command paths: 148 literal declarations plus eight source-audited external/integration factory expansions. The path set equals the frozen Go capability drift-guard map. This is a migration snapshot, not a second runtime registry; new Rust help, protocol and effect output must come from the implementation's single ActionSpec catalog.

Every row names a check and its owner role. These roles are proposed responsibilities, not credentials or claims that anybody performed those checks. `coveredRuleCount` and `coveredMutatingCommandCount` mean **assigned an inventory row**, not behaviorally verified. All 236 rows remain planned or unimplemented. Ten decisions remain unresolved. `allMistakesCovered` is false and must remain false while these gaps exist.

## What the inventory establishes

- 84 mutating commands have exact first-cell TRUST table rows; 72 have command briefs and trust prose references. Both are retained with source lines. A path mention alone does not prove complete effects, authorization, rollback or truthful failure reporting.
- 86 paths use the exact explicit feature-authentication classifiers; 70 enter the shared legacy mutation authorizer. This was derived from classifier source, not inferred from a `native` name. For example `native controller refresh-review` still enters the shared legacy boundary; `accept apply-native` uses explicit native authentication.
- 18 commands declare a parsed effect-free dry-run shape. Preview is not a blanket credential bypass. Identity is resolved first; present-empty or unresolved inherited `DACLI_AGENT` refuses even during bootstrap or preview. Value-taking arguments cannot turn a filename `--dry-run` into permission.
- Legacy `Mutates` is the dispatcher RW classification. It intentionally excludes RO event/proposal appends and does not mean "performs no physical write." A complete new effect census must also cover non-Mutates commands before any all-writes-governed claim.

## Decisions requiring an accountable maintainer

The machine ledger names all ten blockers: Rust MSRV (`CON011`), `catalog`, `escalate`, `github release`, `init`, `provider readiness`, `report`, `run`, `ship`, and `taint`.

`init` documentation's unconditional bootstrap description is narrower in current code: unresolved inherited identity refuses. New Rust must not accidentally turn a missing token into owner authority. `report` documentation says the base action is RO-usable, while its current Mutates declaration gates the whole command before the handler. `escalate` has the same local-proposal versus remote-publication split and TRUST explicitly documents the contradiction. `taint` is classified mutating despite the source read-only walk. These are static source/documentation discrepancies; this audit did not execute those commands or file newly reproduced Go defects.

`catalog` and `ship` need explicit partial/external effect boundaries, and publication remains an exact maintainer choice separate from promotion. `run` cannot prove arbitrary shell effects from a self-declared effect label. Optional provider auth-status checks need an explicit process/credential boundary. Rust MSRV and support policy require a new decision rather than copying Go's compatibility floor.

Seventeen Go-specific native `upgrade` commands have a proposed **retire from new canonical surface** disposition. This does not remove existing Go commands or promise migration of their authority/state. Preserve their legacy adapter responsibility until an approved replacement exists. Likewise, folder-position task status and sync's legacy applied-field exception require explicit adapter translation, not undocumented copying into a new canonical store.

## First-slice limit

The new kernel may expose one public pure InspectProtocol query and refuse unavailable mutations through a shared ActionSpec decoder, policy and result model. No credential verification service, native authority migration, task writer, filesystem/process/store adapter, plugin execution or farm rendering is implemented by that boundary. CLI and in-process semantic tests can qualify specific new kernel invariants; they cannot turn this legacy inventory into "all historical mistakes covered."

Go toolchain, dashboard and coverage requirements remain applicable to changed legacy sources. Proposed Rust equivalents do not erase those obligations. This source-only documentation slice did not run Go gates or claim their results.

## Reproduce the static checks

From the `next` directory:

```sh
python3 design/build_ledger.py --source /absolute/path/to/frozen/dacli
python3 design/check_ledger.py --self-check
python3 design/check_ledger.py --require-covered
```

The generator requires the frozen Git HEAD and checks exact command-set equality against the source capability guard. It consumes the retained AST snapshot; `inventory_source.go.txt` is the original standard-library AST extraction helper, stored with a non-Go suffix to avoid adding a Go package to the repository. To refresh the snapshot deliberately, copy that helper to an isolated temporary `.go` file and run it against the same frozen source with an isolated Go cache, then review the resulting snapshot before regeneration. Neither helper executes dacli, imports its packages, reads credentials, or creates authority events.

`--self-check` rejects an in-memory forged `allMistakesCovered=true` claim. `--require-covered` currently exits **3** with a named unresolved/unverified refusal. This is an audit checker result, not evidence of running the new Rust kernel. `audit-validation.json` records the actual checker outcomes and exact artifact hashes.


## Isolated transaction fixture (#1257)

Issue #1257 separately qualifies an isolated create-only SQLite fixture: atomic
task/version/event/result persistence, scoped operation replay, process-interruption
recovery, checked reads and query rebuilds, typed failure outcomes, online database
backup and artifact-file diagnostics. Its [transaction qualification report](transaction-qualification.json)
records the exact source, toolchain, tests and limits. This evidence does not
verify the complete legacy obligations in this ledger, enable production
CreateTask, implement #1258 authentication, settle engine selection or resolve
CON011's product MSRV decision. All 236 pending rows, ten unresolved decisions,
runtimeVerifiedRuleCount=0 and allMistakesCovered=false remain unchanged.

The initialSliceBoundary and kernel-qualification.json describe the historical
#1256 candidate (Rust 1.83 and 12 tests). Storage effects excluded from that
candidate now exist only in the separately qualified fixture. Its historical
manifest/lock/README hashes are not the current workspace hashes.

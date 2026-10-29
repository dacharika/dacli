# Scoped command gateway proposal (#1258)

Status: proposed interfaces and acceptance slices, not an implemented authenticator. Source base is `dbab9e37e2de989ba326881d7235e1eee6043546` (merged #1257). This document changes no code, schema, manifest, credentials or ledger status. It addresses the cached [#1258 issue](https://github.com/dacharika/dacli/issues/1258); its exact input SHA-256 is `5c06cbf288752397bf3bb76051d590d02c42f1ed901881dc4496b966b231c333`.

The bounded implementation target is an explicitly created, local **new-authority fixture**. It can authenticate its own newly issued credentials and exercise scoped task commands through CLI and in-process API. It does not activate Go credentials, select a production authority, execute plugins, or provide an OS security boundary. Production owner enrollment and recovery remain separate decisions.

## 1. What the current source actually supplies

All line references below are at the source base, not a claim about a moving branch.

| Source | Existing boundary and required change |
| --- | --- |
| `core-types/src/lib.rs:33–50,81–118` | `Authentication` describes catalog requirements; `CredentialContext` is raw, redacted input. Neither proves a caller. Keep that distinction. |
| `core-kernel/src/lib.rs:25–48,67–103` | One registration table drives decode/effect/help/discovery. Every nonempty token is currently refused as unverified; `CreateTask` is unavailable. A new gateway must replace that unavailable seam deliberately, not bypass it through another handler list. |
| `store-contracts/src/lib.rs:25–39,68–105,212–231` | The task port has scope strings and a task command but **no authenticated actor or atomic authorization port**. Calling `create_task` after an API-side check would not satisfy #1258. |
| `store-sqlite/src/store.rs:191–223,235–313` | `BEGIN IMMEDIATE` covers replay lookup and task/version/event/result/query writes. Extend this transaction to load current credential/session/ancestor/revocation state and admit the command. Never put an externally supplied callback inside it. |
| `store-sqlite/src/store.rs:434–455,510–533` | Fresh reconciliation and held-handle schema/identity checks exist. Preserve them, add current authorization, and refuse old schema writers. |
| `store-sqlite/src/schema.sql:2–28` and `store.rs:574–596` | The operation namespace is workspace/project/action/key; its digest contains no actor because #1257 has no authentication. Authenticated replay needs ownership and attribution rather than treating an old receipt as authority. |
| `cli/src/lib.rs:10–43,63–71,132–183` | The v1 transport has bounded stdin, strict unknown/duplicate-field rejection, no secret argv and sanitized errors. It has no task payload or store composition. Preserve those properties while explicitly versioning a new payload. |
| `docs/TRUST.md:16–22,151–179` | Explicit workspace-bound credentials, private setup, verifier storage and independent public identities are existing design lessons. Legacy environment/root fallback is not new Rust enrollment. Host access remains an independent boundary. |

Paths in this table are relative to `next/crates/` except `docs/TRUST.md`. Ledger anchors are AG002/AG003/AG005/AG007, CON001/CON012/CON015–020, K001/K002/K005/K006/K008/K011/K012/K014/K015. They remain planned. The current ledger reports 80 source rules, 156 inventoried command paths, 236 pending checks, 10 unresolved decisions and `allMistakesCovered=false`; this plan does not reduce any count.

## 2. Proposed contracts to freeze before code

These are proposed semantic interfaces, not compiling declarations. Implementation names may change together in one reviewed contract change; the checks and ownership boundaries may not drift independently.

### Pure policy owns decisions, not credentials or I/O

`core-policy` uses `no_std`, `alloc`, bounded data and `forbid(unsafe_code)`. It has no database, clock, filesystem, environment, network, process or key-vault access. It consumes canonical facts supplied by the selected transaction adapter:

```rust
struct PolicyIntent {
    action: ActionId,        // resolved from the one kernel catalog
    scope: ExactScope,       // authority / workspace / project
    resource: Resource,      // task, session target, or project query
}
struct AuthoritySnapshot<'a> {
    authority_id: AuthorityId,
    epoch: u64,
    actor: &'a ActorRecord,
    session: &'a SessionRecord,
    ancestors: &'a [SessionRecord], // bounded, ordered, complete
    grants: &'a [Grant],
}
fn admit(snapshot: &AuthoritySnapshot<'_>,
         matched: &CredentialMatch, intent: &PolicyIntent,
         now: TrustedInstant) -> Result<AdmissionDecision, PolicyRefusal>;
fn attenuate(parent: &AuthoritySnapshot<'_>, child: &RequestedGrant,
             now: TrustedInstant) -> Result<NarrowedGrant, PolicyRefusal>;
```

`CredentialMatch` is a fact produced by fresh verifier comparison inside the adapter's transaction. It is never decoded from wire input. A pure function accepting test facts is not a security boundary: its result is an in-memory decision, not a reusable authorization token. The storage write API must not accept a serialized, cached or caller-manufactured `AdmissionDecision`. The adapter creates a private transaction-bound admission value after evaluation and consumes it before that transaction ends.

The kernel registry remains the single source for `ActionId`, payload kind, effect, required permission, public/static status, help and JSON discovery. A registration macro or equivalent one-table implementation must generate/resolve these consistently; do not introduce a second CLI/API allowlist. Pure policy can depend on these pure registry types and `store-contracts` scope data; the kernel must not depend on API, SQLite, credentials, clocks or policy adapters. Shared data moves down, not between feature implementations.

### API owns normalization; the selected backend owns atomic admission

```rust
struct Gateway<B> { backend: B } // private backend; no raw-store accessor
fn execute(&mut self, envelope: BoundedRequest,
           budget: OperationBudget) -> GatewayOutcome;
trait AtomicScopedBackend {
    fn execute_scoped(&mut self, command: &PreparedCommand,
                      credential: BorrowedSecret<'_>,
                      budget: OperationBudget) -> GatewayOutcome;
}
```

The port is engine-neutral. `api` owns this composition contract and can depend on kernel/policy/task contracts; the concrete SQLite adapter can implement it without `api` importing SQLite. CLI and any later transport invoke this exact API. Request data cannot choose another backend, verifier or authority file. Trusted composition supplies an explicitly selected new-fixture handle and expected authority identity. Missing composition returns the existing typed unavailable outcome, never ambient workspace discovery.

`PreparedCommand` is decoded payload plus catalog identity, not authenticated context. The backend revalidates catalog effect, selected identity and exact command scope; it does not trust a caller's `owner`, `grant`, `parent`, `verified`, `now` or `epoch` field. No wire schema contains those proof fields. A host linking the raw fixture store directly remains trusted host code with filesystem access; Rust type privacy does not confine it. Untrusted community code gets no such port in this slice.

### Credential adapter has a narrow fixture protocol

Propose opaque bearer credentials with independently generated public credential/session IDs and a 256-bit random secret. Roles, host labels, actor IDs and workspace names are never token material or proofs. The fixture parser accepts one versioned, bounded encoding (maximum 128 bytes), with exact length/encoding validation. It never treats arbitrary nonempty strings as valid. Raw token bytes stay in a secret wrapper without `Debug`, serialization, implicit `Clone` or error formatting; explicit secret handoff is a separate operation.

A trusted crypto adapter supplies generation, domain-separated verifier derivation and constant-time comparison using reviewed libraries. Canonical rows store a verifier and format/version, not reusable plaintext. The comparison binds selected authority ID, credential ID, session ID and credential generation. Parse/hash preparation outside the transaction is only a candidate; fresh canonical verifier comparison inside the transaction is mandatory. Unknown identifiers and invalid secrets have the same public invalid-credential result. Entropy/verifier failures fail closed. No network, profile lookup or user callback occurs while a transaction is held.

Exact crypto/entropy/secret-buffer dependencies and encodings need a small primary-source/pin review before implementation; this document does not invent a production authenticator or claim that wrapping a byte vector guarantees erasure from OS buffers, dumps or every parser allocation. A future production vault implements an explicit adapter; no environment or private Go profile is borrowed as its fallback.

## 3. New-fixture owner enrollment is an explicit setup capability

Enrollment is outside the unauthenticated command endpoint. A trusted local fixture creator calls `prepare_new_fixture` with an explicit new-authority creation intent, caller-selected workspace/project bindings and an entropy adapter, then `create_new_fixture(path, prepared)`. The authority ID is generated once in that prepared object; it is not also caller supplied. Create and subsequent explicit opens bind the resulting expected authority ID to the selected fixture. It uses exclusive creation; **every existing destination refuses**, including an empty file, v1 fixture, Go workspace, copied profile or file lacking an owner row. There is no `if no owner, become owner` branch on open or request admission.

The prepared enrollment generates separate random authority, owner actor, owner session and credential IDs. Only this setup operation can create `ActorKind::Owner`; it seeds an explicit permission set for the initial exact project scope. Owner kind itself grants no catalog bypass, future-project wildcard or permission to another authority. Normal delegation can never create an owner actor or replace the existing owner.

For the fixture library, the trusted creator retains the generated owner secret before the database commit attempt. The fixture CLI setup path requires an explicit dedicated private secret sink (for example, an already opened inherited output descriptor distinct from stdin, stdout and stderr); public descriptors 0/1/2 cannot be selected as that sink. It does not put credentials in argv, ordinary result JSON, stderr or a profile. Sink availability is checked before mutation. Database identity, owner actor/session, verifier, epoch and enrollment receipt commit together. Credential delivery and a database commit are not one atomic resource: if delivery/commit acknowledgement is uncertain, preserve the outcome and reconcile the same prepared enrollment identity. Never create a second owner, overwrite the database, print the secret as an error, or reuse an unrelated profile. Losing the only owner secret is a fail-closed fixture recovery limit, not an automatic enrollment trigger.

Opening a fixture requires an explicit selected path and expected identity supplied by trusted composition. Logical scope in a command cannot redirect that path. This does not solve malicious whole-file replacement, rollback, forked authority copies or owner recovery; those block production authority activation and belong to the storage/authority decision, including #1268.

## 4. Exact scope, delegation and revocation semantics

| Item | Proposed invariant |
| --- | --- |
| Identity | Opaque IDs identify canonical rows; independent public IDs are not credentials. Existing workspace/project/task strings retain bounded exact UTF-8 byte equality and SQLite binary comparison. No case folding, Unicode normalization, prefix/`LIKE`, path interpretation or role-name inference. |
| Scope | A session has one exact authority/workspace/project in this first slice. No cross-authority, cross-workspace or future-project wildcard. Reads apply this scope in SQL as well as in policy. |
| Permissions | An explicit bounded set of catalog action IDs, with their catalog effects. Read/write is not an ordered string that can be supplied by a caller. An owner has an enumerated set, not `allow_all`. A create permission need not imply permission to read arbitrary tasks. |
| Read-only session | Can use its granted `ReadTask`/`QueryTasks` actions only in its scope. It cannot create tasks, issue/revoke sessions, rebuild an index, append proposals, export, or claim a preview write. No canonical per-read log/cache mutation is disguised as a query. |
| Delegation | Parent is the currently verified session, never a parent ID claimed in the request. It must possess `DelegateSession`; requested child actions are a subset of its explicit grant, effects must equal catalog effects, and scope must equal/narrow the parent's exact scope. Child expiry is no later than the parent's. Owner kind cannot be delegated. Management permissions are delegated only if explicitly present/requested and within the same subset rule. |
| Lineage | Store a parent session link plus independent actor identity. Check every ancestor is present, belongs to the same authority/scope, is not revoked/expired and has sufficient grants. Refuse missing, cyclic, inconsistent or over-depth lineage. Proposed fixture depth limit: 8; exceeding it refuses. A name/link alone grants nothing. |
| Revocation | A session with `RevokeSession` can revoke itself or its descendants in its own scope; the enrolled owner can revoke sessions in its initial scope only with that explicit permission. It cannot revoke an unrelated authority's sessions. Parent revocation invalidates all descendants by current-chain checks. |
| Epoch | Revocation changes target state and increments an authority-wide monotonic epoch in the same transaction as its immutable auth event/result. Checked overflow refuses; the epoch never wraps or resets. The epoch invalidates cached facts; an old issued epoch alone does not invalidate every unrelated session. Every admission rereads current epoch and chain. Never use a cached context because its token was once verified. |
| Expiry | Trusted adapter time, not wire time. Refuse at `now >= expires_at`; check the session and all ancestors. Child expiry cannot exceed parent expiry or the configured fixture maximum. Proposed fixture maximum lifetime: one hour; owner recovery/renewal/product lifetimes are not thereby decided. Clock unavailable/regressing within a live adapter fails closed. |

A global epoch is not rollback protection. Cross-process/restart time rollback, restoring an old auth database, distributed clocks and revocation durability beyond the qualified SQLite commit model remain explicit limits. This slice does not add writes to read-only requests merely to persist a clock watermark.

## 5. Linearization and the one-transaction gate

For every mutation, including session issue/revocation and a replayed `CreateTask`, the backend performs:

1. Validate bounded syntax and registry metadata without loading authority. Prepare secret comparison material and, for issuance, entropy before taking a database lock. No authority exists in this preparation.
2. Begin `IMMEDIATE` on the one selected fixture database. Recheck application/schema/format and expected authority identity. Load credential verifier, session, actor, explicit grants, complete bounded ancestor chain and current revocation epoch from this transaction.
3. Compare the credential, sample trusted time and run pure policy against the normalized action/effect/scope/resource. A refusal returns before task lookup, prior receipt disclosure or canonical writes. A preverified/cache-produced context is never sufficient.
4. Compute the task input digest internally. Check operation ownership and matching canonical input, then task preconditions. Append the task/version/event/result/query rows with immutable actor/session/admission epoch/policy-version attribution in this same transaction. Session changes use the same transaction discipline and sanitized auth events.
5. Recheck local deadline and expiry before commit. Commit, then acknowledge. Failed/uncertain commit poisons that writer exactly as #1257 does. A fresh selected handle must authenticate again before reconciling the exact original command/key. Never acknowledge a rollback or repeat an uncertain command with a fresh key.

Retain the qualified WAL/FULL/foreign-key configuration, require `read_uncommitted=0`, and use separate connections without shared-cache mode. SQLite's single writer orders revocation against new write admission. If revocation commits before another writer acquires its transaction and reads authority, that writer refuses. If the admitted command already holds the writer transaction, revocation waits and orders after it. There is no API gap where policy succeeds, the transaction ends, revocation commits, and an ordinary `TaskStore::create_task` is then called. These ordering assumptions follow the [SQLite transaction contract](https://www.sqlite.org/lang_transaction.html).

Read commands use one new read transaction/snapshot per request for identity, credential/chain/grants and scoped task data. Never retain a pinned read transaction across requests. A read admitted from a snapshot before a concurrent revocation can finish afterward; this is snapshot linearization, not cancellation of in-flight reads. Revocation committed before that new snapshot is established must be observed. Queries never write authorization caches or canonical events. The snapshot behavior and separate-connection assumptions are specified by [SQLite isolation](https://www.sqlite.org/isolation.html).

Expiry is checked at admission and again before commit. Neither a wall-clock sample nor `OperationBudget` makes an unbounded OS commit a hard realtime operation; expiry does not retroactively undo a command already linearized. Strict cancellation of every in-flight read/write at a later revocation/expiry would need another contract and is not claimed here.

### Replay ownership, attribution and recovery

Propose the v2 operation namespace `(authority, workspace, project, actor_id, action, operation_key)`. It is bound to stable actor identity rather than bearer bytes, so a future explicitly authorized credential rotation need not change an operation identity. Same actor/key plus changed canonical task input refuses. Another actor using the same string key cannot inherit the original actor's receipt or audit attribution. Task-ID collisions still enforce normal task preconditions.

Record actor ID, admitting session ID, epoch and policy/schema version with the committed event/result. Token/verifier bytes are excluded from task input digests, audit payloads and receipt JSON. Do not silently change #1257's v1 digest domain/receipt interpretation: define a new authenticated receipt schema/domain and qualify it against the existing create semantics. Credential rotation and privileged recovery for another actor are not implicitly implemented by choosing a stable actor ID.

A replay still requires current permission for that exact command/scope and matching operation owner **before** its receipt is returned. A revoked/expired credential cannot retrieve a previously committed result. Fresh reconciliation uses the same gate and is an explicit read action (`ObserveCreateTask`) with its own grant plus same-actor operation ownership; `CreateTask` permission alone does not become a general receipt-reading capability. Its payload contains the original bounded `CreateTask` fields and key for canonical digest comparison; it never dispatches the nested command. New fixture owners receive this action explicitly; a read-only session may receive it but it never executes the command. Recovery with lost/revoked credentials requires a newly authorized session for that actor or a later explicit recovery protocol; no fallback owner is constructed.

### Child issuance uses a retained preparation, not fresh entropy on replay

`DelegateSession` has the same actor/action/key namespace. Its canonical logical input is the requested exact scope, action set, effect agreement, expiry, delegation bound and **the canonical issuing parent session ID** obtained after authentication. Random child/credential IDs, verifier material and secret bytes are not part of that logical input digest. The child always receives a newly generated independent actor ID; rebinding an existing actor or rotating credentials is a later explicit operation, not a request option.

The trusted credential adapter creates one `PreparedIssuance` per attempted logical operation before the first commit attempt: preparation ID, new actor/session/credential IDs and secret material. Retain that object through the acknowledgement/reconciliation boundary. It is preparation, not permission; the transaction still checks the current parent and attenuation. A new issuance stores the assigned IDs, a nonsecret preparation binding, verifier and logical digest atomically with its auth event/result. The dedicated secret sink receives only the secret matching those committed identifiers/verifier.

After current authentication, a same-key/same-logical-input replay returns the original **nonsecret** receipt without another child, verifier, epoch change or auth event. Secret redelivery additionally requires the retained preparation to match the canonical preparation/IDs/verifier. The issued child must still be live and unexpired at this admission; revocation never becomes a redelivery or reactivation path. A fresh competing preparation cannot accompany that receipt with its unused secret. Discard it and return an explicit `credential_delivery_unavailable` status for the private handoff; do not relabel its random secret as the old credential. Changed logical input is the normal operation conflict.

Fresh reconciliation can report committed/absent for the original operation under the current parent/actor gate. If a committed operation's prepared secret was lost in a process crash, only its nonsecret outcome is recoverable in this fixture protocol; no token is reconstructed from a verifier and no second credential is minted automatically under that key. Any replacement needs an explicit authorized revoke/new-issuance decision under a new operation, not an unchanged retry. Production crash-safe delivery/recovery is unresolved. This also prevents a duplicate caller from producing a valid-looking receipt paired with a credential that the database will reject.

## 6. Catalog, wire format and public outcomes

Propose one new request/protocol schema version 2 because payload shape, scoped actions and authenticated receipts are new. V1 is a prototype, not a compatibility guarantee: reject unsupported versions explicitly rather than reinterpret old credentials or silently accept old request fields. Final acceptance of this wire break belongs to the integration owner before the implementation slice lands.

The one registry supplies these bounded fixture actions:

| Action | Effect | Gate |
| --- | --- | --- |
| `InspectProtocol` | ReadOnly | Explicit `PublicProtocol` for static catalog metadata only. Missing/empty credential never falls through here. No selected authority facts are returned. |
| `ReadTask`, `QueryTasks` | ReadOnly | Current verified session and the exact action/scope. Authorized absence may return not-found; wrong scope reveals no resource existence. |
| `ObserveCreateTask` | ReadOnly | Current grant for this action, exact scope and same actor's operation. No task write or receipt disclosure before admission. |
| `CreateTask` | Mutation | Current exact `CreateTask` permission and same-transaction task admission. |
| `DelegateSession`, `RevokeSession` | Mutation | Current explicit management permission, scope/lineage/attenuation checks and an auth event/result in the same transaction. |

There is no generic preview switch. An unknown `preview` field is a usage failure; claiming a read effect for `CreateTask` is the existing effect-mismatch refusal. A future preview must be a separate declared read action with no write port. Proposals, index rebuild, counts, raw audit, backup, blobs, plugin execution and filesystem/VCS effects get no route through this catalog.

CLI and API share one strict decoder and normalized request type, not parallel JSON rules. Propose a version-2 maximum input of 512 KiB before decoding, a maximum credential encoding of 128 bytes, bounded grant/action sets, and the existing task limits (256 KiB body, 16 artifact references, 1,024 query rows). Encode arbitrary task bodies as canonical unpadded base64url with decoded-size checks before allocation; the old 64 KiB v1 transport is not falsely claimed to carry every 256 KiB task. Reject duplicate/unknown fields, malformed encoding, trailing documents, excess nesting and ambiguous contexts. Enforce resource limits equally through in-process and actual CLI paths.

CLI accepts request credentials through bounded stdin, never argv/environment/profile fallback. A fixture target is an explicit nonsecret composition option, not a path selected by an untrusted request or a current-directory search. Secret issuance needs an explicit private sink; normal result JSON contains only nonsecret receipt metadata. Public errors, logs, wrapped errors, serialization failures and panic-facing diagnostics use an allowlisted structured error, never `Debug` of the wire request, `CreateTask`, `TaskRecord`, SQL binds, private paths, config or an argument. Existing store records derive `Debug` including body, so they must not be logged wholesale.

Preserve public code semantics: success 0, malformed transport 2, final protocol/policy/precondition refusal 3, authenticated scoped not-found 4, storage/clock/entropy/transport/unknown-commit failure 1. Preserve a typed machine cause and retry classification through wrappers. Operation/input conflicts are final precondition refusals, not permission to retry with new keys. Availability without fixture composition remains an explicit refusal. API outcomes and CLI JSON/exit codes must agree; HTTP status mapping is outside this in-process API slice. Credential failures have no owner context or private row data; after successful secret comparison, expired/revoked/scope failures can expose their stable refusal codes without echoing identifiers or credential values.

## 7. Persistence and crate ownership for implementation

Create a **new fixture format/schema**, proposed `dacli-scoped-fixture/v2`, and a distinct checked schema version. Do not ALTER a v1 fixture in place or infer enrollment from v1 rows. Old held v1 handles already verify schema inside transactions and must refuse a v2 file. A new adapter likewise refuses Go stores, v1 fixtures, absent auth metadata and mismatched identity. A format marker is not a defense against a privileged actor rewriting the entire database.

The proposed v2 database needs canonical authority/epoch, actors, sessions/parent/status/expiry, credential verifier/generation and exact grants, plus immutable sanitized auth events/results and the task receipt attribution described above. Session revocation and its event/epoch must be one commit; actor grant/replay rows cannot live in another database. Session issuance must not store reusable secrets; failed/unknown delivery is not permission to mint another credential under the same operation key.

Suggested source claims keep parallel work disjoint:

| Slice | Exclusive claim and checkable result |
| --- | --- |
| Contract/registry owner | Existing `core-types`, `core-kernel`, `store-contracts` and root manifest/lock edits. Freeze version-2 ActionId/payload/effect/outcome ownership, one action registry and the neutral gateway port before dependent edits. Dependency graph has no policy/kernel I/O or concrete engine dependency. |
| Pure policy | New `next/crates/core-policy/**` only. Missing/forged facts never yield owner context; exact scope, action/effect, lineage, expiry and narrowing tests pass. The crate has no std/host/store-adapter dependency. |
| Atomic fixture backend | Scoped SQLite module/schema plus focused process tests, under an explicit separate claim agreed with the storage owner. New-fixture enrollment, current auth/epoch reads, task writes and management/replay attribution are transactional. Raw #1257 fixture APIs are not called after a separate authorization check. |
| Fixture credential adapter | New `next/crates/fixture-credentials/**` only, after the contract owner freezes shared secret/match types. Own reviewed entropy/verifier comparison and retained prepared enrollment/issuance material; no vault/profile/environment discovery. Root owns dependency pins/lock edits. |
| API | New `next/crates/api/**` only. One bounded decoder, backend port/composition, secret handoff contract and typed redacted outcomes; no concrete database import or duplicated crypto verifier. |
| CLI integration | Existing `next/crates/cli/**` only after registry/API freeze. Thin shared API adapter, explicit fixture composition/private issuance sink, real subprocess parity tests. No duplicated action list or secret argv. |

The exact physical home of the neutral port and shared identity types is a contract-owner decision, not an excuse for dependency cycles. One viable graph is `core-policy -> core-kernel (pure registry), core-types, store-contracts`; `api -> kernel, policy, task contracts`; `store-sqlite -> api's neutral port, policy, task contracts, fixture-credentials`; `cli -> api + trusted fixture composition`. Shared secret/match types live below both API and credential implementation; `fixture-credentials` must not import API or the backend. `api` must not import its concrete backend. If the neutral port is extracted to a pure contracts crate, do that once under the contract owner's claim; do not create near-identical permission/context types in each feature.

## 8. Acceptance and causal verification slices

These are future check names, not test results. No heavyweight gates were run for this document.

1. `unset_empty_forged_foreign_expired_revoked_never_make_owner`: exercise actual new fixture credentials through API and a real CLI child. Remove each relevant verification/scope/expiry/revocation guard separately; require the named semantic test to fail for the expected wrong outcome, then restore green.
2. `read_only_scope_query_cannot_create_or_preview`: create records in two projects/authorities, show only allowed scoped query results, then actual API/CLI create/reforged-effect/preview attempts leave task/version/event/result/auth state unchanged. Disable catalog effect or current grant checks separately and observe red. A public-only InspectProtocol test cannot substitute for this scoped read test.
3. `revocation_committed_before_admission_refuses_cached_context`: two independent processes and barriers, not sleeps. Prime a candidate credential/cache observation, commit revocation in process A, release process B to begin its new admission. Refusal and unchanged task/result rows must be observed. Mutate the backend to use the stale pretransaction snapshot; this must fail. Also qualify both legitimate writer orderings and the documented in-flight read snapshot behavior.
4. `replay_and_reconcile_reauthenticate_original_actor`: current actor replay returns the original result without extra writes; changed input refuses; another actor's same key cannot steal it; revoke/expire before replay and reconciliation and require refusal. Bypass current auth or operation-owner binding independently and observe named red failures.
5. `delegation_never_amplifies_scope_effect_expiry_or_kind`: cover wrong authority/workspace/project, new action/effect, owner-kind elevation, missing/cyclic/over-depth ancestry, expired/revoked parent and child expiry beyond parent. Allow exact/narrow cases. Parent revocation must invalidate a grandchild on a new admission. Mutate each attenuation predicate under focused tests, with actual committed state checked for refused requests.
6. `owner_enrollment_requires_exclusive_new_fixture`: missing owner/empty existing file/v1/Go-like target never triggers owner creation; entropy/secret sink/commit failure yields no success or fallback owner. Observe actual postcommit unknown-outcome recovery with the retained prepared enrollment identity. Child issuance delivery failures must not echo secrets or issue a second credential silently. Two independent issuers racing the same logical key must produce one canonical child: a loser cannot return its unused fresh secret with the winning receipt. Retained-preparation replay may redeliver only the matching secret; a process that lost preparation receives explicit delivery-unavailable. Mutate the reuse/match guard and observe named red failures.
7. `public_surfaces_never_contain_canaries`: use synthetic distinct secret, private-config, body, title, path and unsupported-argument canaries across parser, verifier, policy, backend, wrapper and transport failures. Inspect stdout, stderr, allowed structured log sink and Debug surfaces. Include malformed/duplicate/oversized/context-extra requests and failed secret delivery. Never put a real credential in test argv or artifacts.
8. `schema_and_transaction_binding_refuse_old_or_stale_writers`: prove old v1 open and held handles refuse new format, missing/mismatched auth metadata refuses, and current auth admission plus task/event/result is all-or-none across commit interruption. Preserve #1257 busy/disk/unknown-commit proof; do not claim schema markers resist privileged rollback.

The issue's proposed focused acceptance command remains `cargo test -p core-policy -p api --test scoped_commands`. Give `api/tests/scoped_commands.rs` the cross-boundary test target and `core-policy` its own semantic unit/integration targets; add real CLI subprocess checks where listed. Use the explicit qualified toolchain/locked dependencies and external Cargo output directory. Record exact commands, candidate hashes, named mutation failures and restored results. Ordinary source review is separate from authenticated owner acceptance. Successful tests may support only their named new-fixture invariants, never an all-ledger or historical-runtime claim.

## 9. Decisions that remain open

| Decision | Proposed bounded default / activation consequence |
| --- | --- |
| Production credential/vault/transport and owner recovery | Not selected. Fixture crypto/verifier and dedicated secret handoff only. No keychain, legacy token activation, remote login or existing owner borrowing. Production activation remains unavailable. |
| Credential encoding, crypto libraries and exact pins | Approve a small maintained randomness/verifier/constant-time/secret-buffer implementation before code. Do not implement homegrown cryptography or label a placeholder verifier authenticated. |
| Wire/schema version 2 and shared type/port ownership | Recommended explicit break/new fixture, not in-place upgrade. Contract owner must freeze this before parallel source edits. |
| Lifetime/delegation breadth | One-hour fixture maximum, depth 8, exact single-project scope proposed for tests. Product lifetimes, multi-project grants, root renewal, credential rotation and management UX remain undecided. |
| Trusted time and live authority rollback/fork resistance | Fixture clock assumptions and SQLite commit semantics only. Durable expiry across clock rollback, an external revocation witness, database reset/merge behavior and distributed authority need independent decisions (#1268). |
| Secret delivery failure/recovery | Retain a prepared secret privately; report unknown commit/delivery honestly. No automatic second owner or plaintext persistence. Product-grade crash-safe issuance/recovery remains to be designed before real-user enrollment. |
| Future proposal/preview/export/host effects | Not modeled as reads or smuggled through this task gateway. Their effects, disclosure authority and confinement require separate catalog actions/contracts. |

None of these decisions is resolved by this document, schema pseudocode, an epoch field, a passing ledger inventory checker or a read-only reviewer. Ledger dispositions and unverified rows remain unchanged.

## Source digests

SHA-256 at the source base (the issue input is cited above):

```text
core-types/src/lib.rs      d0bbd6476d13162eade15e1f4c414c114d0d3ee0a2d910d92dc37b188825c498
core-kernel/src/lib.rs     26ee05920b20f89da460d487b32de96882702a92ee2bc53bbb0eb28d810cca6b
store-contracts/src/lib.rs 2549eefd4f54804336f108b377b9203516bc35a0529ad921da417fc626f15a61
store-sqlite/src/store.rs  0c0e7ff775ddfe38a5fa6fce331d086315735bb99731171d2d3d5224d64fa82c
store-sqlite/src/schema.sql 3c3697a12c72b4a467153d1ec3481d66641fc2edaf3bcc6b3753b0f0c4e1f707
cli/src/lib.rs             b4cbae2378d18817719d5aee6f3fadd829eb7aa7de665b965b33369b98c57310
next/design/invariants.yaml 3765d5417b8c044afb28a17932542d9b59bd190bdf1735f554b19ee13a2539ea
docs/TRUST.md              dd473d819398b731495918ea4f3ab23a4640a1f5f4ce3c21bee2a48ab5a98041
```

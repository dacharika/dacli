# Native publication decisions

The sessions authority records owner-bound decisions for one action: updating
an existing issue. It never publishes, invokes a provider or CLI, changes task
status, or records an external action outcome. An allow is an assessment; a
consumption receipt is historical evidence of a local permission transition.
Neither is a bearer credential or permission to perform arbitrary publication.

## Owner policy and authenticated evaluation

`SetPublicationPolicy(owner, request)` binds an existing canonical task reference
to an exact `owner/repository`, visibility (`public`, `private`, or `unknown`),
and explicit internal-disclosure settings. Those settings are owner assertions,
not observations of remote GitHub state. Unknown visibility retains the shared
publication policy's conservative behavior. Every change appends a version with
a monotonically increasing generation and content digest. The caller must name
the current generation, starting with zero; stale updates refuse. Reverting the
settings creates a new version and cannot revive an old decision.

`EvaluatePublication(token, session, request)` authenticates the worker and its
ancestors. It requires `publication.evaluate`, exact `task:<task-id>` and
`repository:<owner/repository>` scope, the worker's registered attempt, current
lease/fence, and latest published clean revision. Fresh task admission through
the owner-configured `LeaseAdmission` adapter is required. No adapter refuses.
A task closure or dependency change which makes that adapter refuse also
prevents new evaluation and consumption; stored `AdmissionEnabled` alone is
not current canonical readiness.

The request binds the action, repository, positive decimal issue number,
projected payload SHA-256 (64 lowercase hexadecimal characters without prefix),
field set, revision, lease generation and expiry. Field sets are copied, sorted
and deduplicated consistently. The authority derives actor, workspace,
installation, authority identity and policy from its own state. It reuses
`policy.EvaluatePublication` for typed reasons, obligations and
allow/deny/needs-approval results. The native digest additionally binds the
exact revision. Assurance is `cooperative-session`: neither a verified host
identity nor protection against an OS user who can replace authority files.

The decision is immutable. A matching evaluation operation key can retrieve its
historical assessment after credential reauthentication; it never renews
validity. Changing the operation payload refuses. A needs-approval decision
cannot become consumable in place: the owner must update policy and the worker
must evaluate again under a new operation key.

## Owner-only one-use consumption

`ConsumePublication(owner, request)` authenticates the explicit owner credential.
The request names the decision, exact normalized action and a stable operation
key. Only allow for issue-update can consume. Under the same authority lock it
checks current worker ancestry/scope, task binding, policy version, clean exact
revision, lease/fence/expiry and decision expiry. It observes task admission and
Git after authorization, then checks current authority and time again before
writing. Revocations, dirty or replaced checkouts, stale revisions/fences,
released leases and changed policies refuse.

The decision consumption and its operation receipt are persisted together.
Distinct concurrent operation keys yield at most one consumption. An exact retry
reauthenticates the owner and returns the same receipt, including after worker
revocation or expiry: `Historical` is always true, including the first response.
It performs no new transition and confers no renewed authority. A different
operation key cannot consume again; reusing the winning key with changed input
also refuses. Worker credentials cannot consume or retrieve owner receipts.

No output or external outcome is invented. A future consequential-action adapter
must separately bind the exact outbound bytes, perform its action, and record
its actual outcome. A lost response after an external side effect would require
explicit reconciliation rather than automatic execution replay. This kernel
therefore advances the decision boundary without completing external publication
or native canonical acceptance.

## Storage and limits

New authorities write schema 6. Existing schema-5 authorities require explicit
`UpgradePublication(owner)`; opening never migrates. This migration is currently
a Go owner API only: the existing native CLI/plugin commands cannot upgrade
a schema-5 profile, and that profile will refuse until the owner explicitly
migrates it. No startup or bootstrap path resets old storage. Existing explicit schema
1–4 migration APIs now reach schema 6. Migrations retain existing sessions,
revocations, task bindings, attempts, leases/fences, revisions, evidence, review
grants, proposals and operation history. Unknown versions and old writers refuse.
The existing canonical-path lock, fsync and atomic-replacement storage protocol
is reused; a crashed lock is not automatically stolen.

Publication policy history, decisions and consumption live in the existing
aggregate, not another task/status authority. Git and Markdown writers do not
share that lock. Their observations cannot freeze checkout contents or promise
atomic canonical task readiness against concurrent legacy edits. The trusted
adapter must be a read-only current observation; cooperative writer isolation
remains required. Permission expiry/revocation does not stop an existing writer.

Tests cover independent processes racing distinct consumption keys and a child
that commits then exits without returning its receipt. Recovery returns history
only. Guard mutations cover exact action binding, current authority and one-use
consumption. All fixtures stay local; no external publication is exercised.

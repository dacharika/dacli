# Native review proposals

Schema 5 adds review grants and immutable proposals to the existing durable
session authority. The explicit owner-only `UpgradeReviews` migrates schema 4
without changing sessions, hashes, bindings, attempts, revisions, fences, leases,
evidence, verification grants or operation receipts. Older writers refuse the
upgraded file. Earlier owner migrations initialize the current schema too.

The trusted application owner configures `NewNativeReviewObserver` with an
explicit workspace and a `NativeReviewPolicySource`, then opens
`OpenReviewAuthority`. The policy source returns the current owner-selected
revision and required check IDs/command digests. Each grant, new proposal and
fresh evaluation loads the canonical task and uses the existing
`store.AcceptanceChecklistDigest`. Missing criteria, duplicate/missing tasks,
wrong workspace/project and unobservable policy fail closed. No request can
provide its own checklist or trusted-policy assertion. Plain `Open` has no
observer and cannot establish current review eligibility.

The owner enrolls a reviewer with `review.propose` and `review.evaluate` for the
exact `task:<id>` resource, and issues a nondelegable `GrantReview` for the task,
repository and implementation worker family. The grant captures the freshly
observed checklist, owner policy and required checks. It has an expiry and can
be revoked with `RevokeReview`. Reviewer sessions must be owner-enrolled roots;
the worker's ancestors, descendants and delegated siblings cannot substitute
for independence. Review credentials allow only review and optional read
operations. Implementation capabilities or historical task attempts under the
reviewer's family disqualify it. These conditions are checked again at use.

`ProposeReview` authenticates the reviewer and records `approve` or
`request-changes`, a rationale of at most 4096 UTF-8 bytes, exact evidence IDs,
current immutable revision, worker lease/fence and observed checklist/policy.
The reviewer references the worker's lease; it never acquires that exclusive
write lease or needs the worker credential. Both outcomes require the configured
successful trusted evidence; a request for changes is recorded but never eligible
as approval. Current support requires one evidence record per required check.
Unknown providers and self-reported outcomes cannot satisfy trusted verification.

Evidence must be successful `trusted-local-runner` evidence for the same full
revision. Evaluation rechecks runner enrollment, grant, command digest, scope,
expiry and revocation, as well as the worker's complete session chain, current
lease and fence, reviewer grant and clean unchanged checkout. New revision
publication, dirty/changed checkout, released/expired lease or changes to policy,
checklist or any relevant authority make the old approval ineligible.

`EvaluateReview` returns a typed reason and
`cooperative-credential-separation` assurance. Only the proposing reviewer may
request this observation using its current credential. Revoked or invalid caller
credentials are denied. Exact operation retries with valid caller credentials
recover the original immutable receipt, even if its grant later becomes stale;
changed payloads refuse. A retry receipt is never current permission.

All authority writes and checks use the same private-file transaction and lock.
The trusted read-only observer must not reenter the authority or mutate task
state. Markdown writers and arbitrary OS writers do not share that lock, so
checklist and checkout checks are fresh cooperative observations, not filesystem
freezes or an atomic task-graph transaction. Separate credentials do not prove
separate humans/models, native Codex identity or reviewer reasoning independence.
The local runner remains an owner-trusted cooperative issuer, not cryptographic
host proof; remote CI identity is unsupported. Credentials/provenance flags in
request bodies cannot upgrade that assurance.

This slice does not move task files, close tasks, apply legacy acceptance
proposals, merge, deploy or consume acceptance authorization. Future acceptance
consumption must repeat current authority, revision and canonical policy checks.
The existing legacy `proc.txt` reviewer path remains unchanged. Real-Git tests
run an actual owner-named command through `NativeVerificationRunner` with three
separately enrolled worker/runner/reviewer credentials and no process records.

## Owner provisioning and native tools

The local owner adapter provisions a new independent reviewer using private
files outside the repository and plugin package:

```sh
dacli native reviewer add --config /private/owner/owner.json --task project/001 --worker-session WORKER_SESSION_ID --runner-config /private/owner/runner.json --policy-revision owner-policy-v1 --policy-out /private/owner/review-policy.json --review-config /private/owner/review.json --connection /private/owner/reviewer-session.json --credential /private/owner/reviewer.token --expires 1h
dacli native serve --connection /private/owner/reviewer-session.json --review-config /private/owner/review.json
```

The owner configuration authenticates enrollment. The command uses an existing
canonical task binding, derives every required check digest from the configured
runner's command and deadline, and issues a new review-only root session and
nondelegable grant. Required checks are all descriptors in that runner profile.
The reviewer receives only `read`, `review.propose` and `review.evaluate` for the
selected workspace/task. Credentials, profile, connection and policy outputs
are exclusively reserved as private new files. Issued authority and recovery
identifiers remain preserved if setup is interrupted; never reset authority or
overwrite an existing credential to recover.

Use a separate connection/process from the implementation worker. The reviewer
catalog is exactly `identity`, `workspace_status`, `task_list`, `task_get`,
`propose_review` and `evaluate_review`. Owner provisioning and policy
administration remain local-only. Worker runner configuration cannot be combined
with reviewer configuration. Shared MCP connections still share one identity.

`propose_review` receives `schema_version: 1` and a `request` containing
`operation_key`, `attempt_id`, `lease_id`, `revision_id`, `expected_fence`,
`outcome`, `rationale` and `evidence_ids`. `evaluate_review` receives
`schema_version: 1` and `review_id`. Grant and issuer identity are derived from
the pinned private reviewer profile; unknown overrides refuse. Typed output and
JSON text fallback contain the same result. Proposal output is an immutable
`Review`; evaluation returns `Eligible`, `Reason` and `Assurance`.

The connection/profile selection is pinned. The private owner policy is read
afresh during canonical observation, so a valid change to its revision/check
digests invalidates existing approval eligibility. Exact proposal replay remains
historical; it does not bypass a fresh eligibility check. Unreadable or malformed
private files fail closed. Keep the implementation worker's lease current until
review has finished; the reviewer never acquires that lease. A proposal does not
change canonical task status or consume final acceptance authorization.

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

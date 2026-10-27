---
name: governance-workflow
description: Coordinate ready independent features with owner-enrolled dacli task, fenced revision and independent review governance through the shared native MCP backend.
---

Call `identity` first and inspect the advertised scope. Read `workspace_status`,
`task_list` and `task_get` using schema_version 1. Missing or expired enrollment
is a refusal; use an already-authorized owner provisioning/recovery path when
available, otherwise identify the exact missing private connection. Never
substitute ambient DACLI_AGENT, a legacy CLI wrapper or another child's identity.

At startup, resume and context loss, call `task_brief` and `ready_frontier` with
`schema_version: 1` through the selected profile. Frontier requires `limit`
(1–100), with project or exact-task scope. Inspect dependencies, source refs,
freshness and omissions; narrow context is enough when it answers the task.
These reads do not reserve work or grant broader authority.

For a multi-feature request, keep ready dependency-independent features in
parallel native-host lanes with disjoint claims, separate checkouts/attempts and
independent worker profiles. Refresh readiness and refill available lanes after
delivery; a blocked feature need not stall another eligible lane. Use observed
host capacity, not configured width, as the availability fact. Parallelize focused
checks and review, and queue expensive full gates for each exact candidate to
avoid contention. Preserve per-feature required evidence and independent review;
land each feature when its own contract and authorized owner integration pass.
Honor the user's CI trigger policy. Existing authorization carries forward for
reversible work within scope without repeated approval; it does not extend an
expired credential or lease. Continue unaffected lanes during exact recovery.

Use the host's native editing tools in the authorized checkout. Register a clean
real checkout with `register_attempt`, acquire only permitted resources with
`acquire_lease`, preserve the returned fence and keep the lease current. Commit
through the authorized DACLI host workflow. Publish only a clean committed
revision with `publish_revision`. `submit_evidence` is a bound self-report,
not trusted execution or independent review. Keep the worker lease current through
independent review, authorized owner integration and canonical acceptance. Review
approval alone is not a release condition. After the owner retires accepted work,
the owner explicitly recovers protection using the authenticated terminal state;
uncertain or abandoned work requires owner reconciliation.

A separately provisioned runner connection advertises named verification tools.
Request each required check on the exact revision/lease/fence and observe its
terminal durable result. Pending or unknown is not successful verification;
retain the exact operation identity for owner reconciliation. Supply no command,
credential or grant overrides. A reviewer uses a separate private authenticated
connection and `propose_review` / `evaluate_review`; never borrow worker identity.
Review eligibility does not accept, merge, push or deploy a task.

Shared backend roles and schemas are unchanged from Codex. Host labels and
subagent labels do not prove independent identity: all children sharing one
MCP process share its enrolled principal. Antigravity task/review packaging does
not establish a host dispatch capability adapter. Orchestration profiles,
automatic child dispatch, automatic termination and model switching are
unsupported here. Do not relabel Codex capability evidence as Antigravity proof.
Hooks, usage measurement and cancellation enforcement are unverified. Cooperative
filesystem actions are not contained by these gates. Use the host's native
agent workflow; never launch a nested coding CLI.

Policy exit3 is final for an unchanged request. Report the refusal or unknown
outcome instead of changing keys to rerun an uncertain effect. Only the owner
can enroll, grant, reconcile retained protection, integrate or accept.

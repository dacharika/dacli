# Native task landing observations

A task can arrive through a reviewed batch PR without having a legacy task-named
branch. Issue #1209 adds an owner workflow connecting its native publication to
the actual batch and verified target. Recording does not merge or accept work.

## Record one delivered source

Use the original registration/publication receipts. The selected revision must
be the latest in that attempt when recorded. Its registration commit supplies
the source base; the caller cannot choose a smaller source delta. Historical
publications remain usable after worker revocation, lease release or checkout
removal.

Save a private request, replacing every placeholder:

```json
{
  "operation_key": "one-stable-key-for-this-recording",
  "expected_previous_id": "",
  "task_id": "EXACT_CANONICAL_TASK_ID",
  "attempt_id": "REGISTERED_ATTEMPT_ID",
  "revision_id": "PUBLISHED_REVISION_ID",
  "github_repository": "owner/repository",
  "pr_number": 123,
  "expected_pr_head": "EXACT_PR_HEAD_COMMIT",
  "integrated_base_commit": "BATCH_RANGE_BASE_COMMIT",
  "integrated_commit": "BATCH_RANGE_TIP_COMMIT",
  "target_commit": "VERIFIED_LANDED_TARGET_COMMIT",
  "target_tree": "VERIFIED_LANDED_TARGET_TREE"
}
```

The request is an owner-owned regular private file, at most 64 KiB. The previous
ID is empty only for the first observation. A new selected delivery must name
the current receipt. Preserve the operation key and exact request if output is
interrupted. Exact replay returns the existing receipt after fresh validation;
changed payloads and superseded selections refuse.

```sh
dacli native landing record --config /private/owner.json --project core --request /private/landing-request.json --json
```

The project explicitly selects PR landing, its base and `github_repo`. Private
owner configuration and credentials are authenticated and revalidated. Worker,
reviewer and operator-reader profiles cannot substitute for the owner. This is
local-only, including through the old generic MCP CLI route. The native plugin
gains no owner recording tool or grant.

## Exact source and target proof

The producer reads the authenticated historical task binding, attempt and
publication. A bounded read-only `gh` query observes the merged PR's exact
repository, number, URL, head and configured base. Successful clean structured
verification of the final commit/tree must already be attached to the task.
An unrelated green main commit does not prove delivery.

The shared validator checks genuine Git objects and the complete task delta:
old/new blob identities, file modes, additions and deletions. Normal merges
and multi-commit cherry-pick batches are supported. The explicit integrated
base-to-tip range must contain that same full source delta; extra batch changes
may exist outside its paths. No selected path subset, patch-ID match, commit
message or synthetic legacy branch can replace this proof. Objects must already
be available; the workflow performs no fetch. Source and target repositories
are separately bound, including independently cloned worker repositories.

The integrated tip must occur in the exact PR head and merge history. Source
contents at the PR head and merge must still match, so a revert inside the PR
cannot pass through ancestry alone. Overlapping rewrites and unsupported
mappings remain refused/unknown and need an exact corrective owner mapping.
The genuine target commit/tree must contain that merge.

Later forward commits, including legitimate edits to delivered files, do not
erase historical delivery. A rewritten target, changed task/delivery generation,
criteria/claims, project policy, repository, selected source, verification record
or invalid history fails closed. Age alone does not expire immutable merge
evidence. A later speculative publication is not automatically a new selected
deliverable; the owner selects a new source with another validated CAS record.

## Shared consumers and retained protection

Versioned observations append to the task's `Landing Evidence` section;
`native_landing.current` selects its digest. Existing history remains. Exact
replay is a no-op; conflicting operations, selection rollback, malformed history
and unknown fields refuse. Append stages the complete document before assigning
it and preserves the canonical 256 KiB task-file limit and bounded history.

Aggregate progress, parent eligibility, acceptance, delivery-slice state,
explanations and cleanup consult the same validated observation. An invalid
present record cannot fall through to loose PR state, old delivery metadata or
legacy ancestry. Absent new-format evidence preserves the legacy workflow; a
missing legacy branch does not itself prove native delivery. Native aggregate
and cleanup rows include the selected receipt and landed/unknown/unlanded state.

Landing does not authorize cleanup. Dirty/extra content, unpushed commits,
live/uncertain ownership, retained claims, protected paths, managed-worktree and
recovery constraints still apply. External native worktrees do not become
cleanup candidates. Recording releases no lease and checks no acceptance box.

## Bounds and qualification

Git and remote observations honor one 15-second context. Git capture is bounded
to a combined 1 MiB; the GitHub query has separate 1 MiB stdout/stderr bounds.
Cancellation is checked before committing task changes.
Existing authority/task locks have bounded legacy wait overhead, and finite
filesystem operations cooperate with cancellation. This is not a hard
15-second response or forced OS-I/O interruption guarantee.

Existing authority records and task locking are reused. No stored authority
version or implicit migration is added. Isolated actual-CLI tests exercise
schema10, released/revoked historical workers and exact replay without changing
authority bytes. Fake service replies qualify fixtures only. Live host capacity
remains #1216, executor qualification #1217, and rolling swarm effects #1218.
CI stays manual-only.

The digest identifies content/replay consistency, not a signature against
arbitrary local file rewriting. Dacli's cooperative local-file trust boundary
is unchanged. Receipts contain source paths and revision metadata, but no
credentials, credential hashes, command output or transcripts. Handle them as
workspace evidence.

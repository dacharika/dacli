# Native artifact retention dry-run

```bash
dacli native storage retention --config <private-owner.json> --project <project-id> --json
```

This owner-authenticated command reports `native-artifact-retention/v1` with
`dry_run: true`. It writes or deletes no artifact and has no apply route. The
owner profile is explicit, immutable for the call and freshly authenticated;
worker credentials and ambient legacy grants cannot substitute.

## Eligibility and retained evidence

The planner reuses the existing run-artifact classifier. A removable generated
artifact needs an exact path, run/task identity, content digest, size and mode,
a terminal run with released claims and a canonical DONE task. Durable run
records, transcripts, verification artifacts and unknown run evidence remain
protected. This delivery does not change legacy cleanup's apply path; a native
retention report is not an apply permit for that separate command.

Fresh native observations further narrow eligibility:

- Verification-output digests preserve matching artifacts, including identical
  copies associated with another task. An unavailable/malformed output digest
  preserves the referenced task's candidates.
- Unreleased leases and retained dispatch reservations protect their task even
  after expiry or revocation.
- Pending operations, uncharged or unknown-retained budget liabilities and
  unresolved controller outcomes retain protection. An outcome that cannot be
  bound to a task creates a conservative global hold.
- Consumed native policy decisions require the exact operation, decision,
  canonical outcome digest and closed acceptance input/receipt. Unknown,
  mismatched or missing outcomes remain held. A genuinely bound closed outcome
  is distinct from an unresolved consumption.
- Closing/unresolved canonical acceptance records retain their task's artifacts.

Unreadable native authority, incomplete run evidence, missing projects or source
authority drift refuse the dry-run. Absence of a hold does not make an otherwise
ineligible artifact removable. Actual tests cover both an exactly identified
removable generated file and refused referenced/retained candidates, while
asserting artifact bytes remain unchanged.

## Limits and recovery

The report is an observation of cooperative writers, not an atomic cross-store
cleanup transaction. Canonical Markdown and run records have their existing
writer/locking contracts; no new authoritative database or writer is activated.
Provider usage and unknown outcomes are not converted into success.

Preserve private aggregate history, canonical task/event records, referenced
verification/review evidence and unresolved operation receipts. Disposable
storage-experiment snapshots live outside the workspace, are private and remain
non-authoritative; DACLI does not automatically remove their publication files.
Owners may inspect and separately manage disposable evidence after deciding what
they need to retain. No command introduced here grants deletion, push, deployment
or release permission. See [authority storage](GOVERNANCE_AUTHORITY_STORAGE.md)
for the retained hybrid model and interrupted-import behavior.

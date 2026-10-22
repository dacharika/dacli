# Local native action policy

Schema15 connects an explicit owner policy decision to the real local native
acceptance consumer. `native policy upgrade --config <owner>` upgrades exactly14
to15, preserving older histories and principals. No automatic or global upgrade
occurs. A genuine schema14 binary refuses the upgraded authority.

Configure an immutable task/project policy with `native policy configure --config
<owner> --request <private-json>`. The request fields are `SchemaVersion: 1`,
`ProjectID`, `TaskID`, `Revision`, `Allow`, `RequireApproval`, `Stopped` and
`ExpiresAt`. Repeating a historical revision returns history without restoring it
as the current policy. Use a fresh revision for a changed legitimate policy.

`native policy evaluate --config <owner> --review-policy <private-policy> --request
<private-json> --json` takes `OperationKey`, `Input` (the existing exact independent
native acceptance input) and `ExpiresAt`. Evaluation requires current independently
credentialed verification and review, performs no canonical Save/Move and records
an immutable typed allow/deny/needs-approval assessment. Its assurance is
cooperative-session, not host containment. Pure policy evaluation alone is
observed-advisory. The stored preparation time/body digest binds the exact future
canonical result without making an observed action claim.

When required, `native policy approve --config <owner> --request <private-json>
--json` records owner approval for `OperationKey`, `DecisionID` and `ExpiresAt`.
Worker credentials or caller approval booleans cannot replace owner authentication.

`accept apply-native --config <owner> --review-policy <private-policy> --request
<existing-input> --decision <id> --json` consumes a current exact decision and
persists closing task retirement in one authority transaction before any canonical
effect. Schema15 fresh actions require this decision; the prior public kernel or
application entry points cannot bypass the gate. Approval, policy, independent
producers, scope, exact tree/revision, lease fence and expiry are checked again
before the effect, after the final bounded observer. Task lock precedes authority
lock. These cooperative locks do not freeze arbitrary filesystem writers.

There is no public consume command or transferable bearer action permit. Distinct
competing consumer operations cannot both consume/close one task. Exact Apply
operation replay recovers only that original closing effect or verified historical
DONE; it never provides permission for a new action. A different decision cannot
attach to the existing operation. Migrated schema14 closing operations without
consumption cannot perform a new Save. Their exact already-DONE receipt may be
finalized historically without manufacturing policy consumption or outcomes.

`native policy status --config <owner> --decision <id> --json` authenticates the
owner and returns history only. Permission, immutable consumption and observed
outcome are separate. An unknown outcome has an `ExpectedOutcomeDigest`, empty
`ObservedOutcomeDigest` and zero `ObservedAt`; only exact raw canonical DONE
readback supplies the observed digest/time. Failed consumption persistence returns
no usable effect output. Partial canonical Save/Move or failed final authority
persistence retains closing/unknown state. Ignored canonical metadata can continue
only with fresh current authority and exact original bytes; tracked partial
metadata dirtiness refuses3 with reconciliation required. No new key, credential
reset, restoration, protection release or stopped-writer claim is inferred.

Existing historical publication decisions/consumption remain unchanged. This
consumer neither publishes GitHub work nor launches processes/models, migrates
cloud authority, releases leases or implements future integration producers.

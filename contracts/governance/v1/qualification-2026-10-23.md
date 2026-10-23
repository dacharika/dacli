# Shared governance conformance qualification — 2026-10-23

This records deterministic backend and transport coverage for core573/#1081,
plus package/install checks needed by core572/#1082. It does not certify a second
coding host. No actual Antigravity worker or reviewer call is claimed.

## Defect and resulting behavior

The shared consumer allowed obsolete tool names such as `submit_review` and
`controller_step`, while refusing current native `propose_review`,
`get_verification_status`, `observe_dispatch` and related calls before they
reached the backend. Its closed ceiling now follows current native tool names.
The selected connection's actual catalog remains a separate mandatory guard;
a worker catalog cannot borrow a reviewer tool.

The additional real-binary test creates fresh private authority through supported
owner CLI commands. An authenticated native stdio control must return the exact
enrolled session, canonical task and workspace/task scope. An otherwise valid
connection pointing at an absent credential must exit 3, leave that file absent
and preserve authority bytes. No bearer contents are read or copied by the test.
This startup refusal is not fabricated into an authenticated tool receipt.

## Coverage and expected boundaries

| Contract | Executed deterministic evidence | Expected outcome |
| --- | --- | --- |
| Missing credentials | `NativeIdentityTests.test_missing_credential_refuses_after_authenticated_identity_control` | Exact scoped control succeeds; absent credential exits 3 without authority writes. |
| Wrong bindings and revoked credentials | `TestNativeWrongBindingCredentialAndReadScope`, `TestNativeReviewRejectsUntrustedEvidenceAndRevokedReviewer` | Invalid binding, token, scope or revoked reviewer cannot act. |
| Overlapping leases | `TestLeaseOverlapAtomicSetAndTaskReservation`, `TestNativeSwarmAtomicConcurrentReservationAndLeaseExclusion` | No concurrent overlapping writer admission. |
| Forged/shared reviewer | `TestNativeReviewerProfileCannotReuseWorkerIdentity` | Implementation credentials cannot substitute for independent reviewer credentials. |
| Stale evidence and replay | `TestVerificationFinalRevisionFenceAndProvenanceGuards`, `TestEvidenceNewRevisionSupersedesEligibility`, `TestNativeReviewHistoricalReplayAndFreshEligibility` | Historical results cannot grant fresh eligibility. |
| Broker/process loss | `TestLeaseLostReplyReplayAcrossProcesses`, four `ProducerProcessLossThenExactRetry` tests, actual integration/acceptance `ProducerCrashRestart` tests | Durable exact retries do not duplicate producer effects; unknown work remains protected. |
| Duplicate/reordered events | `TestNativeLifecycleLedgerFreshnessRestartAndTerminalMonotonicity`, `TestNativeLifecycleCancellationAcknowledgementSequenceAndReplay` | Original receipt/sequence and terminal state remain monotonic. |
| Unacknowledged cancellation | `TestNativeLifecycleCancellationWithdrawsBeforePermitAndRetainsResources`, `TestNativeLifecycleUnsupportedCancellationAndBindingRefusal` | A cancellation request does not prove termination or release uncertain writer protection. |
| Disabled/malformed hooks | `ContractTests.test_hooks_never_promoted_from_stdio`, closed live-only definitions | Explicitly unsupported and not entered by stdio; no blocking/enforcement claim. |
| Antigravity package/install | Five `PackageInstallTests` | Final bootstrap path/interpreter pinned, no connection contents copied, exact private backup, unrelated config preserved, conflict/tamper/concurrent replacement refused. |

The backend tests above run in the existing full Go suite. The shared Python
consumer still needs explicit independently owner-provisioned scenario inputs
before emitting `observed-backend`; missing scenarios stay unsupported. This
matrix does not forge a completed owner-fixture report or a hook observation.

[The actual Codex 0.163.0-alpha.11 receipt](../../../plugins/codex/receipts/native-swarm-0.163.0-alpha.11.md)
provides the separate named real-host workflow: three Luna workers, two waves,
independent connections, trusted checks, review, owner integration and canonical
acceptance. That receipt is Codex evidence only.

## Terminal local verification

Clean, unchanged source `ca366a3f3b6dba94db6762d3bac765ea428b2ee8` (tree
`683276f10bf5b382c467a0657015bff0dd9a205c`) passed formatting, vet, pinned
golangci-lint 2.12.2, workflow contracts/actionlint, both release configurations
and shell syntax checks with Go 1.26.10 on Darwin arm64.

All 92 Go packages passed: 91 packages together, and all 221 session top-level
tests in four disjoint exhaustive groups of 56/55/55/55, with no skipped or failed
tests. Default timeouts and assertions were preserved. This is complete package
coverage; no unpartitioned `go test ./...` success is claimed. The full local
receipt SHA-256 is `f1904b1cf26c4f02a36925f3024e98b5592d02d1d91d09ac00c45435921ea04c`.

All 31 Python tests passed: nine shared conformance/identity, five Antigravity
package/install, and 17 Codex package/bootstrap tests. The native identity test
used the installed runtime from `2ca14340b6ad`; all Go source files are byte-identical
to that runtime's source. Independent source review found no actionable issues.

The orchestrator independently reran three disposable mutations and verified
these named failures, then reran the restored three tests successfully:

- Old tool ceiling: `allowlist lacks {'evaluate_review', 'propose_review'}`.
- Removed startup authentication: `AssertionError: 0 != 3`.
- Disabled conflicting-server guard: `AssertionError: ValueError not raised`.

The manual CI job now builds one binary for conformance/identity and runs the
Antigravity safety tests alongside the retained checks. No push, pull-request or
schedule trigger was added, and no hosted workflow was dispatched. Finalization
changes only these evidence notes and adapter README metadata after the tested
source revision.

## Live Antigravity remains separate

Local metadata identifies Antigravity App 2.17.0 and Antigravity IDE 2.5.5. A UI
attempt did not complete opening the isolated qualification workspace; no model
turn, identity/task/revision/trusted-check/review flow was verified. The user
explicitly deferred live setup until after the code fixes. #1082 remains open.
Private worker/runner/reviewer fixture enrollment and generated configuration are
setup artifacts, not successful host evidence. No provider coding CLI was used.

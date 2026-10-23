# Native Codex swarm qualification — core594

The named run `core594-codex-0.163.0-alpha.11-luna-qualified` exercised actual
Codex native collaboration, three Luna implementation workers, independent
reviewer connections, trusted checks, local Git integration and canonical task
acceptance. The controller completed two waves and terminated with
`completed / empty_backlog`, sequence 18, no retained concurrency and no active
actions. The [sanitized receipt](native-swarm-0.163.0-alpha.11.json) binds the
exact runtime, host probe, source hashes and producer records.

This is owner-operated cooperative governance. It is not automatic child-profile
injection, an unattended launcher, or OS-level isolation.

## Actual run

Runtime source: `808802158e4cbc3bce10eb5fb3dc77ec9b440bc4`, built with Go 1.26.10.
The runtime SHA-256 is
`56b24fb590ac5cf08dbf27d4d4610084fc5d28fb8fe39d0ec712f14c2b9b5603`.
Codex reports `codex-cli 0.163.0-alpha.11`. The installation was refreshed through
`codex plugin add dacli-governance@dacli-local`, and all eight installed package
source files matched the repository before the run. A fresh installed worker
process authenticated the renewed task-scoped connection and advertised 13
worker/read tools. Orchestration workers used their own selected 15-tool catalogs.

| Wave | Task | Claim | Exact implementation commit |
| --- | --- | --- | --- |
| 1 | Normalize labels | `alpha` | `11451e3548c1b8a32b989e9125110c918f002699` |
| 1 | Count statuses | `beta` | `9a04c95a49579a0d0ff7603189063c0cea55fb76` |
| 2 | Compose their report | `combined` | `4c98f91dee721b1f5ec3f2ca173ad81838944306` |

The third canonical task depended on both first-wave tasks. It remained
unschedulable until those tasks were canonically DONE. Its checkout then advanced
to the integrated first-wave commit before its attempt and dispatch lease were
created. The same controller invocation recorded `Waves: 2`.

For each task:

1. A fresh controller response authorized its specific native child dispatch.
   The orchestrator used Codex `collaboration.spawn_agent` with `gpt-6-luna` and
   passed a private selection path, never a credential value.
2. A distinct bridge correlated the actual returned native child ID. The child
   authenticated its independently enrolled session through its own installed
   plugin stdio process, registered its checkout and acknowledged that dispatch
   before editing.
3. The child changed only its assigned path and published the exact clean Git
   revision. B rebased onto integrated A before publication. Scoped diffs were
   independently read back against each pinned integration base.
4. A separately enrolled runner executed the configured named check. The check
   exited zero for the exact revision; an exact-key replay returned the same
   evidence and left the execution marker at one byte.
5. An actual Luna reviewer used a separate private reviewer connection, inspected
   the code and edge cases, proposed approval and freshly evaluated eligibility.
   Its candidate bound the worker, lease, fence, revision and trusted evidence.
6. The owner used the controller's exact integration operation key and producer
   tuple with `NativeIntegrationOwner.Apply`. A separate policy decision and
   approval were consumed by `NativeAcceptanceOwner.ApplyWithPolicy`. Readback
   confirmed the canonical DONE task, policy consumption and observed outcome.
   Exact integration/acceptance replays were historical duplicates.
7. The owner explicitly released protection through authenticated-terminal
   recovery after canonical acceptance. Acceptance did not imply termination or
   automatically release the lease.

No coding-agent CLI was launched or wrapped. The DACLI CLI and application APIs
performed governance and owner operations only. Native coding turns were created
through the host's collaboration tools.

## Recovery and causal checks

`internal/application/native_host_producer_crash_test.go` adds four actual OS
subprocess producer-loss tests. Each helper exits after its public producer
commits and before the supervising caller receives its result. Restart retries
the original operation rather than creating another effect.

| Producer | Named test | Regression mutation that fails |
| --- | --- | --- |
| Dispatch acknowledgement | `TestNativeDispatchAcknowledgementProducerProcessLossThenExactRetry` | Drop the replayed lease; exact task/session/attempt/fence assertion fails. |
| Lifecycle observation | `TestNativeLifecycleObservationProducerProcessLossThenExactRetry` | Rewrite a timestamp during replay; authority-byte stability fails. |
| Trusted verification | `TestNativeVerificationProducerProcessLossThenExactRetry` | Lose stored evidence during replay; exact retry fails. |
| Independent review | `TestNativeIndependentReviewProducerProcessLossThenExactRetry` | Return ineligible with nil error; eligibility assertion fails. |

The orchestrator independently reran these four mutations using disposable Go
source overlays. All four named tests failed at their semantic assertions, and
the restored four-test run passed. The unit fixtures explicitly use synthetic
capability declarations; they do not count as the actual Codex run above.

Existing actual integration and acceptance process-death tests remain required:
`TestNativeIntegrationProducerCrashRestartAtGitEffects` and
`TestNativeAcceptanceProducerCrashRestartAtCanonicalClose`. Their before-effect,
partial-effect and after-finalization cases exercise the core592 public owner
producers, not checkpoint callbacks. Unknown execution retains protection and
requires exact reconciliation; it is not successful evidence.

Existing host-neutral authority/adapter fixtures cover the conformance matrix:

| Behavior | Representative existing test |
| --- | --- |
| Concurrent controllers and atomic reservation | `TestNativeSwarmAtomicConcurrentReservationAndLeaseExclusion` |
| Revocation and replacement | `TestNativeRecoveryRevokedExpiredAndConcurrentReplacement` |
| Overlapping scopes | `TestLeaseOverlapAtomicSetAndTaskReservation` |
| Cancellation acknowledgement and replay | `TestNativeLifecycleCancellationAcknowledgementSequenceAndReplay` |
| Unknown usage remains nullable and reserved | `TestNativeBudgetRestartWindowUnknownCarryAndSTOP` |
| Bounded no-progress and explicit recovery | `TestNativeCheckpointOwner595RecoveryIsRealProgressAfterNoProgress` |
| Unknown dispatch cannot redispatch | `TestNativeDispatchUnknownAndManualReconcileWithoutRedispatch` |

## Supported boundary and retained failures

The measured public app-server schema has 175 methods. `agent/spawn` and
`collaboration/spawn_agent` are absent there and both actual requests received
unknown-method refusals. This does not describe the separate native collaboration
tools exercised by this run. The capability diagnostic created no model turn;
the actual native workers and authenticated producer chain are recorded separately.

Private child profiles were delivered manually and selected in separate MCP
processes. A shared MCP connection, default installation catalog or caller label
cannot establish an independent child's identity. Native host IDs are correlation
only, not cryptographic credential bindings. Direct shell/filesystem operations
remain outside cooperative gates. Provider usage/billing, automatic child-profile
injection, stable host dispatch-key lookup and running-turn host cancellation
remain unverified. A second real host, including Antigravity, remains unverified.

The legacy `dacli commit` command reported the root actor and
`path_scope_unavailable` in these native fixture checkouts. The run does not claim
native commit-path enforcement; traceability comes from the independently
authenticated attempt/revision, exact diff checks and independent reviewer.

A superseded attempt was preserved and explicitly stopped. Its owner driver used
`integration-a` instead of the controller's integration key. The controller
correctly refused continuity despite canonical DONE. No old ref or receipt was
rewritten, and that attempt is excluded from successful qualification. The fixed
driver validates the full candidate-to-controller tuple and uses the descriptor's
operation key. The successful run used a fresh private workspace and authority.

## Repeating the owner preparation

Build `./internal/qualification594` and run `qualification594 setup --root NEW_DIR
--probe PRIVATE_CURRENT_PROBE`. The probe comes from
`docs/probes/codex_native_dispatch_probe.py`; protect it with mode 0600. Use a new
private output directory rather than resetting existing authority. The helper
prepares canonical tasks, roles, independent profiles and bounded owner policies;
it never launches a model or impersonates a child.

Select the generated controller/bridge/worker profiles with the installed plugin,
then use their exact advertised `tools/list` schemas. Perform native child
handoffs and independent review through the host. Before each task's trusted
checks and review, `prepare MANIFEST TASK` pins its current target. After review,
`finish MANIFEST TASK CANDIDATE CONTROLLER_STATUS` applies the exact current
integration descriptor and distinct acceptance operation. `release MANIFEST TASK
OBSERVATION` requires canonical DONE and the authenticated completed observation.
Preserve all uncertain-operation identities. Do not substitute fabricated host
labels or test callbacks for actual native workers.

The receipt's validation field records the final integrated local verification
outcomes. Hosted CI is manually dispatched; this qualification dispatches none.

## Final local verification

Source `da2968a73e8c0bc659395c64cbb563e5c9237ec1` (tree `3982a8b11c8c2bcba21bbdff9b2d62ecf5cce686`) passed the
local bar on Darwin arm64 with Go 1.26.10 and golangci-lint 2.12.2. The checkout
remained clean and unchanged throughout verification. Formatting, vet, pinned
lint, workflow contracts, actionlint including docs, both release configuration
checks and shell syntax checks exited zero. All 17 plugin Python tests passed.

The complete `go list ./...` package set passed: 91 packages ran together with
`go test -p 2`; all 221 session top-level tests ran in four disjoint exhaustive
partitions (56, 55, 55, 55). All 221 expected top-level tests produced passing terminal events; none failed
or was skipped. The unpartitioned
authority package had exceeded this machine's cumulative default ten-minute
limit. The partitions preserve default timeouts and assertions; this receipt does
not claim an unpartitioned `go test ./...` pass. The JSON records terminal counts
and a hash of the complete local test receipt.

Independent authority readback matched each tool receipt, verified separate root
actor sessions and evidence/review freshness after the integration epoch, and
found exactly three integrations, three canonical acceptances and three explicit
recoveries. The dependent task was held in wave one. Provider usage remained
nullable throughout. Four producer mutations failed at the named semantic
assertions and the restored source passed; package omission and automatic docs
trigger regressions also went red before their fixes.

Finalization changes only these receipt notes and JSON metadata after the tested
source revision. No Go, workflow or Python source changes follow that verified
revision. Hosted CI remains manual and no hosted workflow was dispatched.

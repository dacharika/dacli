# Native shell validation — #1152

The app is an optional, synthetic, read-only development preview. The farm,
Task journal, Notice board, assigned-agent controls and evidence/activity
inspectors remain conventional alternatives to the decorative canvas. The rail
and footer identify the synthetic workspace; attention, task, attempt and
evidence records use the existing fixture identities. Fixture → Shell
availability (synthetic) is separate from Fixture → Sample state.

## Local build and launch

Observed host: macOS 26.5.1 arm64, Xcode 26.6, Swift 6.3.3. From the repository
root (no coding CLI, sign-in or authority credentials):

```sh
CLANG_MODULE_CACHE_PATH=/private/tmp/shell1152-clang SWIFTPM_MODULECACHE_OVERRIDE=/private/tmp/shell1152-swift xcrun swift test --disable-sandbox --package-path apps/macos --scratch-path /private/tmp/shell1152-build
/private/tmp/shell1152-build/debug/DacliFarmDesign
```

The test command builds the development executable and runs the existing twelve
farm tests plus seven shell tests. The new tests cover loading without details,
validated restoration, same-project retained offline/stale content, no retained
content after a new load or project switch, terminal-state invalidation, removed
adoption identity and the canonical denied/unsupported golden cases. These are
model tests; they do not prove native focus or VoiceOver speech.

For only the affected shell surfaces:

```sh
/private/tmp/shell1152-build/debug/DacliFarmDesign --render-shell-preview /private/tmp/shell1152-renders
```

Debug-only ImageRenderer exports are **content renders**, not window captures.
They retain the existing export adapters for native List/ScrollView wrappers.
The six shell exports cover loading, empty, offline, stale, denied and unsupported
at 1024×746 content points, 2× raster. No new farm redesign is included.

## Selection and accessibility paths

Ready → select CORE-312 → Loading → Ready restores that exact task and its valid
agent; loading has no task rows or inspector. Offline/Stale retain that selection
only if a snapshot was already visible for the same project. Switching to
Moonrise while loading or offline hides prior records; Ready loads UI-084 and
its own evidence. Empty/Denied/Unsupported clear selection and details; loading
synthetic data again selects a valid initial record. There is no automatic retry
or sign-in action. Availability text always pairs a word and symbol, and a
native button explicitly loads local synthetic records.

Command-1 and Command-2 choose Farm and Journal; Option-Command-I toggles details
only when records are visible. Native task buttons retain the existing Tab,
Space and Return handling, and the journal provides fixed controls independent
of moving sprites. New non-content surfaces have an accessible state heading,
plain-language explanation and a native Load synthetic workspace button.

Historical actual-window/keyboard qualification of the preserved farm is in
[visual-system.md](visual-system.md). It does not qualify the new shell state
surfaces. Current native launch, keyboard and accessibility observations are
recorded below when observed. Full VoiceOver speech, macOS 14 runtime, Intel
execution, performance and live integration remain unverified. No hosted CI was
run. Root performs the repository Go checks separately; Swift is not a Go/Linux
build dependency.

## Current checks

Initial local focused run: 18 XCTest tests passed, 0 failures. A seventh shell
test was then added for preserving a valid wardrobe agent independently of the
selected task. This prevents shell restoration from changing the existing
wardrobe interaction.

Causal probes, each restored immediately after the filtered shell tests:

| Temporary mutation | Observed failure |
| --- | --- |
| Remove the same-project retained-snapshot guard | `testOfflineAndStaleRetainOnlyAnExistingSameProjectSnapshot`: `XCTAssertNil failed` after project switching and after a new load. Terminal-state tests also caught cached-content exposure. Exit 1. |
| Skip clearing a terminal state's restoration candidate | `testEmptyDeniedAndUnsupportedDiscardDetailsAndOldSelection`: `CORE-312` was restored instead of initial `CORE-318`. Exit 1, three assertions. |
| Accept every restoration identity | `testRemovedAdoptionIsNotRestoredAndInvalidIDsAreRejected`: invalid identity assertions fail; after strengthening the behavioral checks it also catches retained removed adoption task/agent selection. Exit 1. |

The probes verify model guards, not UI activation. Logs remain in
`/private/tmp/shell1152-mutation-*.log`; final restored-source results and actual
native observations are recorded below.


Final restored-source run: **19 XCTest tests passed, 0 failures** (twelve existing
farm tests plus seven shell tests). `git diff --check` passed. Six shell content
exports completed with exit 0 and were visually inspected: labels and recovery
buttons fit, loading/empty/denied/unsupported contain no task inspector, and
offline/stale retain the journal and evidence under an explicit freshness banner.
These are the 2048×1492 PNG originals in `/private/tmp/shell1152-renders`, not
actual app-window captures. Final development binary SHA-256:
`64da23d36c93746c6fe5d1118ebe93d9718f318d728a89c9197f6574abd77bc5`.

The SwiftPM runs emitted a read-only global manifest-cache warning in the host
sandbox; compilation and every test completed successfully using the isolated
scratch/module caches shown above. No credential file read, networking client or
coding-process spawn is added; the existing debug exporter only writes requested
content-render artifacts. This inspection is a source observation, not a claim
of runtime security containment.

The final footer review preserved the existing disconnected task's last-sample
time independently of the new shell availability label. The incremental suite
after this presentation fix again passed all 19 tests; no new farm render loop
was run because the six exported non-Ready states are unaffected. The final test
log is `/private/tmp/shell1152-tests-final2.log`.

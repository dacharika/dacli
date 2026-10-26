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

## Actual native shell observations — 2026-10-26 UTC

A fresh worker resumed clean owner checkpoint
`44097b08cb23ea7832b9d86f17021cc542b1737d` under a new native session/attempt
and fence 212 after the earlier lease expired. The checkpoint was explicitly an
unqualified preservation commit; the observations below are the subsequent
qualification. No application source changed during this continuation.

Root reported launching the development wrapper
`/private/tmp/dacli-swarm-20261026/DacliShellDesign.app`. The resumed worker bound
that exact path through CUA, observed its running native window, and checked the
wrapper executable SHA-256 against the tested binary:
`64da23d36c93746c6fe5d1118ebe93d9718f318d728a89c9197f6574abd77bc5`.
The fresh CUA interactions below completed responsively with 20-second tool
bounds. They are native window/accessibility observations, distinct from the
ImageRenderer exports and XCTest model checks above.

| Native interaction | Observed result |
| --- | --- |
| Initial window and journal | Ready synthetic workspace, separate attention, three native task rows, assigned agent, attempt CORE-A208, evidence and activity appeared in the accessibility tree. |
| CORE-312 → Loading | The native tree exposed the Loading heading, explanation and local recovery button; task rows, agent controls, inspector and evidence were absent. Attention and details controls were unavailable. |
| Loading → Load synthetic workspace | CORE-312 and assigned agent Fern returned with their task inspector and synthetic evidence. |
| CORE-312 → Access denied | The Access denied heading and explanation appeared with the local recovery button; no task, attempt or evidence content remained in the native tree or captured window. |
| Denied → Load synthetic workspace | A valid initial CORE-318/Mallow selection loaded; the discarded review selection was not restored. |
| CORE-312 → Offline → Stale | The selected task and evidence remained visible beneath explicit last-synthetic-snapshot/current-work-unknown and stale/current-evidence-freshness-unknown banners. |
| Stale → Moonrise | All CORE task rows and the inspector disappeared. The new project said no snapshot was retained and details remained hidden. Loading its synthetic workspace showed UI-084, its own UI-A208 attempt, and UI evidence. |
| Native journal selection and keyboard | Selecting UI-079 showed Fern. Closing details moved native focus to the UI-079 task button. Unmodified Return reopened UI-079/Fern. Closing again and unmodified Space also reopened UI-079/Fern. |

The state headings, explanations, disabled controls and labeled native recovery
button were available through macOS accessibility. This checks accessible
structure and equivalent native navigation; **VoiceOver speech and a full
VoiceOver audit were not run**. Empty and unsupported shell states were covered
by the restored model tests and inspected content renders, not extra native
interaction claims. No window preset dimensions, new animation qualification,
performance benchmark, live backend, enrollment or live usage are claimed here.

Two untouched CUA screenshots were saved and copied byte-for-byte into this
repository. Both are JPEG/JFIF, 2560×1409 pixels; these are actual native app
observations at the existing host window extent, not compact/wide preset proofs.
No cropping, resizing, retouching or format conversion was performed.

| Capture | SHA-256 |
| --- | --- |
| [Denied native window](captures/shell-denied-native.jpg) | `cf54fe2d502c27e9dabcbbd12e37d463cbc4a44726d5ca93b2dec644e4ef0da5` |
| [Stale native window](captures/shell-stale-native.jpg) | `ef68f7ab097715f04b22b346fbc734d67a5aff3f30ca9f5cae787f437020eaf2` |

Raw accessibility observations remain at
`/private/tmp/shell1152-native-denied-ax.txt` and
`/private/tmp/shell1152-native-stale-ax.txt`. The Go/Linux independence check
remains a separate owner-run verification; these native observations do not
substitute for it. All records here describe synthetic fixture behavior only.

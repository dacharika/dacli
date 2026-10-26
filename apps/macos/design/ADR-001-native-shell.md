# ADR 001: optional fixture shell in apps/macos

Status: implemented development shell for #1152; 2026-10-26.

Keep the native app in `apps/macos`, in the Go repository, as an independent
Swift package. The optional client belongs beside the canonical Go contracts so
changes can be reviewed together without making Swift or Xcode a Go dependency.
Go owns workspace state, task lifecycle, authentication, scope, provenance,
review and acceptance. The macOS client owns presentation and local selection.
The operator/native maintainers own this package; contract changes remain with
the Go contract owners. No second scheduler or authority store is introduced.

`Package.swift` declares Swift tools 6.0 and macOS 14. The executable product and
source target are `DacliFarmDesign`; `Tests/DacliFarmDesignTests` tests that target.
`Sources/DacliFarmDesign` contains the existing farm drawing, fixture projections,
observable session, native journal/inspectors, and separate shell availability
states. `design` records the design and qualification; `assets` holds original
artwork. There are no external package dependencies. The package manifest's
minimum is a declared compatibility target, not a runtime qualification.

The minimum **observed working** environment for this change is Apple Silicon
macOS 26.5.1 (25F80), Xcode 26.6 (17F113), Apple Swift 6.3.3, observed via
`sw_vers`, `xcodebuild -version`, and `xcrun swift --version`. The package builds
for its macOS 14 deployment target on this SDK. Swift 6.0 and macOS 14 runtime
compatibility have not been run; no lower verified runtime/toolchain is claimed.
Intel execution, distribution, signing and notarization are also unverified.

This shell intentionally loads only in-memory synthetic `ProjectFarm` and
`FixtureProjection` records. Its separate Ready/Loading/Empty/Offline/Stale/
Denied/Unsupported fixture selector describes shell availability, not task or
agent activity. Offline and stale can retain an already visible same-project
snapshot with an explicit unknown-freshness banner. Loading hides content while
remembering a candidate stable-ID selection. Empty, denied and unsupported clear
selection and retained content. Restoration validates project, task ID, task key
and agent ID; project changes never expose the previous project's details.
Restoration lasts for this development process only; no disk persistence is
claimed. The existing farm appearance and task-state semantics are preserved.

`contracts/operator/v1` remains the sole portable frame contract. Swift tests
read its existing denied and unsupported golden files directly, without copying
protocol fixtures into the app bundle. The app's local state selector does not
consume a backend transport. Implement authenticated read-only consumption under
#1153 only after #1147 and #1148 are ready. The shell reads no authority
credentials and launches no coding CLI. Its labels and recovery button explicitly
say synthetic; fixture acceptance records grant no live authority.

The regular Go build and tests do not invoke this package. This change adds no
CI workflow or CI dispatch; any future native CI must remain manual-only.
See [local validation](native-shell-validation.md) for exact commands,
observations, causal probes and remaining platform/accessibility limits.

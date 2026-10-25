# Patchwork Farms: native visual and interaction contract

**Status:** M1 native SwiftUI design fixture; synthetic and read-only
**Issue:** GitHub #1130
**Package:** `apps/macos/Package.swift`, executable target `DacliFarmDesign`

## Visual direction

The scene is an after-rain allotment: mint sky, layered sage fields, a blue-green pond, rough timber plots, and one warm yellow sun. Pixel animals are drawn from small original 12×12 shape maps in SwiftUI Canvas. The surrounding project rail, picker, list, controls, and inspector use native macOS SwiftUI components and system typography.

| Token | Value | Use |
| --- | --- | --- |
| Deep creek | `#174C4C` | Brand and primary text |
| Pond glass | `#397D78` | Navigation accents |
| Field sage | `#9FBE8B` | Garden ground |
| Rain sky | `#D1ECE7` | Scene sky |
| Seed gold | `#F2C85E` | Focus marks and sun |
| Berry bark | `#BC5E51` | Selected plot outline |
| Note paper | `#FFFDF1` | Inspector and record surfaces |
| Garden ink | `#193A3D` | Main text |

The original SVG sheets in `assets/` document the palette and early sprite shapes. The native fixture draws its own pixel forms with SwiftUI Canvas and does not load those SVGs at runtime.

## Canonical meaning and authority

| Farm metaphor | Canonical meaning | Interaction rule |
| --- | --- | --- |
| Project farm | Existing dacli project | Switching farms changes only local sample content. |
| Task plot and sign | Task and task reference | Selecting a plot opens the native task inspector. Plot location and crop are presentation only. |
| Animal | Agent identity | Selecting an animal shows a sample status and related task. Name, species, costume, and location are cosmetic. |
| Animal activity word | Task/attempt projection state | The word comes from the selected synthetic sample and never claims live freshness. |
| Notice board | Review attention and evidence references | Opens a sample note. It cannot approve, dispatch, or alter a record. |
| Storehouse | Owner acceptance record | Shows a synthetic owner acceptance tied to an explicit revision and digest. A separate review receipt is shown as distinct evidence. |
| Task/evidence inspector | Conventional record view | Shows identifiers, sample status, description, evidence, activity, and the authority boundary. |

The fixture has two farms, Wildflower (`dacli core`) and Moonrise (`operator UI`), with three agents and three tasks each. Task, attempt, evidence, note, acceptance, revision, and review-receipt IDs are project-distinct (`CORE-…` versus `UI-…`). The six-state picker selects running, waiting, blocked, disconnected, review needed, or accepted synthetic snapshots. A review receipt alone is never presented as owner acceptance. No backend, scheduler, account data, or live protocol is included.

The disconnected state keeps the last sample visible and labels it as a synthetic sample. It does not imply the task stopped. Review needed does not imply owner acceptance. The accepted sample shows owner acceptance for a specific revision and digest, with a separate review receipt.

## Native interaction and accessibility

- `NavigationSplitView` provides the project sidebar, working scene, and native inspector. The sidebar shows each project's name and its review attention count.
- The compact two-row header keeps the native Sample state picker, Garden/List segmented picker, Display menu, and inspector button together. The menu contains High contrast and Selection motion. The list presents the same three tasks as the garden plots; neither view changes task status. A persistent Synthetic preview label identifies the local fixture.
- The status summary wraps the active work and shows sample freshness on its own line. The main scene and inspector scroll independently below their fixed headers. The bounded HSplitView preserves a farm column of at least 440 points and an adjustable inspector between 270 and 320 points.
- Plots, animals, notice board, and storehouse are buttons with descriptive accessibility labels and native keyboard focus. Selecting a list row opens its task evidence. The close control and inspector-toggle close path explicitly reset and then restore keyboard focus to the selected task in the current Garden or List view. Task targets accept keyboard input independently of the macOS Keyboard Navigation preference; unmodified Space or Return opens that task’s details. The list row and plot use the same focus/activation modifier and a dashed visible outline. Other native controls retain standard macOS keyboard behavior. Escape is not an inspector-close shortcut. Dashed deep-creek outlines distinguish keyboard focus on plots, buildings and animals from the solid berry plot selection and gold animal selection.
- State is expressed with text and symbols as well as color. Task titles and evidence labels use at least 12 points; short state and agent captions use at least 11 points and have combined accessibility labels. Plot state text wraps instead of shrinking.
- The Motion control disables small selection transitions. The macOS Reduce Motion environment setting always takes precedence. The farm scene itself has no looping animation.
- High contrast changes the fixture palette; the macOS Increase Contrast setting also activates that palette and cannot be overridden by the menu. This is an alternate palette, not a claim of WCAG certification.
- The development-only Fixture menu has Compact window (1024×768) and Wide window (1440×900) actions. These sizes are outer window frames in logical points; macOS may constrain a preset at a screen edge or when the screen is smaller. The window content minimum is 1024×660 points.
- The package declares macOS 14 as its deployment minimum. Build review has been performed with Xcode 26.6 / Swift 6.3.3; the baseline received native runtime review on macOS 26.5.1; the Sol refinement still needs fresh native captures. macOS 14 compatibility and VoiceOver behavior remain unverified.

The sRGB relative-luminance checks from the original palette apply to listed flat color pairs only. They do not certify all colors after SwiftUI compositing or every native rendered state. Recalculate against the actual native appearance before claiming conformance.

## Original starter assets and rendering

The native animals are three original 12×12 color maps in `FarmCanvas.swift`: fox, rabbit and raccoon. They were drawn for this fixture with distinct ears, muzzles, eye masks, feet and tails; no commercial-game sprites, sprite libraries or downloaded artwork were used. The scene uses the garden tokens above. Animal colors add rust `#BA5938`, ear gold `#EBAD57`, oat `#D1BF9E`, blush `#B8617A`, slate `#6E827D`, cream `#FCE0A8`, and dark ink `#1A3333` (rounded sRGB values).

Each occupied cell is filled edge-to-edge in native Canvas. Cell size is an integral number of logical points, centered in the drawing bounds: 4-point cells for a 48×48 scene animal and 3-point cells for a 36×36 inspector animal. This is direct integer-grid drawing with no bitmap interpolation. If exported as raster assets later, use nearest-neighbor sampling and integer scale factors. The bounded starter set has one static pose for each species. Running, waiting, blocked, disconnected, review-needed and accepted meanings are readable text and symbols beside the animal; no unimplemented animation is implied. Only selection transitions animate, and the menu and macOS Reduce Motion setting can disable them.

The provenance SVG sheet remains an earlier original exploration: animal symbols and utility icons use 32×32 viewboxes; notice-board and storehouse symbols use 64×64 viewboxes in a 128×96 sheet. `field-tiles.svg` records the original field study. Neither SVG is loaded by the app. Buildings, pond, fence, plots, path and field texture are native shapes, symbols and Canvas drawing; their location and decoration remain cosmetic.

## Sol native design review

The baseline native 1024×768 capture showed a truncated active-work summary and sample timestamp, three crowded header rows, separated mosaic-like animal cells, and repeated explanatory cards competing with evidence. The refinement keeps the after-rain palette and makes the farm the expressive element: two quiet header rows, native view navigation, wrapping status/freshness, connected original silhouettes, readable state captions and a concise inspector authority note. Project sidebar subtitles use native secondary text so selection can adapt to the system highlight.

This is a contextual product/design review, not a reviewer acceptance or evidence of live integration. The fixture model and its task/agent mappings are unchanged. Fresh actual-native captures at 1024×768 and 1440×900, keyboard traversal, and the six sample-state mappings remain required for final visual review. Open choices are whether later live work needs an always-visible display menu title, richer animal poses, and a denser layout for more than three tasks. No runtime frame-time, CPU or memory claim follows from this design pass.

## Build and launch

From the repository root:

```sh
xcrun swift build --package-path apps/macos --scratch-path /private/tmp/dacli-operator-build-20261025/swift-build
xcrun swift run --package-path apps/macos --scratch-path /private/tmp/dacli-operator-build-20261025/swift-build DacliFarmDesign
```

The optional Swift package is independent of the Go build and has no external package dependencies. `.build`, `.swiftpm`, and `DerivedData` are ignored locally.

## Performance and evidence limits

No native frame-time, CPU, or memory measurements have been collected. On Xcode 26.6 / Swift 6.3.3, the latest focused test build completed in 2.37 seconds and all three fixture tests passed in 0.003 seconds; these timings are build/test timings, not runtime performance results. The original proposed budgets (idle 60 fps, transition 45 fps, texture memory below 8 MiB) were planning targets for a later sprite implementation; they are not measured results for this SwiftUI fixture. A native performance pass should record device, OS, display scale, p50/p95 frame time, CPU, and peak memory at 1024×768 and 1440×900 with the default and a larger task set.

## Source-backed gaps for follow-on work

- The sample plots have no dependency edges. The operator interview says the dependency chain is manually reconstructed from task files today (`docs/research/interviews/operator.md:111–113`); a separate backlog task covers plot and dependency paths (`t-01M5SKD4NETD7P2YV5AX84BX2M`). Plot adjacency remains decorative until the data mapping is defined.
- The fixture cannot distinguish an agent that is thinking from one that is hung, or show whether work is moving in the requested direction (`docs/research/interviews/operator.md:76–102`). That interview is one source and marks these findings unverified until they recur across three sources (`docs/research/interviews/operator.md:3–8`). Treat them as follow-up questions, not universal operator behavior.
- There is no authenticated read-only client or backend subscription in this fixture. Separate roadmap work covers the SwiftUI client prototype (`t-01M5SKD4CTYWJ9Q5CRPHAEQ6SG`), read-only operator transport (`t-01M5SKFBTKZPFCH4424XYJQYTY`), and bounded projection subscriptions (`t-01M5SKJQ60VA0RJ4KZGXGQQSPE`).
- VoiceOver, macOS 14 compatibility, and native rendering performance remain unverified. No browser screenshots are evidence for this SwiftUI fixture.

## Keyboard repair review

Actual native review of commit `bfa7ea14` found that selecting CORE-316 in List view and closing details did not leave a visible focus ring, and neither Space nor Return reopened the selected task. The original source attached FocusState to plain buttons but did not create an explicit keyboard-input focus target; it also reused the selected focus value after closing. This is preserved as a failed behavioral observation, not reclassified as a passing documentation check.

The repair gives Garden plots and List task rows a shared native SwiftUI input-focus wrapper, handles Space/Return locally, and clears focus before a deferred restore after the split view changes. It changes no system setting. The compiled fixture’s model tests do not verify UI focus; the same native close-and-reactivate sequence must be repeated on the repair binary before claiming this behavior is qualified. Full VoiceOver and macOS 14 runtime checks remain pending.

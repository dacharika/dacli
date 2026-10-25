# Patchwork Farms: native pixel farm

Status: original SwiftUI design fixture, synthetic and read-only. This is a full visual redesign of the initial native fixture. It does not add a client connection, workspace mutation, scheduler, review action, or acceptance authority.

## Direction

An original top-down 16-bit farming world fills the workspace: warm grass, a stepped treeline, crop rows, a sand path, timber buildings, a pond with a dock, and three clothed animal farmhands. Rust roof tiles, grain in timber, quiet parchment texture, tiny flowers and vegetable crates add scale. The task signs belong to the world rather than floating in an unrelated card dashboard.

The artwork uses the visual grammar of cozy farming games as a reference. Every tile, tree, crop, building and animal is original SwiftUI Canvas code. No commercial-game sprites, logos, downloaded fonts, or external image assets are used. `PixelFarmPainter` authors a 400×340 world with rectangle edges rounded to whole logical points; `PixelAnimal` uses original 16×14 cell maps rendered at integral cell sizes. The map scales to its viewport; the animals keep integral pixel cells. There is no blur or bitmap interpolation.

Georgia Bold gives the journal and wooden signs the character of a storybook. Native system type carries task records, evidence and controls. Parchment is a functional record surface; the dark wood rail is persistent navigation, and the main wood toolbar is a compact game HUD. The surrounding UI uses squared, double-beveled timber borders and small inventory slots rather than large rounded cards.

| Token | Value | Purpose |
| --- | --- | --- |
| Dark timber | `#452C20` | Project rail, structural outlines |
| Timber | `#80512F` | Toolbar and inventory frame |
| Honey wood | `#BB8246` | Rules, beveled highlights |
| Parchment | `#F6E5B9` | Native journal and inspector |
| Cream | `#FFF0C9` | Selected signs, inventory slots |
| Soil ink | `#3C291E` | Record text |
| Meadow | `#83A94B` | Terrain |
| Creek | `#4C9298` | Pond water |
| Seed gold | `#F2CA67` | Selected item accents |

## Layout and behavior

The 174-point left rail contains the farm switcher, Task journal, Notice board and Storehouse. Review attention is visible near the bottom. A single “Synthetic preview” indication identifies the environment, with a tooltip explaining its local data.

The compact top toolbar contains the current farm/project, Farm/Journal view switch and details toggle. The Sample state picker, contrast override, motion override and window presets are in the development-only Fixture menu. The Farm menu exposes Command-1, Command-2 and Option-Command-I shortcuts.

The center view contains a slim status strip, a world map, and a three-slot farmhand inventory. Plot signs are native buttons with task IDs, titles and a state word plus symbol. Selecting a plot or a journal row opens the same task record; selecting a farmhand opens agent identity. The Notice board opens review attention. The Storehouse opens the separate owner acceptance record.

The 320-point parchment inspector is a journal page with a fixed title bar and independently scrolling contents. Task identity, description, assigned agent, evidence and activity are distinct sections divided by quiet rules. Agent detail shows identity and its related task. Storehouse detail shows owner acceptance tied to revision and digest, plus its separate review receipt. There are no synthetic “Accept” or “Dispatch” controls.

The Journal tab is a native SwiftUI `List` with the same three tasks and canonical projection as the map. It presents task ID, full title, assigned agent and state. It is an equivalent, conventional way to navigate the record, including with the keyboard.

At 1024 points the rail remains 174, the inspector 320, and the center approximately 530. At 1440 points the farm gains landscape space without expanding controls. The center has a 440-point minimum, and the window minimum is 1024×660. Closing details gives that width back to the farm or journal.

## Meaning and authority

The existing two projects, three tasks per project, three agents per project and six sample snapshots are unchanged. `FixtureModels.swift` and its semantic tests remain intact.

| Presentation | Canonical meaning |
| --- | --- |
| Farm | Project |
| Plot and sign | Task |
| Farmhand | Agent identity; species and appearance are cosmetic |
| State label | Existing task/attempt snapshot projection |
| Notice board | Review attention and evidence references |
| Storehouse | Owner acceptance bound to revision/digest |
| Journal | Conventional task and evidence navigation |

Task, attempt, evidence, review and acceptance IDs are retained. A completed attempt is not owner acceptance; a review receipt is not owner acceptance. The disconnected sample keeps the last visible content and labels its freshness; it does not claim work stopped. Farm actions select records only. Actual task changes and approvals remain under dacli.

## Accessibility

Task signs and journal rows share the existing explicit input-focus modifier. Tab focus is visible as a dashed outline, distinct from solid selected timber/cream styling. Unmodified Space and Return open details. Closing the inspector clears then restores the selected task focus. These remain native interactions, requiring live runtime verification after any redesign.

Statuses always have a word and symbol; color is supplementary. Main record text is 12–13 points, titles 19–26, and compact metadata 10–11. Plot task titles are 12 and wrap. The map itself is decorative to accessibility; its native controls have complete labels. The journal provides an equivalent task list. Animal appearances are explicitly cosmetic in agent detail.

There is no ambient or looping animation. Optional view-mode transition respects Reduce Motion. macOS Increase Contrast activates the stronger palette and cannot be overridden by the Fixture toggle. The design does not claim WCAG certification or a completed VoiceOver audit.

## Development renders and verification

`FarmPreviewExport` adds an opt-in development argument, `--render-preview <directory>`. In debug builds it uses `ImageRenderer` to export the same reusable SwiftUI design at 2× scale. Release builds do not activate the export argument. It exits before opening a window. The export includes wide/compact farm, journal, agent, acceptance and disconnected compact views.

The exporter substitutes plain containers for native ScrollView/List wrappers, which ImageRenderer omits. It reuses the exact inspector page and task-row content views, and bounds the inspector to a clipped viewport. Native list selection, scroll behavior and platform rendering remain outside this artifact.

These are **native view renders, not actual window captures**. They verify design composition and SwiftUI rendering only. They do not verify macOS titlebar behavior, focus activation, native menu behavior, live window resizing, VoiceOver, or performance. Actual app interaction uses the approved computer-use interface; the renderer does not inspect or control the computer.

From the repository root, with isolated writable caches:

```sh
CLANG_MODULE_CACHE_PATH=/private/tmp/dacli-farm-clang SWIFTPM_MODULECACHE_OVERRIDE=/private/tmp/dacli-farm-swift xcrun swift test --disable-sandbox --package-path apps/macos --scratch-path /private/tmp/dacli-farm-build
/private/tmp/dacli-farm-build/debug/DacliFarmDesign --render-preview /private/tmp/dacli-farm-renders
```

The package has no external dependencies. No Go sources or backend behavior change in this design slice. Preserve the existing fixture tests. Do not treat them as focus tests. macOS 14 runtime compatibility, full VoiceOver behavior and runtime performance remain unverified.

## Redesign qualification record

The proposal was built and tested on macOS with Xcode 26.6 / Swift 6.3.3. All five focused Swift tests passed: the three existing fixture projection tests plus two selection-transition tests. The fixture model and its existing tests are unchanged.

Mutation evidence: temporarily removed `selectedAgentID = task.agentID` from `FarmSession.select`. `FarmSessionTests.testSwitchingFarmReplacesAgentAndAcceptanceIdentity` failed with the stale `core-mallow` identity instead of `ui-mallow`; the Accepted-to-Review test also caught `core-mallow` instead of `core-fern`. The original selection update was then restored and the focused suite rerun. This mutation verifies selection safety, not UI activation.

Visual review inspected farm wide, journal wide, acceptance wide and disconnected compact exports. Exports are view artifacts with the wrapper limitations above. Live focus restoration, actual native List rendering, menu actions, titlebar sizing, macOS 14, VoiceOver and runtime performance remain pending. No full Go suite was run for this native-only proposal.

# Patchwork Farms: native pixel farm

Status: original SwiftUI design fixture, synthetic and read-only. This is a full visual redesign of the initial native fixture. It does not add a client connection, workspace mutation, scheduler, review action, or acceptance authority.

## Direction

An original top-down 16-bit farming world fills the workspace: warm grass, a stepped treeline, crop rows, a sand path, timber buildings, a pond with a dock, and three role-based animal farmhands, with a fourth slot for an adoption preview. Rust roof tiles, grain in timber, quiet parchment texture, tiny flowers and vegetable crates add scale. The task signs belong to the world rather than floating in an unrelated card dashboard.

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

The center view contains a slim status strip, a world map, and a fixed farmhand inventory. Plot signs are native buttons with stable work IDs, Task/Issue kind, Open/Done lifecycle, titles and a state word plus symbol. Selecting a plot or a journal row opens the same task record; selecting a farmhand opens agent identity. The Notice board opens review attention. The Storehouse opens the separate owner acceptance record.

The 320-point parchment inspector is a journal page with a fixed title bar and independently scrolling contents. Task identity, description, assigned agent, evidence and activity are distinct sections divided by quiet rules. Agent detail shows identity and its related task. Storehouse detail shows owner acceptance tied to revision and digest, plus its separate review receipt. There are no synthetic “Accept” or “Dispatch” controls.

The Journal tab is a native SwiftUI `List` with the same open work and canonical projection as the map. Completed work is linked separately through the Storehouse. An adoption preview adds its own fourth work item and agent. It presents task ID, full title, assigned agent and state. It is an equivalent, conventional way to navigate the record, including with the keyboard.

At 1024 points the rail remains 174, the inspector 320, and the center approximately 530. At 1440 points the farm gains landscape space without expanding controls. The center has a 440-point minimum, and the window minimum is 1024×660. Closing details gives that width back to the farm or journal.

## Meaning and authority

The two baseline projects retain their original three work/agent identities, six state snapshots and historical acceptance/evidence records. The original projection tests remain intact. The model now distinguishes work kind, role, attributed usage, accepted land turnover, a separate next open issue and an optional fourth adoption-preview agent/work pair.

| Presentation | Canonical meaning |
| --- | --- |
| Farm | Project |
| Plot and sign | Task |
| Farmhand | Agent identity; species and appearance are cosmetic |
| State label | Existing task/attempt snapshot projection |
| Notice board | Review attention and evidence references |
| Storehouse | Owner acceptance bound to revision/digest |
| Journal | Conventional task and evidence navigation |

Task, attempt, evidence, review and acceptance IDs are retained. A completed attempt is not owner acceptance; a review receipt is not owner acceptance. The disconnected sample keeps the last visible content and labels its freshness; it does not claim work stopped. Record actions select records. Cosmetic, adoption and lifecycle controls change this local preview only. Actual task changes, agent enrollment, entitlements, purchases and approvals remain under dacli and its separately implemented integrations.

## Accessibility

Task signs and journal rows share the existing explicit input-focus modifier. Tab focus is visible as a dashed outline, distinct from solid selected timber/cream styling. Unmodified Space and Return open details. Closing the inspector clears then restores the selected active-work focus, falling back to the first open item when historical completed work is selected. The native checks recorded below cover plot and list activation and focus restoration. They do not constitute a full keyboard or assistive-technology audit.

Statuses always have a word and symbol; color is supplementary. Main record text is 12–13 points, titles 19–26, and inspector metadata 10–11. Plot task titles are 12 and wrap; compact kind/ID captions are 8, with full identifiers available in the journal, inspector and accessibility labels. The map itself is decorative to accessibility; its native controls have complete labels. The journal provides an equivalent task list. Animal appearances are explicitly cosmetic in agent detail.

The resident layer continuously animates while the scene is active and motion is allowed. Reduce Motion and the Fixture Farm animation toggle stop movement, gait, hops and particles while keeping stationary state cues. View-mode transitions also respect Reduce Motion. macOS Increase Contrast activates the stronger palette and cannot be overridden by the Fixture toggle. The design does not claim WCAG certification or a completed VoiceOver audit.

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

Visual review inspected farm wide, journal wide, acceptance wide and disconnected compact exports. Exports remain view artifacts with the wrapper limitations above. The actual native checks below are separate evidence for the listed interactions and window sizes. No full Go suite was run for this native-only change.


## Historical native qualification — 2026-10-25

Root reviewed the running native app through CUA. On `aa423429`, selecting plot CORE-316 showed Pebble and its task inspector; closing details and pressing Space reopened the selected plot, and closing again and pressing Return also reopened it. Command-2 showed the real native task List. Selecting CORE-312 updated the inspector and assigned agent to Fern, and closing List details then pressing Space reopened that task. The task-selection, list and keyboard code is unchanged in the subsequent preset-helper revision `1f3f607a`.

Earlier on 2026-10-25, native capture attempts returned white or cropped imagery even after the Mac was unlocked. Those remain failed capture attempts, not accepted visual evidence. They are superseded for visual review by the complete actual-window captures below. Earlier Compact menu attempts also left a 3024×1896-pixel window and did not qualify the requested size. Revision `1f3f607a` adds a guarded own-app principal-window fallback when no eligible key window is available. Root then quit using the actual app menu and relaunched the fixed binary; a preceding background-host Command-Q attempt had not terminated the old process. The observations do not establish which of those conditions caused the earlier preset failure.

After that relaunch, Root exercised both Fixture presets and confirmed complete readable windows, native titlebars and no blank/cropped panes at the requested sizes:

| Fixture preset, outer window points | Actual capture pixels | Untouched native capture |
| --- | --- | --- |
| Compact, 1024×768 | 2048×1536 | [Compact farm](captures/farm-1024x768-native.jpg) |
| Wide, 1440×900 | 2880×1800 | [Wide farm](captures/farm-1440x900-native.jpg) |

These are actual native-window captures, separate from ImageRenderer artifacts. The CUA source paths ended in `.png`, but their bytes are JPEG/JFIF. The two files are stored with `.jpg` extensions and copied byte-for-byte without conversion, cropping, resizing or retouching. SHA-256: compact `8d042e9833097112e58cad35c2b859cd20d3ea7fb43ca41b0253c18cca4f45f8`; wide `a50e75590c9da2156009de76cef40305d370f8854c15e6c06521dc79cdbd486b`.

Root's final review was recorded at 2026-10-25 23:22 UTC against source commit `1f3f607a2151e052dc4980a7e32ec01069b1d854`, binary SHA-256 `a1e1b0acf6d70ecd57ba27bf50758bdd66445ec07bb11f2b719d83a4436bc57a`. All six Fixture sample states visibly selected their corresponding states. Switching to Moonrise used its own task, agent and acceptance/evidence IDs. Its accepted Storehouse displayed revision `UI-r24`, digest `sha256:86bf0c17`, separate review receipt `UI-R019` and acceptance record `ACCEPT-UI-02`. The disconnected Moonrise task `UI-084` retained its last content and clearly labeled connection/freshness. These are observed synthetic-fixture behaviors, not a live connection or a dacli approval operation.

The checks above qualify these fixture interactions and supported-size captures on the reviewed Mac. Full VoiceOver behavior, macOS 14 runtime compatibility, runtime performance, and live backend integration remain unverified. Contrast and Reduce Motion settings are implemented but are not a completed accessibility certification.


## Living farm interaction contract

This later product steering adds genuine resident animation, roles, output-driven cosmetic growth, accepted land turnover, adoption and skins. The historical native captures above show the preceding static revision; they do not verify the new animation or shop/lifecycle flows. The new native binary requires fresh CUA qualification. The earlier toolbar-only contrast adjustment was insufficient in an actual window capture; the living revision requests a dark window preferred scheme and a light content environment, with its native titlebar contrast still pending actual capture.

### Roles, movement and emotion

Builder defaults to the original fox, Planner to the original rabbit, and Reviewer to the original raccoon. Their fixed inventory slots and inspectors display role badges. The world adds a workbench/hammer, map table and review shelf/clipboard, plus a southern stepping trail, seed sack, wheelbarrow and harvest crates. Original owl, cat and bear sprites are costume appearances; they do not change identity or role.

`FarmResidents` is an isolated native `TimelineView`/Canvas subtree with a minimum animation interval of 1/12 second. Static terrain, native task controls, journal and inspector remain outside that schedule. Four bounded paths are calculated deterministically from time, agent slot and projected state. The fourth path and northern seed bed appear only for an adoption preview. This is a rendering architecture, not a measured CPU or frame-time claim.

| Projected state | Decorative movement / pose | Persistent cue |
| --- | --- | --- |
| Running | Walks a bounded loop, alternates feet, pauses for tool work | Thinking dots / work sparkle, hammer/map/clipboard |
| Waiting | Short slow walk followed by an idle pause | Question bubble; Waiting word and pause symbol |
| Blocked | Stationary | Red exclamation bubble; Blocked word |
| Disconnected | Stationary; last work remains visible | Muted connection cross; Disconnected and last-sample freshness |
| Review needed | Paces, then pauses with review prop | Search/review bubble; Review needed word and eye symbol |
| Accepted | Walks with small happy hops and sparkles | Heart, Accepted word and checkmark; acceptance remains revision-bound |

Pointer hit testing uses the same resident-frame positions and growth size as drawing, so the click target follows its visible animal. The canvas is decorative to accessibility. Fixed inventory controls and the native journal provide stable keyboard/AX navigation; focus does not chase moving animals. The scene's active state, macOS Reduce Motion and the Farm animation toggle gate the animation schedule. Inactive/background or reduced-motion views show stationary sprites with meaningful state bubbles and readable labels. No idle movement implies an actual backend heartbeat.

### Output tokens and cosmetic growth

Growth uses **attributed sample output tokens**, never input tokens or invented elapsed activity. Mallow's sample is 18,400 output tokens attributed to the project's A208 attempt; Fern's is 31,200 attributed to A203. Pebble and a newly adopted preview agent have unavailable usage. Their neutral sprites have no inferred growth stage.

| Attributed output sample | Cosmetic stage | World pixel cell |
| --- | --- | --- |
| Unavailable / invalid | Growth unavailable; neutral sprite | 3, with no growth-stage claim |
| 0–4,999 | Sprout | 2 |
| 5,000–24,999 | Growing | 3 |
| 25,000+ | Seasoned | 4 |

The inspector shows the exact output count, attempt attribution and sample status. The inventory abbreviates the count and retains a full accessibility label. Fixture → Selected agent output sample can demonstrate these bands or unavailable usage; overrides are explicitly attributed to a project USAGE-PREVIEW sample and do not rewrite the recorded fixture usage. Thresholds are cosmetic fixture choices, not productivity scores, permissions or acceptance policy.

### Open land, harvest and history

Land represents open Task/Issue records. Only the canonical Accepted snapshot removes its primary work item from the active count; a completed attempt or review receipt cannot harvest the land. The accepted lifecycle preview proceeds through:

1. **Harvested:** the original ID is marked Done/Harvested, crops become stubble and harvest crates, and two baseline work items remain active.
2. **Fallow land:** the completed task leaves the plot and the native sign offers the next open issue.
3. **Next open issue:** the explicitly queued CORE-321 or UI-087 fixture record takes the freed land as a separate Issue with its own ID. The original CORE-318/UI-084 record remains in the Storehouse with its original acceptance revision/digest and separate review receipt.

The visible Sample lifecycle control and Fixture → Accepted land preview operate on local presentation only. Journal active counts and rows exclude the completed primary ID, include its replacement when present, and link completed history through the Storehouse. The replacement has its own waiting state and no inherited owner acceptance. The per-project `queuedTasks` array is the only refill source. Fixture → Empty queued backlog demonstrates the empty case: land remains fallow, the UI says Backlog empty, and no replacement record or ID is invented. Waiting and blocked work remain active land; runnable-frontier readiness is not an active-land filter.

### Animals means adoption; Skins means appearance

The Wardrobe & shop inspector has separate Animals and Skins tabs. Animals previews adopting **another agent**, selecting a role/default species and an independent work state. The extra farmhand receives a new stable synthetic identity (for example `wildflower-adopt-1`), its own task (for example `CORE-P001`), fourth land plot and independent activity. Only Running, Waiting, Blocked and Review needed are offered; adoption cannot mint accepted authority. The preview is bounded to one extra agent per project and can be removed. A monotonic session counter prevents reusing an agent/work ID after removal. Project switching retains each farm's own preview.

Skins dresses an existing selected agent. Work clothes and Meadow scarf are sample-owned; Starlight, Rainy day and animal costumes are preview-only. Trying on or restoring a skin changes only appearance. It cannot change the selected agent's role, identity, assigned work, usage, permissions or acceptance. The flow offers preview adoption, try-on and restore/remove controls. It has no coins, invented prices, payment, purchase success, real ownership grant or live enrollment claim.

### Living revision verification limits

The focused Swift suite now contains twelve tests, including the five earlier tests unchanged. New checks cover an empty queue remaining fallow without invented work; distinct role defaults; output-only thresholds and unknown usage; accepted-only land turnover and replacement identity; reversible cosmetics without authority changes; independent adoption identity/work and non-reused IDs; and bounded moving paths with stationary reduced-motion poses. Path tests verify the motion calculation, not actual native frame delivery or mouse activation.

The expanded debug renderer exports farm compact/wide, role/usage detail, adoption, Animals/Skins, harvested/fallow/replacement and two sampled motion times. Native Menu/Picker wrappers have labeled static adapters in these artifacts, as do the earlier List/ScrollView wrappers. These are deterministic content renders, never a substitute for live motion, click targets, focus, menus, titlebar, VoiceOver or performance checks. Fresh native animation, adoption, lifecycle, growth-control and titlebar results remain pending root's CUA review. Full VoiceOver, macOS 14 runtime, runtime performance and live usage/commerce integration remain unverified.


Causal mutation evidence for the living revision: allowing Review needed to free land produced three assertion failures; inferring Sprout for unavailable usage produced five; forcing reused adoption IDs produced two; disabling path movement produced four; and making Planner reuse Fox produced three; and inventing an empty-queue fallback (also admitting foreign/done queue items) produced five. Each probe exited 1, its source was restored, and the complete focused suite returned green. These probes qualify model/kinematic guards; actual native animation and interaction remain a separate runtime check.

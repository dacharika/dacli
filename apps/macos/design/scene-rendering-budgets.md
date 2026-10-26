# ADR 002: native farm renderer and measurement budgets

Status: initial renderer decision and **unmeasured target contract**, 2026-10-26;
operator/34, GitHub #1160. Implementation and runtime qualification belong to
operator/35 / #1161. This document changes no renderer, fixture, wire schema,
authority, or release behavior.

## Decision and comparison

Keep the current native SwiftUI composition: static `FarmLandscape` Canvas,
native task buttons, inventory, journal and inspectors, with only
`FarmResidents` on a `TimelineView(.animation(minimumInterval: 1 / 12))` schedule.
Preserve the original cozy pixel farm, walking animals, role props, emotions,
cosmetics and synthetic shell states. This is an initial architectural choice,
not evidence that Canvas outperforms SpriteKit or already meets these budgets.

| Concern | SwiftUI composition with Canvas (chosen initially) | SpriteKit hosted in SpriteView |
| --- | --- | --- |
| Existing scene | Preserves the reviewed implementation and bounded deterministic motion; no translation of the farm is required. | Requires translating the decorative scene, poses and hit testing to nodes/textures, plus an explicit bridge from the existing presentation projection. |
| Drawing and animation | A single resident Canvas avoids a native view per pixel; its isolated 12 Hz schedule keeps terrain and record controls outside animation invalidation. Larger fixtures still need profiling and visibility bounds. | Retained nodes, texture atlases and nonvisible-node culling are useful candidates for larger scenes. No measured advantage is claimed here. |
| Native interaction | Existing native buttons/List provide stable keyboard focus and accessible records independently of moving animals. Canvas remains decorative. | Keep the same native controls around SpriteView. Moving nodes must not become the only selection or evidence path. |
| Scheduling | Existing motion gate uses active scene, local pause and Reduce Motion. Minimized/occluded-window suspension needs explicit qualification. | SpriteView exposes pause, preferred frame rate and render gating; visibility and lifecycle still need explicit handling. A requested frame rate is a preference, not a measured rate. |
| Cost/risk | No additional framework state or asset conversion. Main-thread drawing and invalidation may become bottlenecks. | Adds node/texture lifetime and synchronization work. It must remain a derived visual projection, never a second task engine. |

Apple documents [SpriteView](https://developer.apple.com/documentation/spritekit/spriteview)
and its [culling options](https://developer.apple.com/documentation/spritekit/spriteview/options).
Its [preferred frame rate](https://developer.apple.com/documentation/spritekit/skview/preferredframespersecond)
is selected according to display capabilities. Those capabilities motivate a
controlled comparison; they do not predict this farm's performance.

Reconsider only after #1161 measures this chosen composition against the frozen
budgets below. First investigate unnecessary invalidation, repeated projection
lookups and offscreen drawing. If a budget still fails in two of three valid
runs, a bounded SpriteView resident-layer prototype may be compared using the
same fixture, viewport, cadence and native selection controls. Require every
budget to pass and at least 20% lower resident-update p95 CPU duration without
regressing footprint, energy, appearance or accessibility before proposing a
renderer replacement. A faster renderer does not grant live transport authority.

## Measurement conditions and fixtures

The qualification baseline is a local Apple Silicon Mac, macOS 26.5.1 (25F80),
Xcode 26.6 (17F113), Swift 6.3.3, arm64. These versions were observed locally;
they do not qualify performance. Record the actual chip/model, RAM, OS build,
SDK, compiler, commit, binary SHA-256 and profiler version in every result.
The declared package minimum remains macOS 14 / Swift tools 6.0. Intel and the
minimum runtime are unqualified until separately run; do not extrapolate.

Use an optimized release build without debugger or screen recording, AC power,
Low Power Mode off, nominal thermal state, one app window, one 60 Hz display at
2× backing scale, and no unrelated build/capture load. Record deviations and
rerun the baseline rather than silently mixing populations. Measure both exact
outer-window presets, 1024×768 and 1440×900 points, with the inspector open;
record actual content bounds and pixel dimensions. If a preset fails, that run
is invalid for its size. Accessibility and Reduce Motion runs are separate
conditions. A content export does not establish real window size or cadence.

For each size and population: launch fresh, warm up 30 seconds, record 120
seconds, and repeat three times. Report each run and the worst qualifying run;
do not average away a failed run. Use nearest-rank p95/p99 over raw per-update
or presentation samples, reporting sample count, maximum and excluded periods.
Intervals crossing a deliberate visibility transition are labeled separately,
not removed without explanation. Never combine debug and release samples.

| Fixture | Deterministic population and visibility | Current availability |
| --- | --- | --- |
| 3 agents | Existing baseline farm, original three roles/identities; all 3 residents and related open plots visible. | In-memory shell exists; no performance run is recorded here. |
| 20 agents | 20 unique synthetic stable agent/task IDs, 7 builders / 7 planners / 6 reviewers; separate spatial anchors, all 20 residents visible. | Planned #1161 fixture, not an existing menu or command. |
| 100 agents | 100 unique synthetic stable agent/task IDs, 34 builders / 33 planners / 33 reviewers; camera/page culls to at most 32 visible residents, with all 100 reachable in native navigation. | Planned #1161 fixture and bounded viewport, not implemented here. |

For planned fixtures, use IDs `bench-agent-000`… and `bench-task-000`… scoped to
one synthetic project. Assign roles by ordinal modulo 3; cycle Running,
Waiting, Blocked, Disconnected, Review needed and Accepted by ordinal modulo 6.
Accepted records remain historical and do not count as open land; attach their
separate synthetic revision/digest/evidence references. Cycle attributed output
samples unavailable / 0 / 4,999 / 5,000 / 24,999 / 25,000 / 42,000 without
inferring growth for unknown usage. Keep task titles and evidence payload sizes
fixed in the fixture manifest. No live credentials, agents, enrollment or
provider calls are part of a fixture.

The existing `ResidentMotion.sample` has four anchors and clamps later indices
to the fourth. Simply duplicating its array cannot qualify a 20/100-agent scene:
#1161 must provide nonoverlapping synthetic layout and the documented visibility
policy. The 100-agent row measures bounded presentation of 100 records, not 100
simultaneously animated residents. Record both total and drawn counts.

## Numeric budgets fixed before qualification

All values in this section are targets, **not measured results**. MiB means
1,048,576 bytes. CPU percent is process CPU time / wall time × 100, with 100%
equal to one fully occupied core. GPU percent is app-attributed GPU busy time /
wall time × 100 for the selected device; do not substitute whole-system GPU
utilization. If attribution or presentation timing is unavailable, report that
metric unverified and do not declare the complete budget passed.

| Metric and sampling definition | 3 agents | 20 agents | 100 agents (≤32 drawn) |
| --- | ---: | ---: | ---: |
| Resident pose + Canvas submission CPU duration, p95 / p99 | ≤4 / 8 ms | ≤8 / 12 ms | ≤12 / 16 ms |
| Foreground decorative cadence, nominal | 12 Hz / 83.33 ms | 12 Hz / 83.33 ms | 12 Hz / 83.33 ms |
| Delivered resident-frame interval, p95 / p99 | ≤100 / 166.67 ms | ≤100 / 166.67 ms | ≤100 / 166.67 ms |
| Intervals >125 ms / maximum unexplained gap | ≤1% / 250 ms | ≤1% / 250 ms | ≤1% / 250 ms |
| Native interaction presentation frame duration, p95 / p99 during selection/scroll | ≤16.67 / 33.33 ms | ≤16.67 / 33.33 ms | ≤16.67 / 33.33 ms |
| Complete frame received → visible projection commit, p95 / p99 | ≤150 / 300 ms | ≤200 / 400 ms | ≤250 / 500 ms |
| Input activation → matching inspector presented, p95 / p99 | ≤100 / 200 ms | ≤100 / 200 ms | ≤100 / 200 ms |
| Whole-process resident memory peak after warmup | ≤200 MiB | ≤256 MiB | ≤384 MiB |
| Whole-process physical footprint peak after warmup | ≤256 MiB | ≤320 MiB | ≤448 MiB |
| Footprint growth after 10 identical project-switch cycles, settled 30 s | ≤8 MiB | ≤8 MiB | ≤8 MiB |
| Visible animated idle CPU / app GPU busy, 120 s mean | ≤15% / 5% | ≤25% / 8% | ≤40% / 12% |
| Visible stationary idle CPU / app GPU busy, 120 s mean | ≤1% / 0.5% | ≤1.5% / 0.5% | ≤2% / 0.5% |
| Suspended scene CPU / app GPU busy, 120 s mean | ≤0.2% / 0.1% | ≤0.2% / 0.1% | ≤0.2% / 0.1% |
| Total decoded scene texture/cache bytes, excluding compositor surfaces | ≤8 MiB | ≤16 MiB | ≤32 MiB |
| Visible resident / decorative particle count | ≤3 / 12 | ≤20 / 40 | ≤32 / 64 |

A 12 Hz decorative interval is intentional pixel animation; it is not a
60-fps animation claim. Canvas submission CPU time is not GPU duration or
presentation latency. Native-control frame deadlines are measured separately
while selecting 30 agents/tasks and scrolling the journal for 30 seconds.
Update latency starts when a complete bounded response is available locally;
report transport/request latency and source observation age separately. Neither
is hidden inside a local rendering score. Source age remains visible even when
rendering is fast.

Texture caps apply to any future original raster atlas or cached drawing:
maximum atlas side 1024 pixels, RGBA8 without mipmaps is 4 MiB per full atlas;
account for every decoded variant, mip level, duplicate and cache. Use nearest
sampling for pixel art. Terrain/residents currently draw original vector/rect
code and have no external bitmap textures. A compositor may allocate backing
surfaces anyway: include them in process footprint where attributed, and report
surface sizes separately rather than claiming zero GPU memory. Cache eviction
must not delete canonical records or change selected IDs.

Common queue caps apply at all three populations:

- At most 1 outstanding transport request and 1 pending decode job. Wire request
  ≤64 KiB, response ≤1 MiB, matching the existing Go transport. Reject oversize
  before decoding; a requested page limit is never silently clamped.
- Retain at most 1 displayed projection and 1 newest staged replacement, each
  ≤1 MiB serialized, plus 1 bounded incoming frame. Track decoded allocation
  separately under the process footprint budget; serialized size is no estimate
  of Swift heap size. No unbounded event, thumbnail, particle or frame queue.
- Commit staged projections at most 10 Hz (100 ms coalescing window), independently
  of the 12 Hz decorative clock. Synthetic input is 2 snapshots/s for 120 s,
  with a 20/s burst during seconds 60–70. Measure latency for applied snapshots
  and report superseded count and oldest pending age; never score discarded
  snapshots as zero-latency successes. Final post-burst snapshot must appear
  within 500 ms. Queue occupancy may never exceed the caps.
- A future delta consumer may stage at most 128 events, ≤1 MiB total and ≤250 ms
  oldest age. Any exceeded cap invalidates its staged delta chain and triggers
  one bounded snapshot resync, not an ever-growing backlog. This is conditional
  design for a future protocol; no delta stream exists in operator v1.

## Projection, coalescing and recovery contract

Go remains the only producer of task lifecycle, attempt, review, acceptance,
usage attribution, authority and provenance. Swift holds a replaceable read
projection plus local selection, view mode, camera, appearance preview and
animation clock. Motion is a pure function of the projected state and visual
time; no animation completion changes task state or implies liveness.

Current [operator v1](../../../contracts/operator/v1/README.md) is a
request/response frame envelope. Its [stdio transport](../../../docs/operator-transport.md)
provides producer-owned activity pagination cursors, not stream revisions or
cross-operation atomic snapshots. A task's sequence is its identity numbering,
not an event ordering counter. Observation timestamps are freshness metadata,
not gap detection or a total ordering guarantee. Do not add client-invented
wire revisions, deltas or orchestration state to compensate.

For snapshot consumers, bind request IDs to a local view generation, exact
workspace/project/task scope and operation. On switch, cancel obsolete work,
increment the presentation generation, and discard late replies from the old
generation. Validate the frame before staging. Replace an entire successful
operation projection atomically; coalesce only complete replacements for the
same generation/scope/operation. Do not splice a new acceptance badge into old
evidence or claim unrelated operation responses share a producer revision.
Separate panels retain their own source/freshness metadata where consistency
cannot be established. Local request order only rejects obsolete requests; it
does not prove producer state advanced.

Partial/truncated pages remain labeled incomplete. An absent record in a
partial page is not a deletion. Follow only supported producer pagination;
unsupported cursors fail closed. On invalid/mismatched frames, overflow, closed
pipe or timeout, discard staged content and show the actual failure/unknown
freshness. Same-project previously valid records may remain only as explicitly
stale/offline content; denied, unsupported and project switch hide them and
clear invalid selection, following the existing shell contract. Resync means
one fresh read through the authorized read-only transport, not sync, retrying a
policy refusal, resetting a canonical cursor or migrating authority. Coalesce
resync requests to 1; no loop of immediate retries. A future live visible poll
policy is capped at 1 request/s and does not run while suspended; the higher
rates above are a synthetic queue stress condition only.

If a later reviewed protocol supplies deltas, require producer-defined scope,
stream identity/epoch, event identity, base revision and next revision before
consumption. Apply only a contiguous validated chain over its matching snapshot.
Coalesce resulting visual states, never skip the validation of intermediate
ordered events. Ignore an exact duplicate; conflicting duplicate, epoch change,
missing base, unknown ordering or a gap discards the pending chain and requests
one fresh snapshot. Never advance over the gap, replay decorative transitions
as business events, synthesize missing acceptance or merge distinct projects.
A dropped event is therefore recoverable presentation loss, not silent state
fabrication. Transport owners define these fields and semantics; this ADR does
not extend the closed v1 schema.

## Visibility, motion and equivalent selection

Within 250 ms of an inactive scene, hidden/minimized or fully occluded window,
Journal-only view, or no visible farm viewport: stop the resident clock,
particles and scene-only work. Issue zero periodic scene polls, pose updates or
GPU draws after that grace period; finite in-flight work is canceled or ignored
by generation. Preserve only bounded last projection/selection. User-driven
native navigation remains available where visible. Source observation age keeps
advancing honestly; suspension is not evidence that an agent stopped.

On return, request one fresh projection when connected, keep retained records
explicitly stale until validated, and draw the current stationary/derived pose.
Do not replay a hidden duration of gait, particles, celebrations or queued
updates. Background receive/poll transport behavior is a separate integration
implementation; the current in-memory shell has none. Existing scenePhase
motion gating is a starting mechanism, not proof of occlusion handling.

Reduce Motion, local Farm animation pause, and inactive visibility all disable
roaming, foot gait, bobbing/hops, particles and animated transitions. State
bubbles, role props, state words/symbols, selection and evidence stay visible.
Use a deterministic stationary pose, without flashing or focus movement. Test
both system Reduce Motion and local pause independently; a local toggle does
not qualify the system preference. Reduced motion must not lower freshness,
remove information or change task/agent/evidence IDs.

Every total-population agent and task, including offscreen residents in the
100-agent fixture, remains reachable through a scrollable native inventory or
journal and its inspector. Selecting by pointer, keyboard or accessibility
uses the same stable identity and record projection. Provide full role/state
words, attribution/unknown usage and freshness labels; never depend on species,
color, motion or world coordinates. Keep tab order stable as animals move.
Unmodified Space/Return opens the focused record; closing details restores a
valid stable focus, with a documented first-valid-record fallback after removal.
Project changes never expose the previous project's details. The decorative
Canvas stays hidden from accessibility to avoid duplicate moving targets.

Qualification must exercise the first, middle and last IDs at each population,
including a culled ID, using keyboard and VoiceOver; compare resulting task,
agent, attempt and evidence identities with pointer selection. Measure selection
latency under stationary/Reduce Motion as well as animation. Historical native
keyboard/AX checks do not constitute a complete VoiceOver audit or these larger
fixture checks.

## Original asset provenance

The farm is original code-authored artwork: `FarmCanvas.swift` / its
`PixelFarmPainter` supplies terrain, crops, buildings and paths;
`FarmResidents.swift` / `PixelSprite` supplies pixel animals, role props, poses
and emotions; `FarmTheme.swift` supplies palette and timber/parchment treatment.
`apps/macos/assets/farm-sprites.svg` is an original geometric starter sheet, not
a commercial-game atlas or a runtime dependency. Species/costumes are cosmetic
and do not carry authority. System symbols/emoji and installed Georgia/system
fonts use platform rendering; they are not custom redistributable asset files.
The committed source and Git history are the reviewable provenance manifest.

No Stardew Valley sprite, tile, logo, screenshot, downloaded font or external
image is incorporated. “Stardew-like” describes the user's cozy farming-world
direction only. Before adding raster assets, record relative source path,
author/generation method, creation revision, license/rights, dimensions, decoded
format/bytes and SHA-256; preserve originals and include them in the caps.
Current capture files document the app; they are not source textures or timing
evidence. See [visual system](visual-system.md) for capture provenance and the
historical interaction limits.

## Reproducible local Mac procedure

These commands exist now; run from the repository root with writable isolated
build caches. They do not provision live access or dispatch CI:

```sh
sw_vers
xcodebuild -version
xcrun swift --version
uname -m
system_profiler SPHardwareDataType SPDisplaysDataType

env CLANG_MODULE_CACHE_PATH=/private/tmp/scene1160-clang SWIFTPM_MODULECACHE_OVERRIDE=/private/tmp/scene1160-swift xcrun swift build -c release --disable-sandbox --package-path apps/macos --scratch-path /private/tmp/scene1160-build
shasum -a 256 /private/tmp/scene1160-build/release/DacliFarmDesign
/private/tmp/scene1160-build/release/DacliFarmDesign
```

Quit previous development instances first and confirm the profiled PID/binary
matches that hash. In the real app choose Fixture → Wide or Compact and verify
outer size; choose Ready and the baseline three-agent farm. Do not use debug
`--render-preview` or `--render-shell-preview` exports for timing. The current
app has no 20/100 fixture flag. Until #1161 adds a reviewed adapter and publishes
its exact commands and manifest hash, mark those runs **not available**, not
passed. No command in this ADR pretends to launch those planned fixtures.

On a Mac permitted to use Instruments:

```sh
xcrun xctrace list templates
xcrun xctrace record --help
xcrun xctrace export --help
```

Use Xcode Product → Profile (Release) or attach Instruments to the exact
optimized PID. The owner separately ran template discovery outside this sandbox on the same
host. Installed relevant templates include SwiftUI, Time Profiler, Game
Performance, Game Performance Overview, Metal System Trace, System Trace and
Power Profiler. This proves availability only, not a successful recording.
Use SwiftUI / Time Profiler for view/update behavior and Game Performance for
interaction/presentation analysis where supported; configure available memory
instruments in Instruments for footprint, and Metal System Trace with process
attribution for GPU work. Apple describes the distinction between
SwiftUI updates, CPU samples and hitches in
[Optimize SwiftUI performance with Instruments](https://developer.apple.com/videos/play/wwdc2025/306/).
Use supported templates on the measured host; record their exact names/options,
export raw trace/table data and retain the trace locally. Do not infer GPU busy
or actual frame presentation from CPU samples or a SpriteKit FPS overlay.

#1161 must add bounded measurement markers (or equivalent profiler events) for
complete-frame receipt, validated projection commit, resident CPU update and
input-to-present. Actual presentation/cadence requires a supported presentation
trace correlated to resident updates; a TimelineView callback count proves only
scheduling. If the host cannot expose a metric, report it unverified. The
current source has no such timing harness; this design task does not fabricate
percentiles from test durations.

Run four phases independently for each fixture/window: animated idle,
stationary idle (local pause then system Reduce Motion), native selection and
journal scrolling, and hidden/minimized/occluded suspension plus resume. The
visibility phase includes 30 seconds visible, 120 seconds suspended, then 30
seconds resumed; verify the 250 ms stop and absence of catch-up. Also switch
projects ten times and settle 30 seconds for retention growth. Freeze input,
fixture manifest, rate/burst schedule and renderer before collecting results.
If comparing SpriteView later, alternate renderer run order and preserve all
other conditions, including 12 Hz requested cadence and the measured actual rate.

For every run save: environment, commit/tree/binary hashes, fixture manifest,
actual window/content/pixel bounds, active/drawn counts, render/motion/visibility
conditions, warmup and interval boundaries, raw trace and export hashes, sample
counts, p95/p99/max values, peak memory/footprint, CPU/GPU means, texture ledger,
queue peaks/drop/resync counts, source-age/transport latency, and each budget's
pass/fail/unverified state. Record profiler overhead and separate uninstrumented
CPU/memory observations; never subtract guessed overhead. Missing fields or
missed visibility/keyboard/VoiceOver checks make that qualification incomplete.

## Evidence state for this decision

The existing scene's 12 Hz minimum schedule, separated native controls,
four-anchor limit, synthetic-only shell, and closed snapshot envelope were
inspected in source at `f1068df1254c00885a88a8c19e0dbf2181398a66`. Host version
commands succeeded on 2026-10-26. `xcrun xctrace list templates` in this worker's
sandbox exited 134 because Instruments could not write its cache under
`~/Library/Caches/com.apple.dt.InstrumentsCLI`; no trace was recorded. The owner
subsequently confirmed template discovery outside the sandbox in
`/private/tmp/dacli-wave4-20261026/xctrace-availability.log`, without recording.
Run the procedure in an authorized local Mac profiling context; this cache failure is
not a renderer result and calls for no authority migration or setup loop here.

No frame/update latency distribution, queue stress, memory peak, GPU attribution
or 3/20/100 performance result is claimed. Earlier four-agent `ps` diagnostics,
content renders and actual-window captures remain explicitly limited in the
[visual system](visual-system.md) and [shell validation](native-shell-validation.md).
Synthetic local fixtures cannot establish production capacity, cloud agent
capacity, provider cost, throughput, live usage, enrollment or live authority.
CI remains manual-only; no CI, release, deployment or publication is performed
by this ADR.

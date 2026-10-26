# CLI and MCP schema compatibility policy

## Installed binary and agent guidance

`dacli capabilities --json` is the deterministic compatibility contract for
the installed binary. It is generated from the same aggregate CLI command
table and MCP tool registry used for dispatch, and includes command paths,
flags parsed from their authoritative usage contracts, JSON support, mutation
classification, state/prompt/MCP schema versions, and a separate runtime
adapter-capability vocabulary. Stable identifiers such as
`cli.command.task.check.flag.verify` let skills require a precise surface
without guessing from release numbers.

An installed skill carries `capabilities.json`. Diagnose it with:

```bash
dacli version --compatibility
dacli version --compatibility /path/to/capabilities.json --json
```

Discovery checks `DACLI_SKILL_REQUIREMENTS`, repository-local Codex/agent skill
locations, then user Codex/agent skill locations. Output identifies only the
selected requirement document and executing binary; it does not enumerate
other files. Missing optional capabilities include the skill-authored
fallback. Missing required capabilities or incompatible required schemas
refuse with exit 3, so an automation must not retry unchanged. The check never
downloads or replaces either artifact and runtime capability declarations do
not claim that a provider is authenticated.

This is the promise to anyone building against `dacli` from the outside: a
script parsing `--json`, an agent branching on an exit code, an MCP client
that caches tool schemas across a session. It says what you may depend on,
what you may not, and — because the promise is worthless if nobody checks it
— which test breaks when the promise is broken.

`format: 0` in `config.yml` (see [FORMAT.md](FORMAT.md)) governs the on-disk
object files. This document governs the *interface* on top of them: exit
codes, command paths, `--json` shapes, and the MCP tool surface.

---

## Native migration and removed launchers

This source checkout intentionally removes coding-agent launch, runtime adapters,
supervision and provider-driven orchestration commands. Their absence is a product
boundary, not an optional fallback. Historical records remain readable. The native
plugin requires explicit scoped enrollment and has no generic CLI escape; older
v0.3.1 archives do not implement that preview. Inspect the live capability manifest
rather than assuming an old command remains available.

## 1. What is stable

### Exit codes

Fixed and enumerated in [ARCHITECTURE.md § 4](ARCHITECTURE.md):

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Operational failure — the thing exists, the operation failed, retrying may help |
| 2 | Usage error — unknown command/flag/value, the caller's bug |
| 3 | Refused by policy — an answer, never retried |
| 4 | Not found |
| 5 | Conflict |

A command's exit code for a given class of input does not change without a
migration note below. `internal/clikit.ExitCode` is the single place that
maps an error to a code; `internal/clikit/clikit_test.go` pins that the
mapping survives error wrapping.

### Command paths

Once a command path (e.g. `task list`, `context`) ships, it is not renamed
or removed without a migration note. The command table in
`internal/cli/cli.go` (aggregated from every feature slice, per
[ARCHITECTURE.md § 2b](ARCHITECTURE.md)) is the single source of truth —
`dacli catalog` and `--help` are both generated from it, never hand-maintained
copies that can drift.

Unknown or incomplete paths use that same live command graph for bounded,
deterministic suggestions. A known family prints its own registered leaves
before the broader catalog; `task <ref>` points to the read-only `task show`
shape; and ambiguous `show <ref>` intent lists both `project show` and `task
show` without running either. Machine callers receive `suggestions` and
`next_actions` arrays in the standard JSON error envelope. Suggestions are
remediation only: dacli never auto-executes one, and ambiguous read-only intent
never offers a mutating invocation.

### JSON shapes

A command's `--json` output is stable **only if** it sets `Command.JSON` and
is enumerated in `internal/cli/json_invariant_test.go`'s
`jsonHonoringCommands`. Every other command either refuses `--json` outright
(exit 2, "does not support --json") or, for `init`/`new`, merely suppresses
human decoration — those two are not structured documents and carry no shape
promise beyond "still human-readable text."

The authoritative set is generated, not copied into this prose: inspect
`dacli capabilities --json`, whose command entries carry `json: true`, and the
matching `jsonHonoringCommands` invariant. Core agent read paths include these
explicit schema identities:

`overview` uses `workspace-overview/v1`: aggregate project/task/status and WIP
counts, pending-event and live-agent counts, plus at most three ready tasks.
`github pull --dry-run` uses `github-adoption-plan/v1`, with full-plan counts
and bounded categorized pages. `github sync --dry-run` wraps that plan and a
bounded, digested push preview in `github-sync-plan/v1`. Adoption acceptance
text is present only with `--include-acceptance`; applying pull/sync does not
have a JSON form.

`accept` uses `acceptance-result/v1` for both one-task and `--all` closes. Its
top-level totals and every task entry report `newly_checked`, `satisfied`, and
`total` separately. A task closed through the explicit no-criteria override
sets `unverified: true`; batch output also counts `unverified_tasks`. Therefore
zero newly checked means “already satisfied” when `total` is nonzero, never
“nothing was verified.”

| Command | Schema |
|---|---|
| `whoami --json` | `identity/v1` |
| `task show --json` | `task/v1` |
| `status --json` | `workspace-status/v1` |
| `next --json` | `next/v1` |
| `critical-path --json` | `critical-path/v1` |
| `doctor --json` | `doctor/v1` |
| `github doctor --json` | `github-doctor/v1` |

Other advertised JSON commands—context, task lists, capabilities, agents,
explain, cleanup, reconciliation, PR diagnosis/wait, operating profiles,
release trains, and delivery progress—are governed by the same registry and
fixture rule. Adding a new emitter requires a real valid-JSON driver in
`internal/cli/json_invariant_test.go`; silently accepting and ignoring
`--json` is structurally refused.

**Additive only.** A future field may be added to a versioned shape. An
existing field is never renamed, retyped, or removed without a schema bump and
migration note. A consumer must ignore fields it does not recognize rather
than reject the document. Nullable metrics stay `null` when no sample was
captured; unknown observations are never fabricated as zero or success.

### MCP tool surface

[MCP.md § 2](MCP.md) tiers the tool catalog, but both tiers inherit the
promise above rather than making a separate one:

- The twenty-two Tier-1 tools (`get_context`, `list_tasks`, `claim_task`,
  `check_task`, …) are a manually maintained table in
  `internal/mcp/tools.go`. Their names and parameter shapes are part of this
  compatibility promise and are checked against the documented catalog.
- The Tier-2 `cli` escape hatch returns exactly the `--json` document of
  whatever `argv` it was given — its stability is the "JSON shapes" rule
  above, transitively, for whichever command name the caller passed.
- Exit-code-to-MCP mapping (0 → result, 1/2/4 → `isError: true`, 3 →
  normal result carrying `{"refused": {...}}`) is fixed, per MCP.md § 3.

---

## 2. What may change without notice

- **Human (non-JSON) text output** — column widths, wording, line ordering.
  [ARCHITECTURE.md § 4](ARCHITECTURE.md): "carries no stability promise at
  all — anything parsing it has already lost."
- **Usage/help text and refusal message wording** — the exit code is
  covered above; the sentence describing it is not. (`withUsage` appending a
  `usage: ...` synopsis to an exit-2 message, landed 2026-08-10, is exactly
  this kind of change and needed no migration note.)
- **Flag-parsing edge cases beyond the documented forms** — `--key value`,
  `--key=value`, a boolean `--flag`, and the `--key -- value` escape are the
  only forms this policy covers; anything else (e.g. how a bare `--` is
  tokenized) is an implementation detail, fuzzed for crashes but not frozen
  for shape.
- **Internal Go APIs** — anything outside `cmd/` and the command table.
  [FORMAT.md](FORMAT.md): "The Go API is unstable; this is not [the on-disk
  format]."
- **Command paths and JSON shapes not enumerated above** — unlisted means
  not yet promised, not "safe to assume stable." A command gains a
  compatibility promise only when it is added to `jsonHonoringCommands` (for
  `--json`) or documented here (for command paths generally).

---

## 3. How the promise is enforced

Documentation alone lets a shape drift silently while the sentence describing
it stays put. Each numbered guarantee above has a test that fails if the
guarantee is broken:

| Guarantee | Enforced by |
|---|---|
| Exit code survives error wrapping | `internal/clikit/clikit_test.go`: `TestExitCodeSurvivesWrapping` |
| `--json` is honored or refused, never silently dropped | `internal/cli/json_invariant_test.go`: `TestJSONFlagIsHonoredOrRefused` |
| The JSON-honoring commands actually emit/adapt | `internal/cli/json_invariant_test.go`: `TestJSONHonoringCommandsEmitOrAdapt` |
| `context --json` / `task list --json` / `metrics --json` match the documented field names and types | `internal/cli/compat_json_shape_test.go`: `TestDocumentedJSONShapesStillParse` |

`TestDocumentedJSONShapesStillParse` decodes both responses field-by-field
against the shapes in § 1 and fails on a missing field or a type mismatch; it
does not fail on an unrecognized *extra* field, matching the additive-only
rule.

---

## 4. Migration notes

Dated by the day the change landed. Each entry names what changed, why, and
what a caller must do differently. Entries are never deleted — a caller
reading old integration notes needs the history, not just the current state.

### 2026-08-10 — unknown/typo'd flags now refuse instead of running with defaults

**Before:** `dacli task add "x" --projct p` (typo) silently dropped the
unrecognized flag and ran as if `--project` had never been given, exit 0.
**After:** the same call refuses with exit 2, naming the flag:
`unknown flag(s): --projct`. Landed across 51 command handlers via a single
dispatcher-level gate (`Flags.Reject`), rather than per-handler, because a
rule applied by convention at ~100 call sites had already drifted once
before.
**Caller action:** if a script depended on an unrecognized flag being
ignored, that call now exits 2 instead of 0 — treat any newly-appearing exit
2 in existing automation as a real typo to fix, not a regression to work
around.

### 2026-08-10 — `task list --status <value>` refuses an unrecognized status instead of returning an empty list

**Before:** `dacli task list --status closed` (meaning `done`) matched no
status folder, printed nothing, and exited 0 — indistinguishable from an
empty backlog. Applied to both the text and `--json` paths.
**After:** an unrecognized `--status` value refuses with exit 2, naming the
bad value and the allowed set (`model.AllStatuses`): open, active, blocked,
done. Omitting `--status` is unchanged — it still means "every status."
**Caller action:** a caller filtering on `--status` should already be using
one of the four canonical values; if it exits 2 now, the value was already
wrong and previously read as a false empty result.

### 2026-08-10 — entity-layer policy refusals exit 3, not 1

**Before:** a refusal originating below the CLI dispatcher — e.g. `dacli
note add decision "..." --project p` without `--rejected` — exited 1
(operational failure, "retrying may help"), because `internal/store` cannot
import `internal/clikit` to raise a proper exit-3 error.
**After:** `store.Refusedf`/`store.ErrRefused` gives that layer a way to mark
a refusal without importing the dispatcher package, and `clikit.ExitCode`
maps it to 3 next to the other refusal-marker types. The message text is
unchanged; only the exit code moved from 1 to 3.
**Caller action:** any supervisor loop that retried exit 1 from `note add
decision` (or another entity-layer refusal) was retrying a condition that
retrying could never fix. It now correctly reads as exit 3 and should
escalate or ask instead — no code change needed if the loop already
respects the 1-vs-3 distinction in [ARCHITECTURE.md § 4](ARCHITECTURE.md);
change needed if it was treating "not 0" as uniformly retryable.

### 2026-08-11 — flag parser: a bare `--` is strictly positional again

**Before:** a stray `--` outside the documented `--key -- value` escape
parsed as a flag with an empty name and could swallow the next token (e.g.
`dacli task check 001 -- --dry-run` silently dropped `--dry-run`); the escape
form could also set a boolean flag to a value, bypassing the check that
boolean flags take no value.
**After:** POSIX semantics — everything after a bare `--` is positional,
full stop. A boolean flag can no longer be given a value through the escape.
Found by `FuzzParseFlags` (6 seconds to first failure); both cases are now
permanent regression-corpus entries under `internal/clikit/testdata/fuzz`.
**Caller action:** only relevant to callers relying on the previous
mis-parse (none should have been, since it silently dropped arguments); the
documented escape forms in § 1/§ 2 above are unaffected.


## Explicit host-capacity schema 21

Source supports `OpenCurrent` for native schemas6–21. The bounded legacy `Open` reader remains capped19; `OpenNativeRoleRegistry` remains exact20. `OpenHostCapacity` is exact21. Only the authenticated owner command `dacli native capacity upgrade --config <private-owner-file> --json` performs20→21. It refuses unfamiliar source fields and preserves all old populated semantic collections and nested role provenance. Already-open20 handles refuse21; opening never migrates. Schema21 writers reject unknown top-level/nested fields rather than discarding them.

Observer, policy, qualification and report v1 contracts carry the exact owner-selected executor instance and capability binding. Owner CLI request files require explicit contract version1. Capacity observations, host declarations, policy and qualification are separate facts, not automatic dispatch or provider quota. The currently observed live workspace stays schema10; no live upgrade is part of #1228. Admission/queue/driver delivery remains #1229/#1230/#1218.

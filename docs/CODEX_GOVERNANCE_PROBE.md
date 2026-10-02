# Native Codex governance probe — 0.159.2

Measured 2026-10-02 on macOS, using the actual bundled `codex-cli 0.159.2`.
The executable path, reproducible invocation, temporary installation procedure
and sanitized measurements are in [the probe fixtures](../plugins/codex/probes/README.md)
and [the observation receipt](../plugins/codex/probes/observed-0.159.2.json).
This delivery establishes local startup observations. Authenticated integration,
native subagent attribution and tool enforcement remain **unverified** and do
not satisfy parent integration acceptance.

## Current official contract

The official [Hooks documentation](https://learn.chatgpt.com/docs/hooks), fetched
2026-10-02, says subagent hooks use the parent session ID. It describes config-layer
hooks, hook trust, timeout configuration and event-specific behavior. That is a
documented limit, not a native subagent measurement from this run.

Official [plugin packaging documentation](https://developers.openai.com/plugins/build/plugins)
allows root `plugin.json`, default `hooks/hooks.json`, and local marketplace
installation. Plugin discovery, enablement and hook trust are separate concerns.
Our actual plugin install used this format in a temporary repository and home.
Official [subagent documentation](https://learn.chatgpt.com/docs/agent-configuration/subagents)
is background for future delegation probes; availability of a feature flag
is not evidence that delegation ran or that it authenticates a worker.

## Measured scope

The host reported `hooks`, `plugins` and `multi_agent` as stable/enabled in its
feature listing. `use_agent_identity` and `executed_tool_call_metadata` were
under development/disabled. These are capability declarations only.

Each case runs a new `codex -a never exec --json --ephemeral` with a constant
public prompt, fresh temporary working directory and no imported credentials.
The runner compares native `thread.started.thread_id` to native hook stdin's
`session_id`, in memory. It saves only equality, distinct counts, field names,
event names and bounded execution diagnostics. It never opens transcript files.
The fixtures generate no fake native events or authenticated subjects.

| Experiment | Actual observation | Limit |
| --- | --- | --- |
| Two separate local invocations | Different native thread IDs; SessionStart's session ID matched each thread | Both unauthenticated; no separate authenticated reviewer demonstrated |
| `--disable hooks` | No hook events; turn.started emitted | Does not prove every disabled-hook surface |
| No hook-trust bypass | No hook events; turn.started emitted | Persisted trust review not exercised |
| Malformed hook stdout (`{broken`) | SessionStart executed; turn.started emitted | Startup tolerance is not a tool authorization decision |
| Sleeping startup hook | See timeout receipt: entered handler, completion marker absent after eight seconds, turn.started emitted | No PreToolUse timeout behavior measured |
| Invalid hooks.json (`{broken`) | No hook events; turn.started emitted | Failure diagnostic observed; no fail-closed startup refusal observed |
| Real local plugin install | Both marketplace add and plugin add exited 0; plugin SessionStart ID matched thread | No MCP OAuth identity or remote plugin measured |
| Plugin false CLI override | Hook still ran with `-c 'plugins."governance-probe@probe-local".enabled=false'` | Cause unresolved; do not trust this invocation as a disable gate |

All inference attempts encountered DNS lookup failure and were killed at the
runner's eight-second bound. Exit `-9` belongs to the runner termination, not
the hook timeout. A `turn.started` event establishes startup progression; it
does not establish successful inference or a performed action. Absence of
PreToolUse, PostToolUse, SubagentStart/Stop, Stop or SessionEnd is **unavailable
coverage**, not proof those hooks are unsupported. Forced termination cannot
measure graceful SessionEnd or cancellation cleanup.

## Beta boundary and concrete blockers

No trustworthy authenticated beta is certified by this run. The measured
surface supports a candidate **session-level cooperative integration** only:
attach an attempt to the entire host session, keeping any child activity in
that subject. Session IDs are correlation identifiers. Caller-supplied agent
IDs, plugin names, role labels and parsed transcripts cannot authenticate a
principal or confer authority.

To qualify a session-level beta, an authority outside the editable agent
workspace must authenticate the session and bind it to an installation,
principal, task, immutable revision and credential reference. It must record
producer/reviewer and deciding sessions independently of the subject attempt.
A native child or a fresh thread under the implementer's authority cannot
satisfy independent-review policy merely by having another ID. An independently
authenticated reviewer with policy-approved provenance must review the exact
subject revision; unresolved independence must refuse acceptance.

Remaining integration evidence required:

1. Two separately authenticated sessions, with verified credential provenance
   outside hook payloads; proof that review policy rejects same-authority or
   replayed evidence.
2. Actual native delegation and parent/child tool calls covering shell, patch,
   MCP and any other claimed action surfaces; direct structured attribution,
   without private transcript parsing.
3. PreToolUse deny, disabled/untrusted/malformed/timeout/error cases measured
   against an observable attempted mutation. SessionStart tolerance cannot be
   promoted into guarded or authority-enforced capability.
4. An authority-owned gate binding action, target/artifact, policy revision,
   authorization and authority epoch. Revocation must fence future writes;
   test cancellation separately from fencing, including already dispatched
   actions. Hooks alone do not establish deployment authorization or retract
   in-flight side effects.

Unavailable network/authentication in this execution prevents these probes.
The runnable startup matrix deliberately does not import the spawning agent's
credentials to make an unavailable integration appear verified. A future
credentialed harness must be separately designed with bounded public prompts,
authority-owned authentication and sanitized outputs.

## Verification and delivery state

Runner tests passed. Mutation: replacing the version comparison with `if False`
made `test_version_mismatch_refuses_before_cases` fail with
`AssertionError: 0 != 2`; restoring it passed. These tests verify version gating
and process bounds, not host governance.

`gofmt -l .`, `go vet ./...` and `golangci-lint run` passed (lint: 0 issues).
The initial `go test ./...` failed in existing temporary-workspace tests with
`agent token not recognized in this workspace`, plus an execution readiness
timeout. `env -u DACLI_AGENT go test ./...` then passed in full. Removing the
token was limited to the test subprocess; durable dacli operations retained
this run's identity.

Durable reporting is blocked by the claim sandbox: `dacli note add decision`
exited 1 because writing the shared notes temporary file was not permitted.
The prescribed external `blocked.txt` and structured root-handoff writes were also
rejected by the sandbox. `dacli commit` exited 1: git add could not create
`.git/index.lock` (Operation not permitted). No commit was created. The assigned
branch is `dacli/004-probe-native-codex-identity-and-hook-attribution-with-versioned-fixtures`.
No acceptance boxes or task completion are claimed. The owner must project the
claimed artifacts, independently review the measurements, and record unresolved
integration blockers before accepting any parent contract. No push or PR is
requested for this local delivery.

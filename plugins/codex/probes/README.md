# Native Codex governance startup probe v1

Requires Python 3.9+, POSIX process groups and the actual installed Codex CLI.
Install the host separately using the official Codex installation instructions;
this fixture does not install or upgrade Codex. The measured executable was:
`/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex`.

From the repository root:

```sh
python3 plugins/codex/probes/probe.py \
  --codex /absolute/path/to/codex \
  --expected-version 'codex-cli 0.159.2' > /tmp/probe-result.json
python3 -m unittest discover -s plugins/codex/probes -v
```

A version mismatch exits 2 before any probe case. `--case timeout` runs one
case. The full matrix takes about 72 seconds plus four bounded plugin installation
commands (each at most eight seconds). Each host execution has an eight-second
outer bound; the hook has a two-second host timeout and the timeout handler
sleeps five seconds. The runner kills its process group at the outer bound.
The version query and runner unit-test subprocess have separate limits.

Every case has a fresh temporary `HOME`, `CODEX_HOME`, and working directory.
Only PATH, TMPDIR and SystemRoot are inherited. No credentials, user config,
private transcripts, authentication status or global plugin store are read or
copied. Startup can still attempt network inference; successful inference is
not required or certified. Temporary state is removed on normal runner exit.
Run only against a vetted installed binary. Hook trust is bypassed only for
these generated, reviewed scripts; `untrusted` omits that flag.

The plugin cases generate a root `plugin.json`, `hooks/hooks.json`, and
`.agents/plugins/marketplace.json` in the temporary workspace, then actually run:

```sh
codex plugin marketplace add "$TEMP_WORK" --json
codex plugin add governance-probe@probe-local --json
```

All plugin writes use the temporary home. `plugin-disabled` supplies
`-c 'plugins."governance-probe@probe-local".enabled=false'`; `disabled` instead
supplies `--disable hooks`. These are separate experiments.

`observed-0.159.2.json` is sanitized output from the actual host, not golden
expected behavior. `status: observed` describes local execution only.
`simulated: false` does not imply authenticated integration. `unavailable`
entries are unsatisfied capabilities. The fixture never promotes a supplied
agent ID into identity and never parses a transcript. It compares native hook
input with the native JSON event stream, then discards raw IDs, messages and
paths. The diagnostic failure boolean is a text heuristic over native JSON
errors, not a complete failure audit. Unit tests verify the runner only and
are not evidence of host identity or enforcement.

Owner-executed credentialed action receipts are separate under
`receipts/0.159.2/`, with a hash index and explicit projection-generation limits.
See `docs/CODEX_GOVERNANCE_PROBE.md` for the action matrix. These do not upgrade
the unauthenticated startup receipt or certify native governance identity.

## Current no-model boundary probe (core583)

`native_boundaries.py` uses the installed Codex app-server diagnostic protocol,
an ephemeral `thread/start` and `mcpServer/tool/call`. Its explicit allowlist has
no model turn, shell command, thread resume or native-agent launch method. It
uses the repository's existing `scripts/serve.py` bootstrap with exact private
selection files; the native server reads credentials. The probe never opens
credential files, reads transcripts or imports the user's Codex home.

The owner creates an isolated temporary Git/workspace fixture and enrolls three
separate native sessions with exact task/path/checkout scopes. A private fixture
JSON has exactly these fields:

```json
{
  "schema_version": 1,
  "workspace": "/absolute/temporary/workspace",
  "task_id": "canonical-task-id",
  "selection_a": "/absolute/private/selection-a.json",
  "selection_b": "/absolute/private/selection-b.json",
  "child_selection": "/absolute/private/selection-child.json",
  "expected_sessions": {"a": "64hex", "b": "64hex", "child": "64hex"}
}
```

All fixture and selection files must be private regular owner files, with no
aliases or duplicate JSON keys. Selections contain `schema_version`, `binary`
and `connection_file`; tokens never appear in the fixture or command prompt.
The host stage pins Codex0.159.2 and checks each selected runtime matches the
measured DACLI binary. Run only after the owner approves the disposable fixture:

```sh
python3 plugins/codex/probes/native_boundaries.py --stage host \
  --fixture /absolute/private/fixture.json --codex /absolute/codex \
  --dacli /absolute/dacli --output /absolute/private/host-report.json
```

The host stage records actual native tool text/structured parity, authenticated
session and attempt issuer, base commit/tree, protocol call attribution and
closed lifecycle notification names. A second host uses sessionB; a shared
selection control changes host labels while retaining sessionA. Different host
labels/thread IDs must never make shared credentials appear independent.
Distinct authenticated session IDs establish distinct sessions; they alone do
not prove distinct ancestry roots or independent reviewer eligibility. Existing
native review policy still decides reviewer independence.

The actual native collaboration child is a separate owner-authorized action in
the calling host. Supply only the designated fixture/selection paths to that
child. It invokes `--stage child --fixture ... --output ...` using its own stdio
bootstrap, verifies the exact child session and registers its own attempt.
No child identity is inferred from its host agent name, inherited MCP namespace,
thread ID, hook metadata or a caller tag. The child receipt proves the explicit
owner-manual separate-process channel; automatic per-child MCP provisioning and
cryptographic host-agent binding remain unverified.

The hook matrix configures reviewed observe/deny/disabled/untrusted/malformed/
timeout/error callbacks in separate temporary homes. Inventory (`hooks/list`),
callback entry/completion, SessionStart coverage and PreToolUse coverage are
separate fields. Direct diagnostic RPC may never enter these hooks. Such a path
is **unexercised**, even if its tool succeeds; startup callbacks do not certify
tool authorization. A missing completion marker does not establish a host
cancellation/timeout guarantee. Unsupported tool responses are measured through
the actual proxy without fallback. If no supported no-model route enters the
required callbacks, core583's hook acceptance criterion remains open.

The original `probe.py`, `native_actions.py`, `observed-0.159.2.json` and historical
receipts remain provenance. They attempted or ran coding model turns, and this
current workflow does not run them or promote their results into current native
identity/enforcement guarantees. Existing unit tests validate those historical
runners only. New guard tests run with:

```sh
python3 -m unittest discover -s plugins/codex/probes -p test_native_boundaries.py -v
```

The temporary-workspace gate accepts the host's per-user temporary root and the
resolved system `/tmp` root (macOS `/private/tmp`); the root directories themselves
and repository checkouts elsewhere remain refused. Failures print only a closed
stage/category JSON diagnostic, never raw host errors, input contents or private
paths. A failure category is not successful identity or hook certification.

### Actual current observation

The owner-executed `receipts/0.159.2/native-boundaries-v4.json` records Codex0.159.2,
native tool parity, actual attempt issuer/base attribution, closed app-server
lifecycle signals and a fresh actual collaboration child. Separate host A/B
selections authenticated distinct native sessions. A shared-A selection under a
different host label authenticated the same session. The actual child used its
own separately issued private connection and stdio process; its authenticated
session differed from A/B. This measured the manual provisioning boundary.

All seven diagnostic hook cases completed their native tool calls, and the
unsupported tool was refused. **No hook callback was entered in any case.**
Thus timeout/malformed callback behavior and blocking/nonblocking callback
execution were not exercised by this route. Core583 acceptance criterion3 remains
open. Disabled/untrusted configuration did not stop the direct diagnostic proxy,
which is an observed route distinction, not a hook enforcement guarantee. No
model turn was requested through the diagnostic app-server. The native child was
spawned through the calling host's supported collaboration tool.

The public report contains bounded session/attempt IDs, closed event/call records
and producer receipt digests. It contains no private fixture/profile paths,
tokens or credential hashes. Original startup/action historical receipts and the
failed v1 temporary-root attempt remain separate evidence; this observation does
not replace them or make those historical claims current.

The diagnostic RPC deadline is checked before each queue read, including when
notifications arrive continuously. Closed-enum notification retention is a bounded
set, not an accumulating event log. Closing an owned stdio process also performs
bounded cleanup of its own process group when the parent has already exited.
A POSIX fixture verifies an exited parent/live descendant receives termination;
this is process-group cleanup evidence, not a host containment or all-writers-
stopped guarantee. The storm regression uses a small sentinel so removing the
deadline guard produces a bounded assertion failure rather than hanging tests.

The v4 observation repeats the actual host and child stages with the original
operation keys after the deadline/retention/owned-group fixes. The child's original
session, attempt and receipt remain immutable. The earlier public v2 observation
is preserved as a distinct file. This remains diagnostic/cooperative evidence;
hook criterion3 is still open because no callback was entered.

## Shared native stdio conformance (core573)

`conformance.py` is a host-neutral consumer of explicit, separately owner-issued
private selection files. The Codex and Antigravity `probes/conformance.py`
entrypoints call the same implementation. Neither starts a model, infers identity
from host labels, reads bearer credential files, or modifies raw authority.

An owner first creates an isolated temporary Git/workspace using supported
`dacli init`, project/task commands and `native bootstrap --config <private>`.
Provision distinct worker/competing-worker, runner and reviewer roots using
`native enroll`, `native verifier add` and `native reviewer add`. The latter's
actual worker-session and runner-config bindings supply reviewer independence;
merely different labels or session IDs are insufficient. Use `native revoke` for
revoked-identity cases. Lifecycle/dispatch policies and exact producer requests
come from their supported owner CLI, never handwritten authority JSON. Missing
credentials are a deliberately unavailable credential file referenced by an
otherwise valid private connection, never an invented token. Do not use this
runner against production or shared workspace authority.

Private fixture JSON has exactly `schema_version:1`, `workspace` (temporary Git
workspace), `profiles` (closed role name -> absolute private selection path), and
`cases` (closed case name -> ordered steps). Role/case names are declared in
`conformance-cases.json`. Every step has exactly `profile`, `tool`, `arguments`
(including `schema_version:1`), boolean `restart`, and `expect`. Expectations have
exactly `exit_code`, `fields` (JSON pointer -> exact literal), and `equal_to`
(JSON pointer -> `{step: earlier zero-based index, pointer: exact pointer}`).
Pin actual task/attempt/revision/lease/fence/issuer and original receipt fields,
not only the generic error message. Negative producer cases require a successful
control before refusal; replay cases require exact prior-result references.
A case that cannot establish its setup/provenance must remain unsupported; do
not supply a malformed unrelated request to make a refusal case pass.

```sh
python3 contracts/governance/v1/conformance.py --fixture /absolute/private/fixture.json \
  --bootstrap /absolute/installed/plugin/scripts/serve.py --output /absolute/new-report.json
```

The fixture loader checks private regular owner files, duplicate fields, finite
size and temporary workspace bindings. It does not authenticate profiles itself;
the actual server does. Restart terminates only its owned broker group, starts a
new broker on the same original selection, and uses the original operation keys.
Missing/late replies are failed-or-unknown, never an execution result. No request
is automatically retried. Refusal classes, text/structured parity, exact producer
fields and historical equality are asserted independently. Reports contain only
closed case names, bounded counts/status, and `host_certified:false`; raw replies,
profiles, credentials, errors and authority data are not exported.

CI runs deterministic transport assertions and existing real backend provisioning,
replay, lifecycle and recovery fixtures. These are backend conformance, not a live
cross-host certification. Owner-supplied fixture execution is separate and may
report unsupported cases. The existing actual Codex0.159.2 native-boundaries-v4
receipt remains original host-version evidence; its hook callbacks were not
entered. Disabled/malformed hooks are explicitly live-only unsupported here.
Antigravity native callback/agent attribution remains unverified; its package
entrypoint proves only the same stdio contract after actual execution. A host
receipt must name measured host version and source receipt digest separately;
this runner never promotes a transport/unit fixture into that receipt.

The manually dispatched CI job builds one native binary for the transport and
missing-credential tests, and runs the Antigravity package/install safety tests.
These deterministic checks do not launch a coding model or certify a host.
Existing historical contract fixtures and host receipts remain unchanged.

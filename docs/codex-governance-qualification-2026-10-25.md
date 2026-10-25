# Codex governance qualification — 2026-10-25

## Measured build and evidence

The installed host was **codex-cli 0.164.0-alpha.11.1** (binary SHA-256
`a04752e4d46a4d89e53e6f61c59c5349a638c8ce172ffbf391fd482b4d902a4e`). The
installed DACLI runtime reports **dacli dev (darwin/arm64)**; its binary SHA-256
is `976cd086939f4b884fc89b3fd7aff95824d3e3e8e7a7521c2e74bd452a5cb7ea`, and
its source/runtime revision is `c9a56b47d9faede8135b40d62ad3bd94030c994d`. The
installed governance plugin is `dacli-governance 0.5.0`.
The governance identity and child probe code came from checkout
`5129c6a23ae081071f2938996a3f3a3307be4697`. The actual loopback hook matrix
was run against the exact installed host above; its source digest is
`70026669718da17b1022f91bc0c56bdea60f4440e50c989d46bdfe1b410f63fd`.

The live diagnostic probe and the normal tool-pipeline fixture measure
different paths. In the diagnostic probe, seven direct app-server cases
(`observe`, `deny`, `disabled`, `untrusted`, `malformed`, `timeout`, `error`)
completed their diagnostic actions, but no hook callback entered or completed.
Those results do not qualify hook enforcement on the direct diagnostic route.
The independent host selection authenticated a distinct DACLI session; reusing
selection A kept A as the authority. A changed host/thread label did not create
a new identity. The live authority stayed at version 10 during these probes.

Separately, the owner ran the approved deterministic loopback Responses fixture
through the installed app-server's normal tool pipeline. All eight cases were
qualified, with at most two fixture responses per case, a 15-second host work
limit, an eight-second cleanup limit, and a 23-second total limit. The timeout
case completed its hook timeout at about one second. Results:

The fixture sets `bypass_hook_trust` in `thread/start` only for its generated,
reviewed callbacks; the untrusted control omits it. This temporary configuration
does not change the installed plugin or the user's persisted hook trust.

| Case | Hook observation | Tool marker | Qualification detail |
|---|---|---:|---|
| `observe` | callback entered; completed | created | Normal `PreToolUse` callback |
| `deny` | callback entered; blocked | absent | The guarded tool action did not run |
| `disabled` | no callback | created | Hooks explicitly disabled |
| `untrusted` | no callback | created | Hook trust was not bypassed |
| `malformed` | callback entered; failed | created | Malformed hook response reported failure |
| `timeout` | callback entered; failed | created | Hook timed out; host turn completed |
| `error` | callback entered; failed | created | Hook error reported failure |
| `unsupported-callback` | hook started; callback not entered; failed | created | Healthy fixture MCP server advertised a no-op tool; requested tool was unknown and not advertised |

This qualifies the measured local app-server hook delivery and outcomes for the
listed tool-pipeline cases with synthetic responses. It does not qualify remote
model inference or behavior, process containment, automatic child credentials,
or broader production behavior. A deterministic fixture and a successful
identity or lease call do not provide those guarantees.

The separately spawned Luna child used its own manually provided private profile
and stdio process. Its receipt records successful authenticated tool calls and
exit code 0. It does not establish cryptographic Codex-host binding or automatic
profile delivery. The external wrapper failed to capture the exit code because
zsh's `status` name is read-only after the probe had already written its receipt;
that wrapper limitation is recorded without rerunning or changing the successful
probe result.

## Reproduction

Run from the repository root with the measured installed Codex binary. The
private fixture contains temporary workspace and selected connection paths; do
not publish it or pass credentials on the command line.

```sh
python3 plugins/codex/probes/native_boundaries.py \
  --fixture /private/path/to/native-boundaries-fixture.json \
  --stage host \
  --codex /Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex \
  --dacli /absolute/path/to/dacli \
  --expected-version 'codex-cli 0.164.0-alpha.11.1' \
  --output /private/path/to/native-boundaries-host.json

python3 plugins/codex/probes/native_boundaries.py \
  --fixture /private/path/to/native-boundaries-fixture.json \
  --stage child \
  --codex /Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex \
  --dacli /absolute/path/to/dacli \
  --expected-version 'codex-cli 0.164.0-alpha.11.1' \
  --output /private/path/to/native-boundaries-child.json

python3 plugins/codex/probes/hook_matrix.py \
  --codex /Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex \
  --output /private/path/to/native-hook-matrix.json

python3 -m unittest discover -s plugins/codex/probes -v
```

The hook-matrix fixture uses only deterministic local Responses; it makes no
external inference request and reads no user credentials. The diagnostic probe
uses an explicit owner-selected private fixture and does not request a model
turn. Raw private fixture data and credentials are not committed.

## Receipt digests and limits

The immutable receipts are
[`native-host-diagnostic.json`](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-host-diagnostic.json),
[`native-child.json`](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-child.json),
and
[`native-hook-matrix.json`](../plugins/codex/probes/receipts/0.164.0-alpha.11.1/native-hook-matrix.json).
Their SHA-256 digests are respectively `60869bdc9925bf5ad364dab721c695026886374babbf8a276f18c2a56bd8544c`,
`81e38f0f7a5bee1dc857616e07e207a03c21b60f9014aa3fc0d0f42f1f1f966c`, and
`b812d29270ff448d8ebbf0e64ecffc148b042376286fd2fc570d441ce604c40a`.
Historical `0.159.2` receipts are unchanged. These observations do not enroll a
live Antigravity setup or authorize CI, release, or production deployment.

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

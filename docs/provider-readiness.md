# Provider installation and sign-in readiness

`dacli provider readiness` reports local installation and authentication-status
observations for Codex CLI, Claude Code CLI, and Antigravity CLI. It never
launches an agent session. The JSON result uses `provider-readiness/v1`.

```sh
dacli provider readiness --json
dacli provider readiness --config /absolute/path/provider-selection.json --json
dacli provider readiness --config /absolute/path/provider-selection.json --auth-status --json
```

The optional schema-1 selection file contains executable paths only:

```json
{
  "schema_version": 1,
  "codex_path": "/opt/codex/bin/codex",
  "claude_code_path": "/opt/claude/bin/claude",
  "antigravity_path": "/opt/agy/bin/agy"
}
```

The file must be a bounded regular file. Unknown fields and relative executable
paths are rejected. Never put credentials, provider settings, tokens, account
identifiers, or key material in this file. An explicit selection takes
precedence over `PATH`; it is a local path choice, not proof that the binary is
trusted or qualified. Without an explicit selection, all matching `PATH`
installations are counted. More than one distinct installation is reported as
ambiguous. If none is found, discovery uses only a documented installation
location: Antigravity's `~/.local/bin/agy` on macOS/Linux and
`%LOCALAPPDATA%\agy\bin\agy.exe` on Windows. Codex and Claude Code have no
additional fallback location in this adapter. Their official installation
methods place commands on `PATH`. For bounded discovery, a `PATH` longer than
32 KiB or containing more than 128 distinct entries is refused as
`candidate_limit_exceeded` instead of being partially scanned.

Version probes run only for one selected executable and have a two-second
deadline and 4 KiB output cap. They use only each tool's documented
`--version` command. With `--auth-status`, Codex runs the documented
`codex login status`, and Claude Code runs `claude auth status`. The adapter
uses Codex's documented successful exit outcome as credentials-present and
does not infer logged-out status from an undocumented failure code. For Claude
Code it parses only the documented JSON `authMethod` enum and checks that it
agrees with the documented exit status; all other fields and raw provider
output are discarded. Stderr is discarded for both commands. The report never
includes account names, authentication modes, paths, credentials, or provider
output.
An unavailable, expired, malformed, denied, timed-out, or unsupported result
remains `unknown` unless the provider's documented status command gives a
specific logged-out outcome. Antigravity authentication always remains
`unknown`: its CLI uses OS keyring and browser sign-in, and no documented
read-only authentication-status command is used here.

The report distinguishes local executable presence, version observation, and
sign-in status. A successful status check does not establish an active
subscription, API billing, quota, entitlement, selected user/account,
successful model request, provider availability, adapter qualification, or
execution architecture. Discovery grants no dacli identity, task registration,
lease, dispatch authority, or provider capability. It does not wrap or execute a
coding CLI with a task prompt, start a provider, read credential files,
keyrings, tokens, browser profiles, or cookies, or authorize native task work.

Provider command names are documented aliases (`codex`, `claude`, and `agy`);
the product aliases `codex-cli`, `claude-code`, and `antigravity` normalize to
those stable report identifiers and are never executed as commands. The
Antigravity app/IDE and editor extensions are outside this CLI adapter. An
unsupported or ambiguous adapter is reported as such rather than inferred
from an installed application or a host label.

## Primary provider documentation

- [Codex CLI quickstart and command reference](https://developers.openai.com/codex/cli/)
- [Codex CLI command reference](https://developers.openai.com/codex/cli/reference)
- [Claude Code CLI reference](https://code.claude.com/docs/en/cli-usage)
- [Antigravity CLI getting started](https://antigravity.google/docs/getting-started?tab=cli)
- [Antigravity installation and authentication](https://antigravity.google/docs/install?tab=cli)

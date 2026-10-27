# Codex provider readiness

The optional local operator report is available through `dacli provider
readiness`. It discovers Codex CLI from an explicit executable selection or
`PATH`, reports a bounded `codex --version` result, and leaves authentication
unknown by default. `--auth-status` opts into the documented read-only
`codex login status` probe.

The report records only whether the status command succeeded. It does not expose
authentication mode, account data, credential contents, tokens, or raw process
output. A successful probe does not establish ChatGPT plan entitlement,
subscription status, API billing, provider availability, model access, native
host identity, adapter qualification, or dispatch authority. The command does
not run `codex login`, open a browser, start `codex`, or execute a coding task.

See [provider readiness](../../docs/provider-readiness.md) and the official
[Codex CLI reference](https://developers.openai.com/codex/cli/reference).

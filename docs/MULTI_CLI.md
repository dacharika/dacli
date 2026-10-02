# Native host integration boundaries

The current product uses native coding-host agents and a scoped governance MCP
connection. Codex is the first measured plugin integration. A second host is not
certified merely because it speaks MCP or uses similar tool names.

Follow [the Codex plugin setup](../plugins/codex/README.md) and
[native transport](GOVERNANCE_NATIVE_MCP.md). Configure model choice, delegation,
concurrency, sandbox and budgets in the host. dacli does not select or launch a
provider CLI, fall back to another vendor, or supply a runtime adapter preset.

A future integration must measure protocol negotiation, credentials, tool
availability, revision binding and revocation using that actual host. Host
metadata is correlation, never authenticated identity. Distinct models or
subagents sharing one MCP connection do not become independent reviewers.
Owner-issued review and trusted-runner grants remain separate from host setup.

Historical multi-CLI run records remain readable; their usage and cost fields
are observations rather than billing or current execution authority. See
[RUNTIMES.md](RUNTIMES.md) for migration and [CODEX_GOVERNANCE_PROBE.md](CODEX_GOVERNANCE_PROBE.md)
for the retained version-specific measurements.

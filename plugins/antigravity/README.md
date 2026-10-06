# dacli governance for Antigravity

This adapter packages the shared native MCP backend for Antigravity task,
revision, named-check and independent-review workflows. It includes the exact
Codex `scripts/serve.py` bytes as a transport bootstrap; the governance state
machine remains in dacli. It does not wrap or launch a provider coding CLI.

Official setup references: [MCP configuration](https://www.antigravity.google/docs/mcp)
and [plugin packages](https://www.antigravity.google/docs/plugins). The MCP
configuration uses `mcpServers` with a stdio `command` and `args`. The manifest
uses the documented `name` and `description` fields. The template placeholders
are rendered by this installer, not assumed host environment expansion.

Read-only local application metadata observed during implementation identifies
Antigravity 2.17.0 (`com.google.antigravity`) and Antigravity IDE 2.5.5
(`com.google.antigravity-ide`). Neither application was launched or used to
validate this package. Packaging and config rendering do not prove installation,
tool availability, a task/review workflow or shared-schema conformance.

## Prepare and install explicitly

Have the owner provision a private native connection and separate runner or
reviewer profiles as required. Use a private selection JSON with schema_version
1, absolute `binary` and `connection_file` paths, and at most one optional
`runner_config_file` or `review_config_file`. Protect it with owner-only file
permissions. The adapter reads selection paths, never credential contents.
Antigravity `orchestration_config_file` is refused because no host capability
adapter has been established.

From this repository, generate a standalone package at a new absolute path:

```sh
python3 plugins/antigravity/scripts/package.py --output /absolute/path/dacli-antigravity
```

Preview a configuration merge (no file changes):

```sh
python3 /absolute/path/dacli-antigravity/scripts/install.py \
  --package /absolute/path/dacli-antigravity \
  --selection /absolute/private/antigravity-selection.json \
  --config /absolute/workspace/.agents/mcp_config.json
```

Add `--install` only for an explicit configuration write. The configuration
parent must already exist. Existing unrelated server entries and top-level
settings are preserved; a conflicting `dacli` entry, changed packaged source,
symlink or unknown configuration shape refuses rather than overwrites. Writes
use an atomic file replacement and an exact private backup of existing bytes.
The installer does not start a host, enroll sessions or change host caches.

The official workspace config is `.agents/mcp_config.json`; the global config is
`~/.gemini/config/mcp_config.json`. Documented local plugin locations are
`.agents/plugins/` and `~/.gemini/config/plugins/`. Generate into the intended
plugin directory if using that installation route, then render its MCP config
with the private selection before manually activating the host. Use a separate
server configuration and independent private connection for each principal.

To undo an explicit config installation, stop using the connection and restore
the exact returned backup bytes; if no prior config existed, remove only the
new config after checking no later settings were added. Retain the package while
any active configuration refers to its absolute bootstrap path. No automatic
rollback or authority revocation is implied.

## Assurance differences and pending evidence

| Surface | Adapter contract | Actual host result |
| --- | --- | --- |
| Identity | Owner-enrolled native principal; shared process means shared identity | Unverified on Antigravity |
| Task/revision/lease | Same strict worker catalog and fresh backend authorization | Unverified on Antigravity |
| Named verification/review | Separate runner/reviewer profiles; same required-set schema | Unverified on Antigravity |
| Hooks/containment | No hook installed; native shell/filesystem can bypass cooperative gates | Unsupported |
| Dispatch/model routing | No Antigravity host capability adapter or mid-turn switch | Unsupported |
| Cancellation | No automatic host cancellation or termination guarantee | Unverified |
| Usage/provider availability | No provider usage measurement or availability enforcement | Unsupported |

Worker, runner and reviewer catalogs are the backend's shared catalogs, not
forked host implementations. Retained Codex receipts are Codex evidence only.
Actual versioned Antigravity installation, task/review calls, schema conformance
and observed differences must be recorded before core572 can be accepted.
No test, live installation or host validation is claimed by this source package.

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

Local application metadata identifies Antigravity 2.17.0 (`com.google.antigravity`)
and Antigravity IDE 2.5.5 (`com.google.antigravity-ide`). A 2026-10-23 UI attempt did
not complete opening the isolated qualification workspace. No Antigravity model
turn or authenticated task/review workflow was verified; live setup is deferred.
Packaging and config rendering do not prove installation, tool availability or
host certification. The deterministic package/install and shared backend tests
run through the manually dispatched CI workflow and are recorded separately in
`contracts/governance/v1/qualification-2026-10-23.md` in the repository.

## Prepare and install explicitly

Have the owner provision a private native connection and separate runner or
reviewer profiles as required. Use a private selection JSON with schema_version
1, absolute `binary` and `connection_file` paths, and at most one optional
`runner_config_file` or `review_config_file`. Protect it with owner-only file
permissions. The adapter reads selection paths, never credential contents.
Antigravity `orchestration_config_file` is refused because no host capability
adapter has been established.

From this repository, generate a configured plugin directly at its final
workspace plugin path (replace both paths with your own absolute paths):

```sh
python3 plugins/antigravity/scripts/package.py \
  --output /absolute/workspace/.agents/plugins/dacli-governance \
  --selection /absolute/private/antigravity-selection.json
```

This writes resolved MCP paths into the plugin itself and records their hashes.
It pins the Python interpreter used to build the package, so a GUI launch does
not depend on your shell's PATH. The selection is validated using the shared
bootstrap; credentials are never copied into the plugin. Keep the private
selection and its referenced runtime/profiles available. Restart or refresh the
host's customizations, then call `identity` and confirm the expected principal
before making changes. Package generation does not enroll or authenticate a
session and does not prove host tool execution.

Do not move or copy a configured package: its MCP entry pins its final absolute
location. Generate at the final destination instead. In particular, CLI plugin
installation may stage a copy elsewhere; use the explicit workspace MCP config
route below for that surface unless its staged paths have been validated.

For a distribution template without any private selection binding:

```sh
python3 plugins/antigravity/scripts/package.py --output /absolute/path/dacli-antigravity
```

An unconfigured template still contains placeholders and must not be activated
as a plugin. It can instead supply the bootstrap for an explicitly rendered MCP
configuration. Preview that configuration merge (no file changes):

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

Choose either the configured plugin or the standalone MCP configuration, not
both for the same connection: duplicate registrations share the same principal
and are not independent workers. One selection exposes one principal. Separate
worker, runner and reviewer sessions need separately provisioned host contexts;
adding names or agent personas does not grant independent authority.

The official workspace config is `.agents/mcp_config.json`; the global config is
`~/.gemini/config/mcp_config.json`. Documented local plugin locations are
`.agents/plugins/` and `~/.gemini/config/plugins/`. Generate into the intended
plugin directory with `--selection` if using that installation route. Use a separate
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

## Improvements still needed

The immediate packaging gap was an unresolved `mcp_config.json` inside generated
plugins; `--selection` now binds the actual entry at generation time. Next, record
a real Antigravity identity/task/revision/review workflow with distinct enrolled
principals before calling this host-qualified. A native swarm adapter needs
documented child identity, dispatch, completion and cancellation APIs with actual
versioned receipts. Lifecycle hooks may provide useful reminders or refusals,
but must be evaluated separately from process/filesystem containment. Model
selection and usage reporting must remain advisory until the host exposes and
demonstrates enforceable capabilities. These gaps do not justify adding coding
CLI wrappers or another governance state machine.

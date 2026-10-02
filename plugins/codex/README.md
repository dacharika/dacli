# dacli Codex plugin — local preview 0.1.0

This package adds workflow guidance and the existing credential-bound workspace
MCP tools to a native host. It never wraps or launches Codex. The portable
`plugin.json` / `mcp.json` layout is canonical; `.codex-plugin/plugin.json` and
`.mcp.json` support older Codex clients. Both configurations describe the same
server. No hooks, network service, account upload or public release is included.

## Install and configure

Prerequisites: Python 3, a source-built/current dacli binary, an existing `.dacli`
workspace, and an owner-provisioned **legacy workspace agent token**. This preview
cannot enroll a native governance session. It does not mint or discover tokens.
Never copy a native authority credential into this connection.

From this repository, register the marketplace and install:

```sh
codex plugin marketplace add /absolute/path/to/dacli/plugins
codex plugin add dacli-governance@dacli-local
```

The repository catalogue is `plugins/.agents/plugins/marketplace.json`; its
source resolves relative to the `plugins` marketplace root. Restart/open a new
Codex chat after installation. A CLI install is per local Codex home and is not
an account upload or a web/mobile deployment.

The server deliberately refuses until you provide
`~/.config/dacli/plugin/connection.json` in the OS home visible to the MCP
process. Create its parent directory and this file with three absolute paths
(no secret in this JSON):

```json
{
  "workspace": "/absolute/path/to/workspace",
  "binary": "/absolute/path/to/dacli",
  "credential_file": "/private/owner/path/dacli-agent.token"
}
```

The credential file must be a regular file owned by your OS user with mode
`0600` (or more restrictive), containing only the already provisioned token.
Symlinks, empty tokens, oversized files and public-readable credentials refuse
with exit 3. The server sets identity explicitly before executing only
`dacli mcp serve`; it never adds `--operator`. Tokens stay out of argv, configs,
tool arguments and diagnostics. Protect the connection file too: it selects
which executable and workspace the local plugin can access.

This initial package uses one connection profile per OS home. Use a separate
OS home/context and credential per independently acting session. Installing once does not establish isolation between native subagents
sharing its MCP process. If your host shares the connection, those agents share
one cooperative identity. Do not describe that as separate authentication.

Ask Codex to call `whoami`, then inspect `status`. Requests must carry
`schema_version: 1` (or the version subsequently advertised by `tools/list`).
Tools run with the configured identity's existing permissions and access the
configured workspace, not the plugin cache. Review the installed source before
trusting it. The package's MCP catalogue includes writes and the `cli` escape
hatch; host tool approvals and the credential's grant still matter.

## Assurance, recovery and uninstall

This is a **cooperative/advisory preview**, not the completed native governance
bridge. The Go kernel has native session/attempt/lease primitives, but this
package's MCP server still uses legacy workspace tokens and workspace tools.
There is no revision-bound native acceptance, independently authenticated child
identity, trusted verification runner enrollment or enforced native review flow
here. Those remain dependencies of issue #1078. Hooks are absent; disabled hooks
cannot reduce a guarantee this package never grants. Direct shell/file actions
can bypass cooperative workspace gates. Refuse requests requiring stronger
assurance rather than calling them enforced.

On a startup refusal, correct the explicit connection or ask the owner to
provision/revoke/replace a credential. Do not retry unchanged requests, fall back
to root, or delete `.dacli`. An invalid nonempty token is additionally rejected
by dacli itself before any JSON-RPC request. The POSIX credential-file protections are required. Windows and hosts that
cannot expand package paths remain unverified until their adapters are tested.

Uninstall with `codex plugin remove dacli-governance@dacli-local` and, if desired,
`codex plugin marketplace remove dacli-local`. Revoke the workspace credential
through the owning workflow and remove its private file. Uninstalling the plugin
must preserve the workspace's `.dacli` audit records and repository files.

## Verify and export

```sh
python3 -m unittest discover -s plugins/codex/tests -v
python3 plugins/codex/scripts/package.py /absolute/output/dacli-governance.zip
python3 plugins/codex/scripts/smoke.py --codex /absolute/path/to/codex --dacli /absolute/path/to/dacli
```

The deterministic export contains only explicitly listed package files, including
hidden compatibility manifests; it excludes probes, tests, caches and repository
state. The build writes outside the plugin folder. Existing host observations
are under `probes/`; advertised features alone do not prove runtime enforcement.
The install smoke receipt documents tested host version and separate transport
observations; it does not certify native session enrollment.

Format sources: [OpenAI packaging documentation](https://developers.openai.com/plugins/build/plugins)
and the installed Plugin Creator skill. Local packaging follows its local-plugin
workflow because dacli needs local workspace access.

Verified on Codex Desktop CLI 0.159.2 on macOS: temporary marketplace install,
native app-server tool/skill discovery, and a harmless fixture-only read-only
identity call. No model turn, production credential or native authority session
was used. Missing connection hides tools; empty and invalid tokens exit 3.
See receipts/install-0.159.2.json for the sanitized observations.

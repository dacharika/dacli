# dacli Codex plugin — local preview 0.2.0

Owner-enrolled native session tools connect Codex to canonical tasks, external
Git checkout attempts, fenced leases and revision-bound self-reports. The
portable `plugin.json` / `mcp.json` package and Codex compatibility manifests
select the same native transport. No hooks, model launcher or CLI escape is
included. The marketplace identity remains `dacli-governance@dacli-local`.

## Provision and install

Prerequisites: Python 3, POSIX private-file support, a current dacli binary with
`native bootstrap`, `native enroll`, `native serve`, `native revoke` and `native upgrade`, and an
existing real Git repository with a dacli workspace and canonical task.

The owner creates a private directory outside the plugin, cache and exports.
Protect its parent directories as well. Save an owner-only (`0600`) owner.json:

```json
{
  "schema_version": 1,
  "workspace": "/absolute/workspace",
  "authority": "/private/owner/authority.json",
  "installation_id": "owner-chosen-installation",
  "owner_credential_file": "/private/owner/owner.token"
}
```

For a new authority, initialize once; enroll the session for the actual task:

```sh
dacli native bootstrap --config /private/owner/owner.json
dacli native enroll --config /private/owner/owner.json --task project/001 --connection /private/owner/session.json --credential /private/owner/session.token --path src --expires 1h
codex plugin marketplace add /absolute/path/to/dacli/plugins
codex plugin add dacli-governance@dacli-local
```

Existing authority state must be preserved, not reinitialized to recover access.
The owner credential grants authority and stays outside the plugin configuration.
Enrollment writes a private native profile and token; never put token bytes into
commands, chat, manifests or tool arguments. Native profile details and authority
lifecycle are documented in the repository's `docs/GOVERNANCE_NATIVE_MCP.md`.

Create `~/.config/dacli/plugin/connection.json` in the OS home visible to the MCP
process, also mode `0600`, with exactly these fields:

```json
{
  "schema_version": 1,
  "binary": "/absolute/path/to/dacli",
  "connection_file": "/private/owner/session.json"
}
```

This selection is bounded to 16 KiB and must be an owner-owned regular file;
final-component symlinks, duplicate keys, unknown fields/versions, public modes
and missing/malformed selections refuse. Parent directory aliases are allowed;
protect those directories against replacement. The bootstrap never reads token
bytes: it removes ambient `DACLI_AGENT` and executes only
`dacli native serve --connection <profile>`. The native server validates the
private profile and credentials and reauthenticates requests. Neither bootstrap
nor server falls back to operator authority or the legacy adapter. Selection of
the executable is a trusted owner action, not a binary integrity guarantee.

Restart/open a new Codex chat after installing or changing the connection.
Call `identity`, then `workspace_status`, `task_list` and `task_get`. Follow the
bundled workflow and advertised schemas for `register_attempt`, `acquire_lease`,
`renew_lease`, `release_lease`, `publish_revision` and `submit_evidence`.
The old legacy MCP CLI remains a separate explicit repository interface; this
package never selects it.

## Identity and recovery boundaries

One profile per OS home means one cooperative session per shared MCP process.
Native children sharing it share that identity. Host labels and `_meta` are
correlation data, not separately authenticated child or reviewer credentials.
Use separately enrolled sessions and server processes when independence is
required. Enrollment is owner-only; grants for trusted runners and independent
review, acceptance consumption and deployment authorization are not exposed.
`submit_evidence` always records **self-reported** provenance. It is not trusted
execution evidence. Host filesystem and shell actions remain outside these
cooperative gates; no containment or universal enforcement is claimed.

For an existing schema-5 authority, the owner runs the exact migration once:

```sh
dacli native upgrade --config /private/owner/owner.json
```

The command requires the private owner credential and preserves existing session
IDs, credentials, connection files and authority history. Restart Codex's native
MCP process with the same profile afterward. It migrates only schema 5 to 6;
already-schema-6, malformed, unsupported or forged-owner requests refuse without
reset. Old schema-5 binaries cannot write the upgraded authority. Migration is
local owner work, never a plugin tool or automatic startup fallback.

Refusals require owner investigation; do not retry unchanged or reset authority.
To revoke, use the exact session ID returned by `identity` or enrollment:

```sh
dacli native revoke --config /private/owner/owner.json --session SESSION_ID
```

A live server checks revocation on subsequent requests. Uninstall with
`codex plugin remove dacli-governance@dacli-local` and optionally
`codex plugin marketplace remove dacli-local`. Revoke separately; uninstalling
must preserve workspace records and authority history. No global installation,
account upload, web/mobile deployment or public submission is part of this
preview. Other host versions and Windows are unverified.

## Verify and export

```sh
python3 -m unittest discover -s plugins/codex/tests -v
python3 plugins/codex/scripts/smoke.py --codex /absolute/path/to/codex --dacli /absolute/path/to/dacli --schema5-dacli /absolute/path/to/retained-schema5-dacli
python3 plugins/codex/scripts/package.py /absolute/output/dacli-governance.zip
```

The smoke requires Codex 0.159.2 and uses disposable OS/Codex homes, a public real
Git fixture and genuine owner bootstrap/enrollment. No model turn, production
credential or dacli agent spawn is requested. It exercises installed tool/skill
discovery, native identity and task reads, attempt/lease/revision/self-report,
then live refusal after the owner revokes. It exports only bounded observations,
never tokens, local paths, task contents or raw host responses. The receipt at
`receipts/native-install-0.159.2.json` records the successful native run, including exact live authorization refusal
after owner revocation. The optional `--schema5-dacli` path must point to a genuine
old binary: it creates the old profile, verifies new-server refusal before
migration, runs the explicit owner upgrade, confirms profile/credential retention
and old-writer refusal, then exercises the same credentials through Codex.
The current native receipt includes this migration proof.
Historical `receipts/install-0.159.2.json` describes version 0.1.0 only.

The deterministic allowlisted archive excludes historical probes/receipts,
tests, caches, scripts used only for testing and all private state. Build outside
the package directory. Historical host probes remain separate observations;
feature advertisements do not certify enforcement.

Format source: [OpenAI plugin packaging](https://developers.openai.com/plugins/build/plugins)
and the installed Plugin Creator local update workflow.

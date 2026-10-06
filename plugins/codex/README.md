# dacli Codex plugin — local preview 0.5.0

Owner-enrolled native session tools connect Codex to canonical tasks, external
Git checkout attempts, fenced leases and revision-bound self-reports. An owner
may separately enable named asynchronous verification through private runner
configuration or independent review through a separate reviewer connection. The
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
process, also mode `0600`, with these required fields:

```json
{
  "schema_version": 1,
  "binary": "/absolute/path/to/dacli",
  "connection_file": "/private/owner/session.json"
}
```

To enable verification, explicitly enroll the worker with `--verification` and
provision a separate runner using `native verifier add` as documented in
`docs/GOVERNANCE_NATIVE_EVIDENCE.md`. Add the optional
`"runner_config_file": "/private/owner/runner.json"` selection field. The
bootstrap passes that reference as `native serve --runner-config`; it does not
read runner credentials, mint grants or accept check commands. Without this
field the default eleven-tool catalog is unchanged. Invalid configured runners
refuse startup rather than silently dropping verification or choosing another
authority. Restart the MCP process after changing selection or configuration.

This selection is bounded to 16 KiB and must be an owner-owned regular file;
final-component symlinks, duplicate keys, unknown fields/versions, public modes
and missing/malformed selections refuse. Parent directory aliases are allowed;
protect those directories against replacement. The bootstrap never reads token
bytes: it removes ambient `DACLI_AGENT` and executes only
`dacli native serve --connection <profile>` with the optional owner-selected
runner reference. The native server validates the
private profile and credentials and reauthenticates requests. Neither bootstrap
nor server falls back to operator authority or the legacy adapter. Selection of
the executable is a trusted owner action, not a binary integrity guarantee.

Restart/open a new Codex chat after installing or changing the connection.
Call `identity`, then `workspace_status`, `task_list` and `task_get`. Follow the
bundled workflow and advertised schemas for `register_attempt`, `acquire_lease`,
`renew_lease`, `release_lease`, `publish_revision` and `submit_evidence`.
With the configured runner, discover `request_verification`,
`get_verification_status` and `cancel_verification`. A request identifies an
owner-configured check and an exact fenced revision; it supplies no command,
credential, grant or replacement directory. Preserve its operation identity for
status, cancellation and recovery. Status queries remain responsive during
execution. Cancellation requests do not create successful evidence; pending
durable records require reconciliation if execution cannot commit an outcome.
The old legacy MCP CLI remains a separate explicit repository interface; this
package never selects it.

## Identity and recovery boundaries

For review, the owner provisions a new review-only root session and a private
policy derived from the configured runner's named command/deadline digests:

```sh
dacli native reviewer add --config /private/owner/owner.json --task project/001 --worker-session WORKER_SESSION_ID --runner-config /private/owner/runner.json --policy-revision owner-policy-v1 --policy-out /private/owner/review-policy.json --review-config /private/owner/review.json --connection /private/owner/reviewer-session.json --credential /private/owner/reviewer.token --expires 1h
```

Every output must be a new private file. The command authenticates the owner and
uses the existing task binding; it does not enable a task or acquire a worker
lease. All configured runner checks are required by the generated policy. Use
an owner-configured runner descriptor containing the intended review checks.
Keep the worker lease current while review is being performed.

In a **separate reviewer process**, select the reviewer connection with the
optional `review_config_file` pointing to its private review profile. This
selection advertises five read tools plus `propose_review` and `evaluate_review`.
It excludes attempt, lease, evidence and verification mutations. The bootstrap
forwards the profile path without reading credentials or policy. A selection
containing both `runner_config_file` and `review_config_file` refuses: worker
verification and independent review require separately enrolled identities.

`propose_review` accepts exact attempt/revision/lease/fence, outcome, rationale
and evidence IDs. Reviewer identity, grant and required policy come from private
configuration. `evaluate_review` returns fresh eligibility and a reason. Exact
proposal retries recover historical records; changed policy/checklist/tree,
released leases or revoked authority can make fresh evaluation ineligible.
Both approval and `request-changes` require the configured successful trusted
checks. Neither a proposal nor eligibility accepts, merges or publishes a task.
See [native review](../../docs/GOVERNANCE_NATIVE_REVIEW.md) for the full contract.

One profile per OS home means one cooperative session per shared MCP process.
Native children sharing it share that identity. Host labels and `_meta` are
correlation data, not separately authenticated child or reviewer credentials.
Use separately enrolled sessions and server processes when independence is
required. Enrollment is owner-only; administration of grants, acceptance
consumption and deployment authorization are not plugin tools.
`submit_evidence` always records **self-reported** provenance. It is not trusted
execution evidence. Optional named verification uses the separately enrolled
local runner and retains its distinct provenance; it does not grant acceptance
or deployment authority. Host filesystem and shell actions remain outside these
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
`receipts/native-install-0.159.2.json` preserves the historical 0.4.0 native run, including exact live authorization refusal
after owner revocation. The optional `--schema5-dacli` path must point to a genuine
old binary: it creates the old profile, verifies new-server refusal before
migration, runs the explicit owner upgrade, confirms profile/credential retention
and old-writer refusal, then exercises the same credentials through Codex.
That historical 0.4.0 receipt includes this migration proof.
It also checks a separately provisioned reviewer through the actual Codex client:
trusted evidence, exact replay, fresh policy changes, worker lease release,
request changes, caller-override refusal and live reviewer revocation. No model
turn or stronger independent reasoning guarantee is inferred from those checks.
Historical `receipts/install-0.159.2.json` describes version 0.1.0 only.

The deterministic allowlisted archive includes that unchanged historical 0.4.0
receipt as a historical observation; it excludes other probes/receipts, tests,
caches, scripts used only for testing and all private state. Build outside
the package directory. Historical host probes remain separate observations;
feature advertisements do not certify enforcement.

Format source: [OpenAI plugin packaging](https://developers.openai.com/plugins/build/plugins)
and the installed Plugin Creator local update workflow.

### Native host identity beta boundary

The bounded no-model fixture in `probes/native_boundaries.py` measures actual
Codex diagnostic-client tool calls and separately owner-selected MCP sessions.
All clients sharing a selection share its authenticated native session; host
labels, threads and subagent metadata do not grant separate authority. An actual
native child can use a separately enrolled connection through the explicit
owner-manual separate-process channel. This does not certify automatic child
profile delivery, cryptographic host identity or independent review from thread
IDs. Native reviewer independence remains enforced by separate credentials,
ancestry and current review grants.

Direct app-server MCP calls may bypass model tool-hook paths. Callback inventory
and startup notifications are distinct from observed tool authorization; absent
callback execution is reported unexercised. Hooks are not a governance or
containment guarantee. See `probes/README.md` for the exact fixture schema,
reproduction and remaining acceptance boundary. Historical launching probes and
receipts are preserved; the current probe starts no coding/model CLI turn.

The current Codex0.159.2 observation is
`probes/receipts/0.159.2/native-boundaries-v4.json`. It confirms separate enrolled
host sessions, shared-selection identity reuse and an actual native child using
its own credentialed stdio process. Its direct diagnostic tool calls entered no
hook callbacks in all seven cases. Timeout/malformed callback enforcement remains
unexercised and core583 criterion3 remains open; this beta boundary does not rely
on hooks or relax reviewer independence.

## Native swarms and bounded loops

An owner may select a separate orchestration connection with
`orchestration_config_file` in the private plugin selection. It is mutually
exclusive with runner and reviewer selections. The bootstrap forwards only the
absolute profile path; it never reads child credentials, enrolls agents, upgrades
authority or launches coding CLIs. Default worker, runner and reviewer catalogs
remain unchanged.

The private orchestration profile has version 1 and pins `workspace_id`,
`project_id`, `session_id`, `role` (`controller`, `bridge` or `worker`),
`capability_file` and its raw `capability_sha256`. A controller additionally pins
`policy_revision` and `routes`, using the shared `ControllerTaskRoute` records.
Routes name independently enrolled bridge, worker, runner and reviewer sessions,
existing verification/review grants, check IDs and prompt digests. These are
configuration references, not credentials or new grants. The configured native
policies and actor scopes must already exist. No production migration happens
when a plugin profile opens.

The controller catalog adds `plan_swarm`, `start_loop`, `get_loop_status`,
`step_loop`, `reconcile_loop`, `stop_loop` and `cancel_dispatch`. Every role also
gets `get_dispatch_status`, `get_dispatch_lifecycle` and
`get_orchestration_host_status`. Bridge connections get `correlate_dispatch`,
`observe_dispatch` and `acknowledge_cancellation`; a worker handshake connection
gets `acknowledge_dispatch`. Inspect `tools/list` for strict field names. Start
accepts only an operation key, invocation ID and checkpoint ID. Advances require
an exact invocation, project, expected sequence and durable operation key.
Requests accept no shell commands, private profile paths, credentials, routes,
model selections or grant overrides.

A bounded cycle is: plan, start, step, perform only freshly permitted native host
instructions, correlate the real child, have that child register its own attempt
and acknowledge dispatch using its own independently configured connection,
then reconcile. Worker revision publication, named runner verification and
independent reviewer proposals retain their separate connection workflows. A
step receipt authorizes a host action only when `HostActionAuthorized` is true;
status reads and replayed receipts never authorize another launch. Keep the
original operation identity after a disconnect, inspect status and reconcile;
do not change a key to repeat an uncertain dispatch. Controller limits govern
waves, concurrency, corrections, no progress, retries and elapsed time. The
controller does not perform a coding turn itself.

`stop_loop` halts future invocation effects and retains existing dispatches and
resources. `cancel_dispatch` records a cancellation request and withdraws future
worker authority. Host acknowledgement and host completion remain separate
observations. Missing or stale heartbeat means unknown/disconnected, never
successful cancellation or termination. An independent bridge records actual
host observations and cancellation acknowledgement against the exact binding.
Unknown writers require owner reconciliation; do not release their protection
based on a chat label or missing heartbeat.

The current host channel is owner-attested Codex collaboration with manual
separate-process profile delivery. Automatic host dispatch is unsupported and
automatic termination remains unverified. Other host capability formats refuse
explicitly; this portable MCP interface does not certify their support.
`get_orchestration_host_status` reports this boundary with
`effect_authorized: false`. Local integration remains a separate producer:
`pending_local_integration` is a truthful wait state, not completed end-to-end
loop delivery. Packaging reads/refusals can be reproduced with
`python3 scripts/smoke.py --orchestration-selection /absolute/private-selection.json --orchestration-role controller`;
this probe starts no model turn and makes no successful dispatch claim.

Difficulty-based routing, when configured by the owner, is returned as a shared
swarm/dispatch selection and explanation. It chooses an approved model/role for
a new native child turn; no caller model override or mid-turn switch is exposed.
Harder corrections remain tied to observed producer outcomes and controller
limits. Historical fixed-role records remain historical, and uncertain writers
retain their protection. Host model availability and provider spend enforcement
are separate facts.

Owner model-routing configuration and local integration use their explicit
native command adapters. They are not authority-granting plugin tools. The
integration outcome must bind the actual configured source/target revision and
verification; source checks alone cannot certify a changed landing target.
Unknown effects require inspection and reconciliation under the original
identity, and `pending_local_integration` remains pending until that producer's
actual outcome is available.

## Next-turn routing and required checks

Controller profiles expose read-only `get_dispatch_routing` with `project` and
`dispatch_id`. The latter identifies either the persisted dispatch or its
correction action operation key. The result is the shared selected role/model,
difficulty, explanation, owner policy revision and binding. A correction describes
a future native child turn; it does not change the original action's actor,
lease or running model and does not authorize a launch. Unknown or out-of-scope
records refuse. Bridge and worker profiles cannot call this controller tool.

Use the controller's complete selected required-check set from the owner policy. Each check must retain
its exact check/grant, producer, revision, lease and fence binding; one successful
check cannot stand for the remaining selected required checks. Named runner requests and
status/recovery use their original durable operation identities. Review and
owner integration require the full selected required successful set for that exact
revision. Failed, missing, pending or historical checks are not substitutes.

Version 0.5.0 is the assembled source update. Its combined service checks,
current catalog/schema generation and actual host packaging/reload proof remain
pending in this implementation handoff. The bundled historical 0.4.0 receipt is
unchanged and makes no 0.5.0 installation or verification claim.

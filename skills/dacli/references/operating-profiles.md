# Operating boundaries

The host owns inspect/plan/implement/review cycles. Choose one bounded task or
a set of non-overlapping tasks using canonical readiness; configure
concurrency and spending in the host. There is no dacli `start` profile or
provider-driven service supervisor. Inspect task scope and the selected native
session before attempting any lease.

The controller is a cooperative governance surface for canonical task reads,
attempt registration, scoped leases, revision publication, and self-reported
check evidence. It does not create a coding agent, select a host model, deliver
a prompt, start a new chat, or terminate a host process. The host owner performs
those actions and reports the exact observed result.

Use the selected process's own `tools/list` result as the MCP capability
boundary. The inspected profile exposed `identity`, `workspace_status`,
`task_list`, `task_get`, `task_brief`, `ready_frontier`, `register_attempt`,
`acquire_lease`, `renew_lease`, `release_lease`, `publish_revision`,
`submit_evidence`, and `get_policy_status`; other profiles may differ. A CLI
command, source function, package file, or other installed plugin does not
prove that the current process exposes a capability.

Owner enrollment, the exact selected private profile, workspace binding,
capability schema, and scope determine whether an operation is authorized.
A shared MCP connection shares one enrolled identity. A host label, model
name, profile prose, or ambient environment variable cannot substitute for an
enrollment. A schema upgrade is a separate explicit owner action; reading a
newer source tree or refreshing skill files does not activate a schema.

Native lifecycle cancellation records a governed request and can record a
later acknowledgement. It does not stop a Codex, editor, or other host
process. The host owner must stop or observe the process manually and record
what happened. Automatic prompt delivery and process termination are not part
of this contract.

Keep these observations distinct:

- **Source support:** a command or tool exists in the selected source/build.
- **Profile activation:** the selected connection and explicit authority
  schema permit the operation.
- **Fixture qualification:** deterministic tests exercised the contract using
  fixtures; this says nothing about a live host.
- **Live qualification:** an exact host/process/tool observation is recorded
  for the named runtime, version, and capability digest.
- **Cached connection:** an already-open host connection may retain older tool
  state. Refreshing package files cannot hot-reload it or prove reconnection.

The retained `0.163.0-alpha.11` receipts qualify only the exact historical
observations they name. They are not current-version or current-host evidence.
Do not treat an old receipt, a successful file refresh, or a selected runtime
manifest as proof of a new process or live tool discovery.

After an installation update, select or restart the host process through its
documented UI, then independently observe its identity and `tools/list`
output. No dacli app reconnect API is part of this contract. A cached `-32003`
refusal describes that cached call; it does not establish that newly
provisioned credentials are invalid. Stop on the refusal and use the
documented owner provisioning and process-selection path.

See the [operator playbook](https://github.com/mlnomadpy/dacli/blob/main/docs/OPERATOR_PLAYBOOK.md) and inspect `dacli capabilities --json` for the installed surface.

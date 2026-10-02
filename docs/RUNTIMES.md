# Native hosts and removal of runtime adapters

Coding-agent CLI launchers have been removed from this checkout. There are no
shipped adapter presets, provider fallbacks, runtime doctor, spawn, supervise or
provider-driven start/loop/service commands. The host owns its native agents,
model selection, approvals, execution sandbox, concurrency and spending limits.

## Current integration

Use the [Codex plugin](../plugins/codex/README.md) with an explicit private native
session connection. The plugin starts only `dacli native serve`; it cannot
launch a coding-agent CLI or expose a generic CLI/shell tool. The
[native transport](GOVERNANCE_NATIVE_MCP.md) describes provisioning and revocation.

The kernel governs coding-agent lifecycles through scoped attempts, leases,
revision evidence and owner decisions. The agents do the engineering work in
their native host. dacli is not an arbitrary DAG of jobs or a cron trigger.
The cooperative local boundary does not intercept every host filesystem action.

## Credentials and review

Provider login gives a host access to its model; it does not authenticate a
dacli principal. A role, model name, host session label or UUID is not a credential.
Agents sharing an MCP connection share the enrolled session. Independent review
requires separately eligible credential families and owner-issued grants;
see [native review](GOVERNANCE_NATIVE_REVIEW.md).

## Historical state

Existing `.dacli/runtimes`, run records, usage reports and probe receipts may be
read for historical attribution or recovery. Their presence does not enable
launching or confer native authority. Keep receipt bytes intact. Use
`dacli cleanup --dry-run` and only an exact reviewed safe plan for storage
cleanup; unknown liveness, divergent files or missing evidence remain refusals.

Older released binaries, including v0.3.1, retain the earlier launcher product.
They do not install this source checkout's native preview. Historical experiments
in [the probe report](CODEX_GOVERNANCE_PROBE.md) describe exact observed versions,
not a support claim for every host or current release.

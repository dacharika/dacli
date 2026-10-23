# Antigravity shared governance conformance

## Shared native stdio conformance (core573)

`conformance.py` is a host-neutral consumer of explicit, separately owner-issued
private selection files. The Codex and Antigravity `probes/conformance.py`
entrypoints call the same implementation. Neither starts a model, infers identity
from host labels, reads bearer credential files, or modifies raw authority.

An owner first creates an isolated temporary Git/workspace using supported
`dacli init`, project/task commands and `native bootstrap --config <private>`.
Provision distinct worker/competing-worker, runner and reviewer roots using
`native enroll`, `native verifier add` and `native reviewer add`. The latter's
actual worker-session and runner-config bindings supply reviewer independence;
merely different labels or session IDs are insufficient. Use `native revoke` for
revoked-identity cases. Lifecycle/dispatch policies and exact producer requests
come from their supported owner CLI, never handwritten authority JSON. Missing
credentials are a deliberately unavailable credential file referenced by an
otherwise valid private connection, never an invented token. Do not use this
runner against production or shared workspace authority.

Private fixture JSON has exactly `schema_version:1`, `workspace` (temporary Git
workspace), `profiles` (closed role name -> absolute private selection path), and
`cases` (closed case name -> ordered steps). Role/case names are declared in
`conformance-cases.json`. Every step has exactly `profile`, `tool`, `arguments`
(including `schema_version:1`), boolean `restart`, and `expect`. Expectations have
exactly `exit_code`, `fields` (JSON pointer -> exact literal), and `equal_to`
(JSON pointer -> `{step: earlier zero-based index, pointer: exact pointer}`).
Pin actual task/attempt/revision/lease/fence/issuer and original receipt fields,
not only the generic error message. Negative producer cases require a successful
control before refusal; replay cases require exact prior-result references.
A case that cannot establish its setup/provenance must remain unsupported; do
not supply a malformed unrelated request to make a refusal case pass.

```sh
python3 contracts/governance/v1/conformance.py --fixture /absolute/private/fixture.json \
  --bootstrap /absolute/installed/plugin/scripts/serve.py --output /absolute/new-report.json
```

The fixture loader checks private regular owner files, duplicate fields, finite
size and temporary workspace bindings. It does not authenticate profiles itself;
the actual server does. Restart terminates only its owned broker group, starts a
new broker on the same original selection, and uses the original operation keys.
Missing/late replies are failed-or-unknown, never an execution result. No request
is automatically retried. Refusal classes, text/structured parity, exact producer
fields and historical equality are asserted independently. Reports contain only
closed case names, bounded counts/status, and `host_certified:false`; raw replies,
profiles, credentials, errors and authority data are not exported.

CI runs deterministic transport assertions and existing real backend provisioning,
replay, lifecycle and recovery fixtures. These are backend conformance, not a live
cross-host certification. Owner-supplied fixture execution is separate and may
report unsupported cases. The existing actual Codex0.159.2 native-boundaries-v4
receipt remains original host-version evidence; its hook callbacks were not
entered. Disabled/malformed hooks are explicitly live-only unsupported here.
Antigravity native callback/agent attribution remains unverified; its package
entrypoint proves only the same stdio contract after actual execution. A host
receipt must name measured host version and source receipt digest separately;
this runner never promotes a transport/unit fixture into that receipt.

The manually dispatched CI job builds one native binary for the transport and
missing-credential tests, and runs the Antigravity package/install safety tests.
These deterministic checks do not launch a coding model or certify a host.
Existing historical contract fixtures and host receipts remain unchanged.

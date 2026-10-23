# Governance identity and lifecycle v1

`governance/v1` is an independent, closed JSON contract. The executable wire
model and graph validator live in `internal/governance/contracts`; `Decode`
rejects unknown fields and trailing documents. `Bundle.Validate` resolves all
references within one workspace, requires exactly one authority, and rejects
missing or incompatible versions on every record. IDs are globally unique
within the snapshot, case sensitive, opaque references. Revisions are immutable
artifact identifiers chosen by the authority; credential and delegation fields
are references, never secrets. SHA-256 digests use lowercase hexadecimal.

`testdata/valid.json` includes human, installation, authority, host capability,
host-session, agent-session, attempt, lease, evidence, and each lifecycle
decision. The negative fixtures cover missing workspace, incompatible version,
cross-workspace identity, provenance, and lifecycle predecessor binding.
Run `go test ./internal/governance/contracts` to exercise all fixtures and
additional fence, assurance, delegation and decoder rejection cases.

The validator is structural, not an authenticator. The authenticated-host origin
in the fixture is synthetic: it asserts no native authenticated integration.
Consumers must authenticate referenced credentials, delegation and provenance,
verify artifact digests, and atomically compare authority epoch and current
resource fence against durable state before executing an action. A valid
historical snapshot does not authorize a live write. See
`docs/GOVERNANCE_AUTHORITY.md` for authority and recovery boundaries.

Nothing here changes `contracts/controlplane/v1`: remote inputs remain proposals
and cannot grant authority or advance local lifecycle state by receipt.

Correction fixtures include session-only execution, an independent reviewer,
historical evidence after revocation, per-action enforcement refusal, deployment
target/artifact/authorization/policy/expiry bindings and cancellation chronology.
Enforcement modes (cooperative, guarded, authority-enforced, unsupported) are
independent of assurance. Cancellation support is best-effort or unsupported.
Revocation records invalidate future decisions without deleting history; live
writes require transactional checks outside this structural snapshot validator.

Authorization `action_digest` is required. Its SHA-256 input is the following
ordered string descriptor (not JSON): domain `dacli-governance-action/v1`,
authorization schema, workspace, authorization ID, human ID, action, target ID,
resolved target environment, artifact ID, resolved artifact revision, resolved
artifact digest, authorization revision, policy revision, issuance, expiry.
Encode each string as its UTF-8 byte length in unsigned decimal without leading
zeros, then `:`, then the exact bytes; concatenate without separators or BOM.
Times are normalized to UTC RFC3339Nano (Go's shortest fractional-second form,
with `Z`). Prefix the lowercase hexadecimal SHA-256 result with `sha256:`.
Do not include `action_digest` itself. IDs are compared exactly; no Unicode or
whitespace normalization is performed. `AuthorizationActionDigest` implements
this encoding; the golden test pins its output against the fixture.

The atomic single-use consumption identity is the pair `(workspace,
authorization ID)`, not the digest and not the deployment decision ID. One
snapshot cannot attach that authorization to two distinct deployment decisions.
A live store must reserve/consume that pair transactionally with the deployment
reservation and retain its replay record across retries, restarts and expiry.
Replaying the same decision is a store-level idempotent lookup of the original
result, not a second action. Snapshot validation cannot detect consumption in an
omitted historical record. Changing an authorization's ID and recomputing its
digest does not create human approval: authentication and approval verification
remain mandatory; this unkeyed digest proves only metadata consistency.

## Shared native stdio conformance (core573)

`conformance.py` is a host-neutral consumer of explicit, separately owner-issued
private selection files. Its closed tool-name ceiling follows the native stdio
catalogs, including `propose_review`, `evaluate_review`,
`get_verification_status`, `observe_dispatch`, and `acknowledge_cancellation`;
each request must also appear in that selected profile's actual `tools/list`
catalog. Obsolete names and tools outside the role's catalog are refused. The
Codex and Antigravity `probes/conformance.py` entrypoints call the same
implementation. Neither starts a model, infers identity
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

`python3 -m unittest contracts.governance.v1.test_native_identity -v` runs an
additional real-binary startup boundary check. It creates disposable authority
through owner CLI commands, confirms an authenticated `initialize`/`identity`
control over native stdio, then points an otherwise valid connection at a
deliberately absent credential file. The missing-credential startup must exit 3
and leave the authority bytes unchanged. Set `DACLI_CONFORMANCE_BINARY` to reuse
a binary; otherwise the test builds one temporary binary. It never reads or
copies credential contents.

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

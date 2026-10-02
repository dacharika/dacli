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

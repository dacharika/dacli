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

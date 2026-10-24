# Signed role releases

The role registry binds native governance decisions to an exact, signed version
of the existing `team.Role` model. The native host still runs the agent and
selects the permitted model for its next turn. Registry verification does not
provide filesystem, network, process, or tool containment.

This delivery covers the local release cache, explicit project pins,
publisher verification, native admission, and [explicit local management](ROLE_MANAGEMENT.md).
Hosted organization/team catalogs, inheritance, the complete constraint/scenario
contract, and aggregate outcomes remain tracked by #978 and its later delivery tasks. Do not
describe the complete shared registry as shipped until those tasks are accepted.

## Immutable content and trust

`internal/rolebundle.ReleaseContent` signs the complete exported `team.Role`,
including the instruction body and model profile, together with its release
version and sorted exact policy/skill references. SHA-256 identifies the content;
an Ed25519 signature binds that digest to an owner-enrolled publisher key ID.
Changing instructions, permissions, routing facts, or references changes the
digest and requires a new release version. A cached role/version cannot be
replaced by different signed content.

Owner-managed public verification keys live in
`.dacli/role-registry/publishers.json`. Releases are cached beneath
`.dacli/role-registry/releases/<role>/<version>/<digest>.json`. Imports verify
their signature, content, schema and exact identity before caching. A downloaded
metadata proposal never enrolls a key, pins a role, edits a role file, or starts
an agent.

The separately signed lifecycle ledger is append-only, with increasing
generations and a previous-status digest. Historical statements remain
signature-checked after their freshness window; the selected exact release must
have a current active statement. Publishing another version does not change an
existing pin. An expired statement for another version does not expire a fresh
selected version. Revocation is terminal for the exact release.

Offline use requires the same verified cached release and a still-fresh signed
lifecycle statement. Offline operation cannot discover a remote revocation that
has not arrived. It refuses once the cached freshness window expires. Key
rotation retains historical public verification keys needed by the signed
ledger; it never silently substitutes a new signing identity.

## Explicit pins and admission

Project pins and their change history are stored together in the canonical
project document. Each pin selects a role, release version and digest. Every
owner change increments the project generation, including an explicit rollback
to an older release. Rollback never decreases a generation or changes immutable
release bytes. No request resolves a floating `latest` or version range.

Native admission compares the verified release with the canonical parsed local
role. Local instructions or routing changes therefore require an explicit new
release and pin. Skill references bind the exact installed skill document
version and digest. Policy references bind the current authenticated native
policy revision and a domain-separated digest of its typed content. The initial
policy kinds are `native-swarm`, `native-preflight`, `native-budget`, and
`native-model-routing`; unknown, expired, stopped, or changed required policies
refuse use.

Provenance follows the role through native planning, preflight, routing and
dispatch records. A historical plan, successful cache import, or signed bundle
does not replace current admission or grant a fresh host permit. Projects without
registry pins retain their local role workflow.

## Authority schema migration

Durable signed-role provenance requires native authority schema 20. An owner
with an existing schema-19 authority explicitly runs:

```sh
dacli native role registry upgrade --config /absolute/private/owner.json
```

Older authorities require their existing explicit migrations first. This command
does not create a new owner, reset credentials, replace actor grants, or migrate
an authority automatically during plugin startup. Keep a recoverable owner
snapshot and validate the selected installation before migrating it.

The current runtime uses an exact-schema reader. The legacy bounded reader
continues to reject schema 20, preventing an older binary from rewriting records
while dropping role provenance. Current worker, controller, bridge, runner,
reviewer and owner adapters must be updated together before using a migrated
authority. Qualification uses disposable authorities; the repository's live
authority is not upgraded as part of development or a source merge.

## Verification

Local checks cover full-role digest changes, signature/version/reference refusal,
immutable imports, lifecycle history, exact-version freshness, offline cache use,
pin drift, explicit rollback generations and key rotation. Native checks must
also cover matching-policy success, changed-policy refusal, admission without
optional preflight/routing, the final dispatch boundary, and schema migration
with old-reader refusal. The test mutation and observed failure belong in the
delivery evidence. CI remains manually dispatched.

# Signed role releases

The role registry binds native governance decisions to an exact, signed version
of the existing `team.Role` model. The native host still runs the agent and
selects the permitted model for its next turn. Registry verification does not
provide filesystem, network, process, or tool containment.

This delivery covers the local release cache, explicit project pins,
publisher verification, native admission, and [explicit local management](ROLE_MANAGEMENT.md).
It also includes the signed constraint contract and declarative scenario evaluator
described below, plus the hosted organization/team catalog API and exact pin
inheritance documented here. Aggregate outcomes remain tracked by core670 and
#978; the complete shared registry is not yet delivered.

## Signed constraint contract and scenario fixtures

Schema-1 releases retain their original canonical JSON bytes and
`dacli-role-release/v1` signing domain. Schema-2 releases require a `contract`
member and use the separate `dacli-role-release/v2` signing domain. Lifecycle
statements still use schema 1; release schema 2 does not require another durable
authority migration beyond schema 20. Older runtimes refuse unsupported release
versions, and current cache decoding rejects unknown fields and trailing JSON
rather than silently dropping signed semantics.

The contract mirrors the canonical role's purpose, instructions, grant, scope,
exclusions, shortcuts, runtime/model/profile preferences, skills and escalation.
Mirrored values must agree with `team.Role`; they do not create another source of
role authority. Signed host requirements, MCP/tool requirements, budget mode,
gates and scenarios also participate in the content digest. Owner
`role registry diff` exposes the complete `content.contract` value, including a
schema-1 current value of `null`.

Native admission recognizes the cooperative capabilities
`native-policy-observation` and `native-swarm-admission`. Required gates are
`budget`, `model-routing`, `preflight` and `swarm`, mapped to exact authenticated
native policy references. Budget mode `native-policy` requires the budget policy;
`none` adds no budget requirement. Missing, changed or unavailable required
observations refuse admission. Required sandbox, network, filesystem or process
containment and required MCP/tool enforcement are represented but refused by
this host, which cannot prove them. A signed requirement is not evidence of
enforcement.

Scenarios contain declarative fixture facts: requested, allowed and forbidden
actions; gate and budget state; escalation; and expected outcome. The library
evaluator performs no dispatch or command execution. Results bind the exact
release digest, signed policy references and scenario digest, with
`evaluated_action` and `evaluated_outcome` labels. A matching fixture outcome is
not a live enforcement observation. Unknown or unsupported contract semantics
are refused.

Final dispatch, direct handoff and acknowledgement reopen current signed release
and pin provenance, lifecycle, policy references and capability observations
before authorizing an effect. Qualification replaces a signed release at the
last measured capability callback and requires refusal with no permit or lease;
supported signed-contract controls must succeed at the same boundaries.

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

## Hosted catalogs and inheritance

The composed control-plane runtime requires the explicitly provisioned hosted
PostgreSQL schema 12, including the exact migration checksums and forced RLS on
all six role registry tables. This is separate from native authority schema 20.
Startup never applies either migration. Operators review and apply
`cloud/migrations/0012_role_registry.sql` through the existing migration runner
and ledger procedure before starting this binary; an older hosted schema fails
readiness. Preserve a full database backup and the enrolled publisher keys.
Rolling back the binary requires a compatible database restoration procedure;
immutable catalog rows are not deleted to simulate a downgrade.

Catalogs belong to an organization and optionally a team. Publisher enrollment
is an explicit catalog-scoped mutation. Publishing, pinning and lifecycle
changes reload the authenticated actor's current membership, permissions,
account, organization and device within the tenant transaction. Membership
versions and expiry remain authoritative; client-supplied roles or timestamps
cannot grant permission. Tenant-bearing foreign keys, explicit predicates and
forced PostgreSQL RLS protect the same boundary. Successful mutations append
an immutable actor/device/correlation audit event in the same transaction.

The authenticated `/v1/role-registry` API exposes these bounded operations:

| Method and path suffix | Operation |
| --- | --- |
| `GET /catalogs` | List accessible organization/current-team catalogs |
| `POST /catalogs` | Create an organization or team catalog |
| `POST /catalogs/{catalog}/publishers` | Explicitly enroll a public verification key |
| `POST /catalogs/{catalog}/releases` | Import an exact signed envelope as a draft |
| `POST /releases/{catalog}/{role}/{version}/{digest}/lifecycle` | Append a signed lifecycle transition with the expected role-wide sequence |
| `GET /releases/{catalog}/{role}/{version}/{digest}` | Export exact signed bytes and the complete bounded role history |
| `PUT /pins` | Append an exact organization/team/project pin with the expected scope generation |
| `GET /projects/{project}/roles/{role}` | Resolve the project, current membership team, then organization pin |

The API derives organization, actor and device from the verified identity.
Release import carries `role_id`, exact `version`, `content_digest`, and the
base64-encoded raw JSON `envelope`; publisher enrollment carries `key_id` and a
base64-encoded Ed25519 `public_key`. Mutations require a fresh `X-Request-ID`
correlation. Lifecycle writes carry `expected_sequence`, `state`, signed
`status`, and `reason`. Pin writes carry `target_kind`, `scope_id`, `role_id`,
`catalog_id`, exact `version` and `content_digest`, `expected_generation`, and
`reason`. There is no floating version selector or automatic upgrade.

Released, deprecated and revoked states are append-only metadata; release bytes
and enrolled keys are immutable. Signed lifecycle generations form one chain
per catalog and role across release versions. Revocation remains terminal for
an exact release. Pin changes record the previous exact selection and advance
the scope generation even when selecting another catalog or rolling back.
The highest-priority explicit pin is authoritative: an invalid or revoked
selection refuses resolution instead of silently falling back.

Exports are proposal data, not an execution permit. They preserve the raw
signed envelope bytes and full role-wide lifecycle chain so a local owner can
verify them against separately enrolled keys before explicitly importing,
selecting a local project pin and satisfying native admission. The hosted API
does not enroll local trust, edit local role files, start an agent, or replace
the native host's execution authority. Catalog resolution is a read observation;
consumers must revalidate freshness and current governance before effects.

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

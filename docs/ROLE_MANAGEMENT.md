# Explicit local role management

The `role registry` commands require an explicit private owner `--config`
and exact `--project`. Ambient legacy agent grants never authorize these
operations. They are excluded from the legacy MCP executor and native worker
tool catalog. Registry management is local; it performs no network discovery.

```sh
dacli role registry import --config /private/owner.json --project core --release release.json --lifecycle lifecycle.json --json
dacli role registry verify --config /private/owner.json --project core --role builder --version 1.0.0 --digest sha256:<exact-digest> --json
dacli role registry diff --config /private/owner.json --project core --role builder --version 2.0.0 --digest sha256:<exact-digest> --json
```

Import accepts bounded regular JSON files containing a signed release and its
exact signed lifecycle statement. The publisher must already be enrolled in the
local owner-managed keyring. Import only caches the supplied objects; it never
enrolls keys, selects a pin, rewrites canonical roles, or runs an agent. A valid
revocation import returns a revocation receipt without claiming activation.
Verify and diff are reads. Diff reports signed field changes, current selection
and lifecycle availability, and whether the target matches canonical `team.Role`.

Selection requires authority schema 20. An older authority may import, verify,
and diff; pin changes refuse until the owner explicitly migrates. The separate
`native role registry upgrade` command migrates authority storage. It is distinct
from `role registry upgrade`, which selects a newer signed role release.

```sh
dacli role registry pin --config /private/owner.json --project core --role builder --version 1.0.0 --digest sha256:<exact-digest> --expected-generation 0 --reason "Reviewed initial selection"
dacli role registry upgrade --config /private/owner.json --project core --role builder --version 2.0.0 --digest sha256:<new-digest> --expected-generation 1 --expected-version 1.0.0 --expected-digest sha256:<old-digest> --reason "Reviewed newer release"
dacli role registry rollback --config /private/owner.json --project core --role builder --version 1.0.0 --digest sha256:<old-digest> --expected-generation 2 --expected-version 2.0.0 --expected-digest sha256:<new-digest> --reason "Restore prior reviewed selection"
```

Pin requires no existing selection. Upgrade requires strictly greater SemVer
precedence. Rollback requires a real prior selection in project history; an
arbitrary cached older release is insufficient. All selections compare the
expected project generation and current exact pin under locks, append actor and
reason history, and increase generation. A stale observation refuses without
changing the project.

The selected signed role must already match the canonical parsed local role.
These commands do not materialize changed role content: review and update that
content separately before selecting its matching release. A cache entry, pin, or
management receipt grants no host filesystem, network, process or tool
containment. Incoming synchronization envelopes remain proposals and cannot
activate any of these owner operations. See [signed role releases](ROLE_REGISTRY.md).

## Explicit hosted catalog import

A hosted export is an untrusted transfer until the local owner verifies it
against the separately enrolled local publisher keyring:

```sh
dacli role registry import --config /private/owner.json --project core --catalog-export catalog-export.json --json
```

`--catalog-export` is exclusive with `--release` and `--lifecycle`. The bounded
closed JSON export identifies one exact tenant, catalog, role, version and
digest and carries its release bytes and complete role-wide signed history.
Historical statements are signature-checked at their issue time; the selected
statement must satisfy current freshness. Gaps, altered signatures, mixed
identity and immutable cache conflicts refuse import. Import never derives
local trust from downloaded metadata, selects a pin, changes canonical role
files or starts an agent. Verify, diff and an explicit owner pin/upgrade/rollback
remain separate operations. Hosted inheritance is observed provenance; later
remote pin changes do not automatically change the local project selection.

## Aggregate role adoption and outcomes

```sh
dacli role registry outcomes --config /private/owner.json --project core --json
```

This read requires the explicit private owner profile and an exact project.
It uses durable native acknowledged dispatch and cooperative host observation
records, with exact executed role/version/content digest and a digest of the
signed policy set. It does not infer success from a reservation, verification
result, review proposal, cancellation acknowledgement or missing outcome.
Completed and failed observations are historical cooperative reports, not
provider measurements, proof of containment or canonical task acceptance.
Later pin changes and worker retirement do not rewrite those historical facts.

Each visible cohort requires at least five distinct tasks. Task samples and
unique acknowledged dispatch samples are separate counts, so retries cannot
inflate adoption. Small cohorts are omitted without publishing their identities
or totals. Outcome counts describe dispatch observations; if any nonzero cell
has fewer than five distinct supporting tasks, the entire outcome vector is
withheld to avoid revealing a small complementary cell by subtraction.

The report has a closed role-version grouping and contains no developer,
actor, agent, session, task, run or dispatch identities, raw evidence identifiers
or private paths. Individual filters, ranking dimensions, arbitrary time windows
and lower cohort thresholds are unsupported and refused. These fixed cohort
rules reduce small-sample disclosure; they are not differential privacy and do
not guarantee anonymity against external knowledge of role identifiers.

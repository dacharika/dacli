# Explicit local role management

The six `role registry` commands require an explicit private owner `--config`
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

# Dolt and DoltHub evaluation for dacli — 2026-10-22

Recommendation: **evaluate Dolt for versioned project knowledge and configuration,
but keep the current authority and PostgreSQL decisions unchanged.** Database
branching could improve review of task plans, decisions, roles and workflows.
It does not establish authenticated agent identity, exclusive resource ownership
or single-use permission consumption.

This is a documentation/source assessment following the
[system audit](SYSTEM_AUDIT_2026_10_22.md). No Dolt software was installed, no
database was created, no remote was connected and no live concurrency or
performance qualification was performed. All applicability judgments below are
dacli-specific engineering inferences.

## Distinguish the products

| Product | Documented purpose | Possible dacli role |
| --- | --- | --- |
| Dolt | MySQL-compatible SQL database with data/schema versioning, branches, merges and history | Candidate for a versioned work/knowledge store |
| DoltHub | Database repository sharing, diffs, review and collaboration | Optional remote for deliberately shareable projections |
| Hosted Dolt | Managed running Dolt SQL servers | Additional operational service only if an application adopts Dolt |
| DoltLab | Self-hosted database collaboration service | Alternative collaboration location, with its own operations burden |

These are different deployment and privacy choices. The database engine does
not require publishing data to DoltHub. Sources: [Dolt overview](https://www.dolthub.com/docs/introduction/what-is-dolt/),
[DoltHub/DoltLab](https://www.dolthub.com/docs/concepts/dolthub/),
[Hosted Dolt](https://www.dolthub.com/docs/products/hosted/).

## Where it could help

The current work/knowledge store is human-readable Markdown, while the native
authority is a private locked aggregate. Dolt is a plausible candidate when
querying dependencies, reviewing proposed knowledge changes or comparing
versioned role/workflow configurations becomes difficult enough to justify
another storage dependency.

Dolt stores a commit graph and exposes history/diff/branch operations through
SQL. Its storage engine shares unchanged content between versions. These are
useful primitives for reviewable project-state changes, not evidence of a
smaller dacli installation or faster actual workloads. Sources:
[version control](https://www.dolthub.com/docs/sql-reference/version-control/),
[storage engine](https://www.dolthub.com/docs/architecture/storage-engine/).

The least risky first experiment is a **disposable, non-authoritative projection**
of sanitized tasks, dependency edges, decisions and workflow versions. Read-only
queries and proposed changes can be compared with the existing Markdown source.
A branch merge would produce a proposal for dacli validation, not directly
change a task's accepted state. This reuses the current storage-experiment
boundary rather than creating two live writers.

## What makes live governance different

### Transaction success must prove a single winner

Dolt distinguishes SQL transaction commit from a version-control commit. Its
documentation lists `SELECT FOR UPDATE` as unsupported and says `LOCK TABLES`
does not prevent other sessions accessing a table. Source:
[supported statements](https://www.dolthub.com/docs/sql-reference/sql-support/supported-statements/).

The vendor's concurrency explanation says concurrent changes to different cells,
and even the same cell with the same value, may merge successfully. It also
describes version-control graph operations as serialized. Source:
[Dolt concurrency](https://www.dolthub.com/blog/2026-02-17-dolt-concurrency/).

For dacli, that means an importer or future database adapter must demonstrate
exactly one successful lease acquisition or decision consumption under competing
processes. SQL transaction support and a merge without a data conflict are not
enough. Unknown commit results must still reconcile the original operation key.
Documentation describing isolation is not a substitute for pinned-version
behavioral evidence.

### Project branches cannot fork live authority

A task-description merge may be valid; merging two independently issued exclusive
leases cannot make both writers exclusive. Rolling a data branch back must never
revive a revoked session, reuse an old fence, erase a consumed decision or forget
an external side effect. The safe initial model leaves credentials, lease fences,
consumption tombstones and canonical acceptance receipts outside branchable
project knowledge. A different future authority model requires explicit design
and migration, not a generic table merge.

### Branch permissions are not the current security model

Dolt's branch controls apply to authenticated SQL-server connections; direct
local CLI/file access bypasses those checks. The documented branch permission
model governs modifications and does not make branches a tenant read-isolation
boundary. Source:
[branch permissions](https://www.dolthub.com/docs/sql-reference/server/branch-permissions/).

Therefore neither a branch name nor database commit author replaces dacli actor
authentication, reviewer independence, tenant authorization or protected local
files. A future adapter must specify SQL privilege defaults, branch controls and
who can access the database directory.

## The hosted PostgreSQL service is a separate decision

The existing service uses lib/pq and PostgreSQL-specific RLS policies,
transaction-local tenant context, advisory locks, JSONB and worker
`FOR UPDATE SKIP LOCKED` claims. Relevant source:
[`device_auth.go`](../../cloud/internal/tenantrepo/device_auth.go),
[`jobs.go`](../../cloud/internal/envelopeworker/jobs.go),
[`tenant migrations`](../../cloud/migrations/0007_envelope_worker.sql).

Dolt's documented client/dialect is MySQL-compatible. DoltgreSQL is a separate
Postgres-compatible product; this evaluation establishes no compatibility of our
migrations, RLS or worker/recovery behavior with it. Sources:
[Dolt overview](https://www.dolthub.com/docs/introduction/what-is-dolt/),
[DoltgreSQL](https://www.dolthub.com/docs/products/doltgres/).

Keep PostgreSQL for the current hosted service. A later engine replacement would
need a separate tenant-isolation, migration and recovery qualification plan.

## A bounded comparison under issue #1084

Expand the existing storage decision, rather than creating a second migration
program. Compare three options on the same sanitized fixture:

| Candidate | Main question |
| --- | --- |
| Current Markdown/private aggregate | Can bounded loading and existing indexes satisfy the actual workload? |
| Local SQLite transaction boundary | Is a simpler local database enough if versioned data branching is unnecessary? |
| Pinned Dolt release | Do queryable versions and proposal branches justify its runtime, migration and permission costs? |

Before selecting a writer, record:

1. Exact tool/runtime versions, schema, writer topology and transaction-to-history
   commit behavior; no vague claim that every database write is automatically a
   separate reviewed commit.
2. Import/export identity, count, edge and content-digest parity. Projection
   failure or restart must leave the original workspace/authority unchanged.
3. Competing-process lease and single-use consumption results, same-value writes,
   disjoint edits that jointly violate a domain invariant, stale fences and
   ambiguous commit acknowledgement.
4. Branch/merge/reset and remote replay outcomes. Revocation, monotonic fences,
   consumed decisions and external outcomes must never roll backward.
5. Permission behavior, private-data exclusion, explicit remote disclosure and
   tenant isolation. Never export raw authority, credentials, credential hashes,
   transcripts or source by default; task knowledge can itself be confidential.
6. Workload measurements: ready-frontier/context latency, peak memory, disk/history
   growth, startup, lock contention, backup/restore and supported platforms.
7. Cutover/rollback rules that stop old writers and keep one authority. If the
   simpler model meets the measured needs, retain it.

These are future selection gates, not results claimed by this research.

## Backlog implications

- [#1084](https://github.com/mlnomadpy/dacli/issues/1084): add Dolt explicitly to
  the existing storage evaluation, with projection-first and safety gates.
- [#1091](https://github.com/mlnomadpy/dacli/issues/1091): keep bounded authority
  loading as an immediate implementation slice; do not block it on a database
  experiment or remove replay history to get under a limit.
- [#1068](https://github.com/mlnomadpy/dacli/issues/1068): retain the native
  architecture and delivery critical path. Dolt research does not delay #1088.

The preceding audit found most local bytes in Git worktrees. Switching task
storage would not reclaim those worktrees. Versioned data storage also retains
history, so its disk behavior must be measured rather than assumed beneficial.

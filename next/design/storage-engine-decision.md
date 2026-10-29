# Rust storage candidate and versioned data experiment

SQLite is the first qualified **fixture implementation**, not a final engine
selection or migration of the Go workspace. The kernel remains independent of
storage. `store-contracts` owns the bounded create/replay/read outcomes; one
selected store transaction owns task, immutable version/event, operation receipt
and derived query writes together. No parallel live Dolt task writer is added.

[Beads](https://github.com/gastownhall/beads) demonstrates useful versioned SQL
features: data diffs, branches, merges and remote sync. Those primitives could
support reviewable task plans, dependency changes and configuration history.
They do not establish that dacli's authenticated claims, single-use decisions,
revocations or accepted outcomes remain valid after a database merge or reset.
That distinction is dacli's engineering requirement, not a criticism of Beads.

Dolt deserves a controlled comparison. DoltLite also deserves inclusion because
it embeds versioned storage behind a SQLite interface and offers Rust bindings.
The vendor still labels DoltLite Beta and identifies version-control concurrency
and remote handling as continuing work. Its published performance measurements
are not dacli workload results. Source: [DoltLite status,2026-10-12](https://www.dolthub.com/blog/2026-10-12-doltlite-gets-better/).

[#1268](https://github.com/dacharika/dacli/issues/1268) tracks pinned SQLite,
Dolt and DoltLite trials with the same synthetic transaction/graph fixtures,
competing processes, ambiguous acknowledgements, crash recovery and branch/reset
invariants. Record ready-frontier/query latency, startup, memory, disk/history
growth and operational/package cost at the declared scales. One winner must be
proven for claims and single-use actions; conflict-free data merging is not that
proof. Live authority cannot be forked with a worktree or revived by rollback.

Keep versioned proposal data behind explicit domain validation. Never split
mutable task state and its commit receipt across independent databases or treat
a synchronized branch as authority. A future engine must meet the same atomicity,
identity, replay and failure contracts before selection. Remote sync requires its
own deliberate data disclosure and migration plan.

Current evidence: the SQLite create-only fixture has process-interruption and
transaction tests, recorded in [transaction qualification](transaction-qualification.json).
No Dolt/DoltLite adapter, benchmark, database, remote or engine cutover was run by
this slice. The dated [legacy evaluation](../../docs/research/DOLT_EVALUATION_2026_10_22.md)
remains historical; its prior issue#1084 is closed and is not reopened by this
new Rust experiment. Hosted PostgreSQL and current Go authority stay unchanged.

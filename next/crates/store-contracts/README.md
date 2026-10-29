# Task-store contracts

The first fixture store port from dacli #1257. Types and validation use only
`core` and `alloc`. The port describes one create-only command, exact scoped
identity, immutable receipts, stable-version reads, derived-query maintenance,
reconciliation observations and bounded budget data. It imports no database,
filesystem, process, clock, CLI, UI or authentication implementation.

These are fixture storage primitives. They do not authorize callers, activate a
source authority, select an engine automatically or expose a production write
transport. The command kernel remains unavailable for writes until the separate
scoped gateway is qualified. SQLite owns its concrete connection, schema and
clock handling; another adapter must prove the same semantic contract before it
can be selected deliberately.

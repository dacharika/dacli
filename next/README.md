# Greenfield command kernel

This is the first implementation slice of [#1256](https://github.com/dacharika/dacli/issues/1256),
based on the architecture in [#1255](https://github.com/dacharika/dacli/issues/1255).
It implements static protocol discovery and the common decoder, effect check,
context gate and typed result path. `CreateTask` is registered as unavailable.
It does not write tasks, start agents, execute plugins or replace the Go core.

## Run the slice

Rust 1.83 or later is required. From this `next` directory:

```sh
cargo test -p core-kernel -p cli
cargo run -p cli -- --help
printf '%s\n' '{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"public_protocol"}}' | cargo run -q -p cli
```

The last command returns the actual catalog as JSON. Action names, summaries,
effects, authentication requirements and availability in help and JSON come from
one kernel registration table. There is no separately maintained command list.
`--help` is a transport option, not a second domain dispatcher. Other arguments
are usage errors; credential values are not accepted in argv.

The CLI reads one complete JSON request from standard input with a 64 KiB byte
limit before JSON decoding. Duplicate and unknown fields, trailing documents,
invalid UTF-8 and context fields that do not apply are usage errors. It emits
one JSON result and preserves exit 0 (success), 2 (usage), 3 (final policy refusal)
and 1 (transport failure). This slice has no resource lookup that would produce
exit 4. Refused input is not automatically retried. Error output does not echo
raw input, credential bytes, parser details or unsupported arguments.

An explicit credential context with an empty token demonstrates a refusal:

```json
{"protocol_version":1,"action":"InspectProtocol","effect":"read_only","context":{"kind":"credential","token":""}}
```

Missing, empty and whitespace-only credentials produce `empty_credential`.
Every nonempty credential produces `credential_verification_unavailable`:
there is no credential verifier or current dacli authority reuse here. Do not
submit actual credentials to this prototype. Tests use synthetic canaries only.

## The static-public discovery exception

The eventual authenticated command gateway remains the design for authority
operations. This first slice explicitly permits `public_protocol` only for
static public `InspectProtocol` metadata. It returns no workspace records,
source observations, evidence, identity, policy grants or current authority facts.
This is a deliberate bounded exception to the proposed architecture's blanket
“every route authenticated” wording, not an empty-credential fallback.

Supplying a credential context never falls through to public discovery, even for
this query. Public discovery cannot authorize the registered task mutation; it
returns `public_context_not_allowed`. The catalog still truthfully reports the
task writer as unavailable. No path treats a nonempty string as an authenticated
identity, an owner grant, a parent grant or an authorization decision.

A request's `effect` is untrusted. The kernel rejects an unknown classification
or a mismatch against its registered `ActionSpec` before invoking a handler.
Unregistered actions are final typed refusals. The successful query and refused
mutation share that exact path. An unavailable implementation also has a terminal
refusal arm rather than an empty successful handler.

## Compiler and verification boundaries

`core-types` and `core-kernel` use `#![no_std]`, `alloc` and
`#![forbid(unsafe_code)]`. The types crate has no dependencies; the kernel depends
only on the types crate. Neither has filesystem, process, database, networking,
clock, environment or UI access. `cli` owns standard I/O and JSON translation,
using exact direct versions and the committed Cargo lockfile. A standard library
or unsafe restriction is not an OS sandbox for a future external executor.

```sh
cargo metadata --no-deps --format-version 1
cargo tree -p core-kernel
cargo fmt --all --check
cargo test -p core-kernel -p cli
```

Set `CARGO_TARGET_DIR` to an external build directory when working in a shared
checkout. Root-level Go gates remain separate from these focused Rust checks.
The scoped integration owner runs repository-wide gates before publication.

The semantic tests exercise actual CLI processes against in-process typed
outcomes, successful static discovery, missing/empty/unverified credentials,
unknown or mismatched effects, unknown mutations, strict context decoding,
bounded reads, output failures and redaction. A real initial failure showed that
Serde's internally tagged unit variant accepted extra context fields despite
`deny_unknown_fields`; the strict empty-struct variant fixes that behavior.
Causal checks also bypass the shared gate protecting successful discovery, so
an unavailable task handler cannot conceal an ineffective policy test.

The incident/rule dispositions and legacy command inventory live in `design/`.
Inventory coverage is not implementation coverage. Unresolved dispositions and
later slices prevent any claim that all dacli incidents have been solved.
The storage transaction, real scoped authenticator, graph, admission, effects,
evidence, observations, legacy import and cutover remain separately qualified
slices. This prototype makes no legacy CLI/wire compatibility, production
activation, release or source-authority transition claim.

# Fenced owner recovery

Native worker expiry, revocation, cancellation and stale heartbeats leave resource
protection intact. Cooperative terminal observations do not prove every writer
subprocess stopped. Only explicit owner recovery releases retained protection.
This is local cooperative governance, not OS containment or filesystem-lock theft.

`native recovery upgrade --config <private-owner-file>` explicitly migrates schema9
to10 under the existing authority transaction. It retains identities, grants,
verification, review, publication, dispatch and lifecycle history. Genuine older
schema6/7/8/9 writers refuse10. No global migration or session grants are implicit.

Use `native recovery preview --config <private-owner-file> --request <private-file>
--json` with `Kind` (`reservation` or `lease`) and `ProtectionID`. The returned
`RecoveryTarget` names exact workspace, task, dispatch/intent when present,
attempt, original protection ID/fence, assigned worker, immutable owner-enrolled worker family, repository and checkout.
A preview is historical observation and grants no release authority.

`native recovery apply --config <private-owner-file> --request <private-file> --json`
accepts `RecoveryRequest` with an immutable `OperationKey`, exact `Target`,
`Mode: owner-reconciliation`, nonempty `Rationale`, `Uncertainty` and `ReleaseRisk`.
The owner explicitly accepts uncertainty about remaining writers and the risk of
reassignment. Normal controller, bridge and worker credentials cannot recover.

Alternatively the owner may configure a versioned `RecoveryPolicy` through
`native recovery configure --config <private-owner-file> --request <private-file>
--json`. Setting `AcceptCooperativeTerminal` with explicit policy rationale accepts
the limitations of separately authenticated cooperative terminal reports. An
`authenticated-terminal` request identifies that current policy revision plus
exact last terminal observation sequence and evidence digest. This is an owner
assurance decision; it makes no stronger host-termination claim.

Recovery atomically records an immutable tombstone, withdraws the old assigned
worker family's authority for that task and all existing/future descendants, releases only the matched
protection and advances the global fence. It preserves the original dispatch
`Converted` value, intent and lifecycle bindings and original fences. Reserved
pre-intent dispatches, intents before worker acknowledgement, converted native
leases and standalone leases are recoverable; unknown forms remain refused.
Unrelated task authority stays valid. A separately owner-enrolled replacement
may acquire released resources with a newer fence. Canonical task WIP remains
independent of retained resource occupancy.

Exact owner-authenticated replay returns the original receipt without changing
current protection. Altered replay, another key for the recovered target, stale
target fences and fence overflow refuse. Failed persistence returns no receipt.
Late lifecycle reports retain their original binding; they do not restore old
worker authority or release replacement protection. These local commands add no
MCP tools; plugin exposure is tracked separately under core593. Recovery receipts
are evidence for later workflow reconciliation, not task acceptance or merge.

Terminal evidence is authenticated when the enrolled bridge records it. Recovery
consumes that immutable recorded evidence under the *current* owner's assurance
policy; later bridge revocation does not erase historical authentication. New
reports still require current bridge credentials. A historical policy revision
cannot substitute for the current policy, and revision contents cannot be reused
with altered policy. This intentional choice lets the owner reconcile terminal
history after a host disconnect without pretending current host availability.

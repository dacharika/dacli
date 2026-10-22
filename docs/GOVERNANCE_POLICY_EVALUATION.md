# Publication policy evaluation

`internal/governance/policy.EvaluatePublication` evaluates supplied observations
for concrete issue creation or update. It performs no authentication, disk,
environment, network, publication, or authority mutation. All identifiers and
recorded-authority flags are observations. Every result is `observed/advisory`,
including allow; assessments are historical data and never bearer permission.

The result binds the exact actor, session, attempt, workspace, installation,
authority, repository, action/resource, sorted deduplicated field set, lowercase
SHA256 of the projected payload, policy revision, lease ID/generation, validity
window and observation time, plus visibility/request/recorded flags. The digest
uses domain-separated JSON and SHA256. Inputs are copied; times are UTC. Issue
creation targets the repository itself; update targets a positive decimal issue
number in that repository. Closure and unknown actions/fields are unsupported.
Malformed bindings, digest or time windows deny.

Existing `publication.New`, `Allows` and `Reason` define disclosure decisions.
Explicit public internal requests without recorded repository authority need
approval; requests without the explicit flag deny. Unknown visibility never
enables internal disclosure, even with both flags, while safe fields retain the
existing policy. Private verdicts still require an explicit request. No separate
disclosure field allowlist is maintained here. `ProjectText` calls the existing
`Sanitize` implementation; content projection does not imply permission. The
caller must compute the digest of the exact projected outbound bytes: evaluation
cannot sanitize a digest or certify arbitrary source bytes.

An assessment cannot substitute for atomic current-authority single-use
consumption. A later transaction in the same durable authority aggregate must
reauthenticate current scope and repository disclosure authority, compare current
policy revision, live lease/fence, expiry and exact action/payload, and atomically
reject replay. The actual external outcome is recorded separately; uncertain
outcomes require reconciliation. This package implements neither consumption nor
external execution.

Tests cover the existing policy parity matrix, denial precedence, malformed
requests, canonical field sets, unchanged inputs, content sanitization and every
binding's digest sensitivity. Mutation receipts are retained by the delivery
owner alongside full repository verification evidence.

Delivery mutation evidence: forcing the explicit-request input to true in policy
construction produces `TestPublicationParity` failure for public findings with
`explicit=false, recorded=true` (allow versus deny). Removing lease generation
from the encoded binding produces
`TestBindingsChangeDigest/generation: binding mutation did not change digest`.
Both changes were restored before final verification.

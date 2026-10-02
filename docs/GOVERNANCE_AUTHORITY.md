# ADR: Versioned governance authority and lifecycle

Status: proposed contract design for #1070 under #1068. Implementation scope is
fixtures, typed records and executable structural validation. Native authenticated
Codex or other host integration is not implemented or claimed.

## Identity and authority

A workspace has exactly one active authority. Its stable ID and monotonically
increasing epoch identify the sole writer responsible for serializing decisions,
leases and fencing tokens. An installation identifies a registered installation,
not a human and not a source of implicit authority. A human identifies the
accountable principal through an explicit credential reference. A host-session
belongs to an installation; an agent-session belongs to that host-session;
an attempt binds one agent-session or host-session to a task and immutable revision. A retry
gets a new attempt ID; process IDs, provider names and mutable branches are not
identity or revision proof. Every record carries `governance/v1`, a nonempty ID
and the same workspace ID. References resolve only inside that workspace.

Credentials and delegation are explicit references to separately verified
material. Neither a parent process, a host capability declaration, nor a received
proposal supplies credentials. The authority verifies issuer, subject, audience,
workspace, permitted actions, expiration and revocation before honoring a
delegation. Secret credential bytes never belong in these metadata records.
Host capabilities describe a verified adapter's supported actions and maximum
assurance, not permission to perform them. The v1 structural validator requires
references and consistent lineage; authentication of those references is a
separate integration responsibility.

## Leases, evidence and decisions

Leases bind authority ID and epoch, attempt, resource, positive fencing token,
and a half-open issuance/expiry interval. Resource tokens strictly increase
across successive leases; overlapping leases or repeated tokens are invalid.
The authority allocates tokens atomically in durable storage. Every write must
compare the current epoch and resource fence, even if the former lease holder
is still running. Time alone cannot exclude a stale writer. The validator
checks snapshot consistency and decision timestamps; it cannot assert that a
snapshot is current, stop a process, or lock a resource.

Evidence binds workspace, subject attempt and revision, producing or reviewing session,
capability, origin and SHA-256 artifact digest. Observations are distinct from
authenticated-host provenance. An observed capability cannot assert the latter.
The authority verifies the digest against retained bytes and authenticates the
producer before trusting provenance. A self-reported check result is evidence
of a report, not proof that a command ran. Fixture host claims are synthetic.

Decisions bind authority epoch, accountable human, deciding delegated session, subject attempt,
lease fence, exact revision, timestamp and evidence IDs. Action-specific
minimum assurance is observed for completion, authenticated for acceptance and
merge, and human-approved for deployment. The decision cannot exceed its host
capability's assurance or actions. These levels express contract requirements;
a string claiming human-approved is not human approval. Deployments require an
explicit independently verified human authorization for the target and artifact.
Policies may raise these floors, never silently lower them.

Completion records that an attempt finished. Acceptance records a separate
review decision over that revision. Merge records the separately authorized
integration of that accepted revision. Deployment records promotion of the
merged artifact to an explicitly authorized target. Each later stage references
the immediately preceding decision for the same attempt and revision, with
nondecreasing timestamps. None implies the next stage. A changed revision needs
new evidence and decisions; a completion cannot be reused as acceptance or
landing proof. Structural validation does not substitute for independent review,
repository ancestry, successful checks, or actual deployment observation.

## Storage migration and recovery

Governance v1 is a new contract namespace. Existing markdown tasks and local
append-only event logs retain their authority and lifecycle meaning; this change
does not migrate storage or enable a new service writer. A future store must
persist authority epoch, identity and decision records, lease counters and
idempotency keys transactionally, retaining historical evidence references.
Unknown, absent or incompatible versions are rejected, never interpreted as v1.
Migration requires an explicit versioned reader/writer plan, backup, validation,
and atomic cutover with one writer. Historical records must not be rewritten
to suggest stronger provenance or assurance than originally obtained.

Recovery first observes durable authority identity, epoch, leases, attempts,
revision and decisions. Authority transfer requires explicit operator authority,
quiescing or fencing the old writer, and increasing the epoch before writes
resume. Counters must never reset after backup restoration. Lost counters or
uncertain authority state require refusal and reconciliation, not best-effort
replay. Expired leases require newly allocated tokens; stale decisions and
credentials do not become current by restart. Recovery preserves completed
stages and only retries an explicitly authorized, idempotent pending action.
No inferred deployment, rollback, credential refresh or destructive replay is
permitted from completion alone.

## Control-plane boundary

`contracts/controlplane/v1` remains unchanged and proposal-only. Remote task,
approval, policy or capability messages enter the local Inbox for verification
and a separate local authority decision. Receipt cannot mutate a task, grant a
capability, start an agent, accept work, merge or deploy it. Governance v1 does
not widen the existing control-plane metadata allowlist or claim that its wire
signatures authenticate a native host session.

## Enforcement, attribution and live obligations

Per-action enforcement is separate from authentication confidence. Cooperative
means the host promises to comply; guarded means an adapter checks an action;
authority-enforced means the authority mediates it with durable fencing;
unsupported means it must refuse. A high authentication assurance never upgrades
an enforcement mode. Capability declarations are synthetic metadata until a
host conformance proof establishes these properties. This proposed contract has
no live integration guarantee.

A host-session may own an attempt when a native host exposes no authenticated
agent identity. This is honest session-only attribution: consumers must not
invent a child identity or infer stronger provenance. Evidence's attempt and
revision identify the subject, while its session identifies the producer or
reviewer. A decision's session identifies the decider, independently of the
subject attempt's execution session. Independent-review policy must authenticate
both identities and evaluate independence; merely unequal IDs do not prove it.

Deployment targets and artifacts are typed workspace records. Deployment
requires an explicit human authorization reference binding workspace, action,
target, artifact, immutable revision and policy revision. Authorization issuance
and expiry delimit a half-open interval. Live consumers must authenticate the
human's authorization, verify the artifact bytes, recheck current policy and
revocation, and consume an idempotency/replay key atomically with action
reservation. Snapshot validation cannot prove freshness or single use. Other
stages do not carry deployment authority.

Revocation is fail-closed from its effective timestamp: future actions and
fenced writes by the revoked session (including children of a revoked host)
must be refused. Live write gates must consult current revocation as well as
current authority epoch, resource fence and lease expiry in one transaction.
Historical evidence and earlier decisions remain retained with their original
provenance. A snapshot can check decision timestamps against revocation; it
cannot enforce a running process's writes or invalidate cached credentials.

Cancellation requested is distinct from observed cessation. A request record
without an observation proves only intent. Hosts declare best-effort or
unsupported cancellation; neither guarantees termination. An observed timestamp
must follow the request, and consumers must authenticate the observation before
claiming cessation. Revocation and fencing remain mandatory even if host
cancellation fails or is unsupported. Process killing is not a substitute for
revoking authority.

Authorization action descriptors use the canonical length-prefixed UTF-8
encoding and SHA-256 algorithm specified in `contracts/governance/v1/README.md`.
They bind the exact target environment, artifact digest, revision, policy,
human, authorization identity and validity interval. Validation recomputes the
digest against resolved records rather than trusting a caller's label.
The consumption key is `(workspace, authorization ID)`. Distinct deployment
decisions cannot reuse it within a snapshot; live services must enforce its
single use transactionally across all history, including after expiry or
restart. A retry of the same decision returns its recorded result and cannot
execute again. Neither possession of the ID nor a matching unkeyed action digest
proves approval, identity, delegation or authenticity.

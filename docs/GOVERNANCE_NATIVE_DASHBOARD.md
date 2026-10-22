# Native governance in the local dashboard

Run `dacli dashboard --native-connection /absolute/private/connection.json`.
The optional connection is an existing owner-enrolled native session profile;
the dashboard neither discovers owner credentials nor enrolls, migrates or
changes authority. Without a connection, native data is explicitly unavailable.
Connection schemas 1 and 2 are supported. The connection and credential must be
private regular files, bound to this workspace and installation. Each read
revalidates the selected connection and authenticates in one bounded snapshot.

The supported `native enroll` worker connection includes read, workspace and
exact task scopes. It can display its own explicitly selected task detail.
Project-wide attention and spend require an explicitly owner-provisioned read
scope containing the project resource and its task resources. A task-only
connection displays permission state on those project-wide surfaces; this does
not silently broaden its grant. Project identity is derived from authoritative
bindings, and foreign task records are never returned. Project budget totals
remain unavailable unless every contributing historical allocation and current
budget task is authorized.

The task inspector joins exact attempt, lease, published revision, evidence,
reviewer and policy-decision IDs. Record controls reveal and focus only visible
records without replacing the dashboard route. Missing linked records are
labelled unavailable in the scoped snapshot. Historical evidence is not a fresh
verification, approval or execution permit; producer eligibility is not inferred
from a saved receipt. Declared path claims and lease fences remain visible after
expiry; expiry does not release resources.

Attention includes retained expired protections, disconnected or stale
cooperative lifecycle observations, current approval requirements, and the latest
current-policy preflight failure per task. Old failed preflights are historical
records rather than current blockers after a later ready proof. Lifecycle
freshness uses source observation time and policy freshness, not receipt time.
Native attention opens the Work task inspector. No native mutation or release
control is exposed.

Spend separates nullable measured provider tokens/cost from owner-estimated
allocation spent, reserved and limit amounts. Unknown usage remains reserved;
missing usage is unavailable, never zero or enforceable remaining spend.
Transcript files are not required or read. Existing legacy burn charts retain
their own source and calibration labels.

Authorities at schemas 6 through 16 are validated with the shared native decoder.
A schema-10 authority remains schema 10; unavailable newer services are explicitly
unsupported. Unsupported future schemas, corruption, revoked/expired identities,
changed profiles and unreadable private files yield typed error or permission
states without exposing private error details. The descriptor-first reader
refuses symlinks, FIFOs, nonregular files, public permissions and files larger
than 16 MiB. Records and attention are deterministically capped at 512, with a
visible limited state. This is a cooperative local observation, not containment
of arbitrary filesystem writers or proof that a host process has terminated.

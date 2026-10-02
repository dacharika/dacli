# Claim sandbox cleanup and recovery

`dacli cleanup --project <slug> --dry-run` includes a distinct `sandboxes`
collection in repository-cleanup/v3. Earlier plan IDs cannot authorize these
operations. No startup hook or automatic cleanup applies a plan.

Only the exact `.dacli/worktrees/.claim-sandboxes/<run-id>` directory can qualify.
The observer binds its source manifest (all administrative and working entries),
process record, successful runtime exit, versioned projection manifest, parent
commit request/receipt, handoff and consumption receipt, task and writer records.
A terminal outcome alone is insufficient: claims must be released, the recorded
process absent, matching writers absent and task done. The receipt commit must
have the manifest base as parent and be contained in the configured landing base.
Projected paths, handoff hashes, receipt tree, actual bytes and file modes agree.
Sandbox HEAD may remain at its initial base with projected modifications dirty.

Initial support is conservative. Extra ignored/untracked files, missing files,
symlinks, gitlinks, staged changes, divergent HEAD/refs, novel Git objects,
alternates, active hooks, nonstandard Git configuration and unsupported filesystem
entries preserve the source with an explicit reason. Ordinary directories,
including empty directories and their modes, are included in recovery. All file
bytes must match the receipt; initial support does not archive divergent content.
Unknown process evidence prevents proving the absence of a writer.

Apply pins both base and projection under `refs/dacli/cleanup/<identity>/`, then
writes a private mode-0600 gzip JSON archive containing the complete working
manifest and only file bytes that differ from the base. The canonical repository
already contains reconstruction objects; cleanup does not relocate a clone's
object database. The archive is reconstructed in staging and its complete
manifest checked before the durable audit records verified recovery. All source
and eligibility identities are re-observed immediately before exact-source
removal. Archive or evidence failures retain the source. An interruption during
removal leaves a `removal-started` audit and the verified recovery archive.
A repeated apply never overwrites an existing recovery audit: reconcile the
source or use the recorded restore path.

Restore uses the same CLI/MCP restore API (`--restore <plan-id> --artifact
<sandbox-identity>`). It validates managed paths, private archive type/digest,
entries, parent relationships and complete reconstruction before reserving an
absent destination. Existing destinations are refused. Archives and recovery
refs remain retained after restore. If final placement is interrupted, recovery
remains available; inspect and preserve the partial destination before retrying.

Restoration means identical working-tree files and directory/file modes.
Git administration, reflogs, index metadata, repository configuration and refs
are not recreated. The restored directory is a recovered file tree rather than
a new writable execution checkout. Durable transcripts, process records,
projection, handoff, parent and acceptance receipts are never sandbox cleanup
sources. Existing generated regular-file quarantine remains a separate policy.

The safety boundary is cooperative filesystem integrity: cleanup holds its own
lock and rechecks source/evidence, but does not freeze a checkout or acquire a
global writer lease. It does not claim protection against hostile concurrent OS
users. Owners review real dry-runs before applying; workers exercise temporary
real-Git fixtures and never clean their own or another live sandbox.

Tests measure total retained bytes across a real temporary repository including
canonical Git objects, recovery refs, archives and audits. A compact archive
must reduce this total. Production inventory and the particular governance009,
011, 012 and 013 runs must be independently observed by the owner in the shared
workspace; the isolated worker checkout contains none of those durable records.

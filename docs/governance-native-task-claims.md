# Owner managed native task path claims

`dacli native task claims preview` and `dacli native task claims apply` provide
an explicit owner path for changing a task's `claims` frontmatter after task
creation. Both commands require the private native owner configuration and
credential. Preview returns an integrity checked plan bound to the exact task
file bytes, project, task ID, and prior claim list. Apply refuses if any of
those facts changed after preview.

Apply validates claims with the same portable repository-relative grammar used
by native lease admission. It rejects absolute, traversal, noncanonical,
case-folded, `.git`, `.dacli`, symlinked, or duplicate paths. The task update
runs under the task's cross-process lock. While it runs, an owner-authenticated
read-only native authority guard holds the existing authority lock and refuses
when a lease or reservation still protects the task, including expired but
retained reservations. It does not change the authority file, session grants,
worker credentials, or native schema.

Task creation still accepts claims in the Go API through `store.TaskOpts.Claims`.
`dacli task add` does not currently expose a claims flag; setting the owner
claims after creation is the explicit native owner path described here.

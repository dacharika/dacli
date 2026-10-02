# Teams, roles and independent authority

Roles describe skills, ownership, escalation and intended responsibilities.
They are useful context for a native host choosing agents. Role files do not
launch a process or authenticate a native session.

## Role documents

`.dacli/roles/<name>.md` retains scope, out-of-scope, skills, shortcuts, grant,
escalation and WIP fields. `out_of_scope` wins over `scope`; an empty scope is
not an empty permission set. Existing runtime/model fields remain historical or
planning metadata, not a provider adapter configuration enforced by dacli.

Use `dacli role list`, `dacli team`, `dacli team route <path>` and `dacli route <path>`
to inspect ownership. Historical agent records describe lineage; they do not
prove a native process is alive or eligible to approve a change.

## Native sessions

An owner enrolls an explicit workspace/task scope using private native authority.
The host runs the agent and supplies the configured MCP connection. An `rw` or
`ro` role label is not native authentication or OS sandbox enforcement. Direct
host filesystem actions remain outside the cooperative governance interface.
Agents sharing one MCP connection share one enrolled session identity.

## Review independence

Native review requires an owner-issued grant, an eligible independent credential
family, current checklist/policy and the exact revision and trusted evidence.
It does not acquire the worker lease or close a task. Neither changing the role
name nor selecting another model establishes independence. See
[GOVERNANCE_NATIVE_REVIEW.md](GOVERNANCE_NATIVE_REVIEW.md).

## Escalation

Use typed help requests and durable findings/decisions so a host can resolve a
blocker without losing its evidence. Escalate scope changes to the owner; do not
infer permission from an attempted out-of-scope write. A loop that finds no
eligible work should stop, rather than fabricate progress. Native delegation
and repeated work cycles belong to the host.

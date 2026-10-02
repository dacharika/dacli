# Extracting transport-independent operations

`Status`, `ListTasks`, and `GetTask` are the first shared operations. CLI and MCP
read tools reach them through the existing feature adapters. The service owns
the operation; adapters retain compatible text/JSON rendering and error mapping.
Services must not resolve ambient identity, discover a workspace from the current
directory, launch providers, or import feature/transport slices.

For each request, an adapter resolves the selected workspace and the caller's
identity using its trusted authentication boundary. It then constructs a fresh
`RequestContext` and passes it explicitly to the service. A zero context is
refused. The context copies the selected workspace and binds a principal and
session to it; it does not discover authority from labels supplied in a request.

`NewRequestContext` checks a supplied governance snapshot's structure, workspace,
human/session lineage, and effective session or parent revocation. The adapter
must authenticate the principal and obtain the snapshot from a trusted current
authority before calling it. Credential references and a structurally valid
bundle do not authenticate a caller. The constructor is not an enrollment or
credential-verification API.

Validation happens at construction. The context does not retain a live
revocation source or validation time. Do not cache it as an ongoing session
authorization: construct it again from current identity and revocation data for
each request. Future mutation services must check current scopes, policy,
authority epoch, lease fence and revocation atomically at their write boundary;
these read constructors do not implement that transaction.

`NewCooperativeContext` explicitly adapts the existing CLI token resolver. It
preserves legacy read behavior for resolved read-only/read-write identities and
does not fabricate a native host session or authenticated-host assurance. MCP
still invokes the same feature adapters; later structured tools can invoke the
service directly once their authentication boundary is implemented.

When extracting another operation, move shared behavior into this package or
lower shared entities, resolve context at the adapter boundary, retain transport
compatibility there, and add a regression that rejects unchecked/wrong-workspace
contexts. `TestApplicationDependencies` guards the package boundary;
`TestMCPReadServiceParity` exercises both front ends. Mutation evidence must show
the relevant guard failing before acceptance.

`NewNativeContext` is the explicit native credential boundary. Supply a trusted
`sessions.Authority`, the selected workspace, session ID and bearer credential
out of band (never transport argv). It reloads current durable authority and
requires an exact `read` action and `workspace:<selected workspace ID>` resource;
wildcards and task-only resources do not authorize workspace-wide reads. Missing,
invalid, expired, revoked, wrong-workspace or insufficient-scope credentials yield
redacted policy refusals (exit 3). The authority also verifies installation
binding against its durable state. No credential is retained in the context.

This authenticates possession of a scoped DACLI session credential. The bound
principal is the authenticated session ID; it does not establish authenticated
human, host, or native subagent identity, and self-asserted labels are not
credentials. Authenticate afresh with `NewNativeContext` for every request.
Previously constructed contexts are snapshots and cannot certify ongoing
revocation enforcement or mutation policy. The snapshot and cooperative
constructors above remain legacy adapter boundaries with their existing trust
requirements; they do not provide native credential authentication.

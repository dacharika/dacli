# Native governance MCP readers

`dacli native reader add` enrolls a dedicated native MCP session for reading governance context. It uses the existing private owner configuration and credential; it does not upgrade the authority schema or reuse an operator, worker, verifier, reviewer, or controller session.

Choose exactly one scope:

```sh
dacli native reader add --config OWNER \
  --workspace --connection NEW_CONNECTION --credential NEW_CREDENTIAL

dacli native reader add --config OWNER --project exact-slug \
  --connection NEW_CONNECTION --credential NEW_CREDENTIAL

dacli native reader add --config OWNER --task t-CANONICAL_ULID \
  --connection NEW_CONNECTION --credential NEW_CREDENTIAL --expires 8h
```

Workspace scope uses the workspace's canonical ID. Project scope accepts only the exact project slug. Task scope accepts only the task's canonical `t-…` ID; aliases, numbers, and qualified shorthand are rejected. Missing, duplicate, or invalid scope selectors do not broaden to workspace scope. Expiry defaults to one hour and must be positive and no longer than 24 hours.

The new connection and credential paths must be absolute, different, and unused. They are created exclusively with mode `0600` before the reader is issued. The session has exactly the `read` action and one resource matching the selected scope. Native MCP rechecks the session, expiry, revocation, action, and resource on each call. It does not provide attempt, lease, publication, evidence, verification, review, controller, or owner authority.

Configure the MCP client to use the new connection file as its selected profile, then restart or reconnect that MCP process. A process that already loaded another connection keeps its existing identity; writing a new profile does not hot-switch a running process.

If persistence fails after issuance, the command preserves the issued actor and both reserved files, prints only the session ID and file paths, and asks the owner to reconcile them before retrying. The credential is never printed.

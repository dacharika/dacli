# Local operator transport

The native operator client owns a `dacli operator serve` child process. The
process uses stdin and stdout as a JSON-lines channel; it opens no TCP or Unix
socket and does not accept terminal commands on the protocol stream. Diagnostics
and CLI errors go to stderr. Stdout contains one JSON response frame per input
frame.

## Provision a separate reader

An owner creates a dedicated project reader with:

```text
dacli native operator add --config <owner-private-config> --project <slug> \
  --connection <new-private-connection> --credential <new-private-credential>
```

The owner may choose an expiry with `--expires`. Provisioning writes separate
private files and enrolls only `read` plus `operator.read` for the selected
workspace and project. Ordinary worker credentials that have `read` do not
grant operator access. Do not copy the owner credential or worker credential
into the app. The app receives the connection file path and reads the private
credential file through dacli; bearer material is never passed in argv,
environment variables, protocol frames, or logs.

The app starts the child with:

```text
dacli operator serve --connection <private-connection>
```

The connection is immutable for the child lifetime. Before each read, dacli
reopens and validates the connection, workspace, credential, and authority,
then authenticates the exact workspace/project/task scope. Revocation and expiry
therefore apply to the next frame. Unsupported connection or authority schemas
fail closed; this path does not migrate authority files.

## Frames and lifetime

Each request is one v1 JSON object terminated by a newline. Unknown fields,
trailing JSON values, unsupported versions, invalid IDs, unknown operations,
and unsupported cursors are rejected. The client selects the workspace and
project in every request; task reads name an exact task. The Go handler calls
the existing task, activity, timeline, attention, and governance projection
builders rather than maintaining a second operator data model.

Request frames are limited to 64 KiB, response frames to 1 MiB, and each read
has a 15-second deadline. Request limits are bounded and are not silently
clamped. Activity cursors are producer-owned; other projections without proven
resumption mark capped results partial/truncated. The response preserves source
and observation identity and historical, stale, limited, and unknown states.
Errors use stable redacted codes and do not include raw request text, private
paths, credentials, commands, prompts, transcripts, or private evidence bodies.

The client closes stdin when it disconnects; EOF ends the child process. If a
read exceeds its deadline, dacli emits a redacted failure frame and exits so a
stalled projection cannot leave a hidden child or accumulate abandoned request
goroutines. The client reports an exited child or closed pipe as disconnected.

This transport is read-only. It does not accept, sync, reserve work, launch an
agent, execute a command from the client, contact providers, or open a listener.

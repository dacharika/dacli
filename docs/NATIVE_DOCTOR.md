# Native compatibility doctor

`dacli native doctor` collects bounded read-only observations for one selected
plugin manifest and, when available, its exact package archive, plus an
optional native connection and optional capability receipt.
It emits `native-doctor/v1` JSON with the Go build version, VCS revision and
dirty-build state, selected plugin name and version, a SHA-256 digest of the
manifest bytes, and the authority schema range supported by this binary.

```sh
dacli native doctor --plugin-manifest /private/plugin.json \
  --package-artifact /private/plugin.zip \
  --runtime-artifact /private/host-runtime \
  --connection /private/native-connection.json \
  --capability-file /private/host-capability.json --project core --json
```

The command reads only the exact paths provided. Manifest input is limited to
64 KiB; a selected package artifact to 64 MiB; native connections to
16 KiB; capability evidence to 16 KiB; authority state to the existing 32 MiB
sessions limit. `artifact_digest` is the SHA-256 of the exact selected package
artifact bytes. `manifest_digest` identifies only the selected manifest bytes.
When `--package-artifact` is omitted, `artifact_digest` is `unknown` and
`artifact_status` is `not_selected`; manifest, runtime and authority inspection
remain available. Supported manifest dialects are the portable manifest and
Codex compatibility manifest. A selected archive must contain the exact
selected manifest bytes at a supported path (`plugin.json` for portable;
`plugin.json` or `.codex-plugin/plugin.json` for Codex), at archive root or
under the `dacli-governance/` package root.
The result does not include input paths, credentials, credential hashes, or
private profile contents. An unavailable or invalid required manifest, or an
unavailable or mismatched selected archive, yields a redacted exit-3 refusal;
the command does not emit a partial versioned result or next action for those
errors. Optional runtime and authority observations may report unavailable or
unknown status with a redacted next action.

The current `dacli` process is reported as running. `host_runtime_availability`
remains `unknown`, including when `--runtime-artifact` is selected. That flag
reports only whether the explicitly selected artifact has bounded, regular-file
and executable metadata (`metadata_available` or `unavailable`). The executable
is never run; artifact metadata does not establish actual host runtime
availability, runtime compatibility or host execution.

Descriptor-safe public and private metadata readers are currently implemented
for Darwin, Linux, FreeBSD, OpenBSD, NetBSD and DragonFly. Other platforms fail
closed until an adapter is qualified. This command does not make a universal
claim that a host runtime is ready.

Authority schema compatibility is reported separately from authenticated
startup. A supported older schema can be compatible. The current authorized
schema is present only after the selected actor credential matches a live,
unrevoked enrollment and the native connection successfully starts. Missing or
invalid enrollment, expiration, actor revocation and supported-profile mismatch
have distinct status values. If the bounded shared authority reader cannot
authenticate under a supported schema, authority availability, schema and
binding classifications remain `unknown`; the doctor does not infer them from
unauthenticated header fields.

Capability freshness is an owner-provided observation bound to the selected
connection workspace and project, not live host evidence. Tool discovery and
host execution remain `unknown` until separate host-specific probes produce
versioned evidence. A fresh capability receipt does not prove either.
The doctor never enrolls an actor, upgrades a schema, replaces a profile,
changes policy or cache state, or releases a resource. Use the returned next
action as guidance for an owner-controlled follow-up; rerunning the doctor
performs inspection only.

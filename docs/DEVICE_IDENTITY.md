# Device identity

Device identity authenticates a human-selected device to an explicitly selected
control plane. It does not grant a coding agent local governance authority.
Use a native macOS build with cgo and Security.framework support. Linux,
Windows and cgo-disabled macOS builds return `unsupported_store` with exit 3;
there is no plaintext credential fallback.

## Select and authenticate a device

```sh
dacli auth login --provider https://control.example --tenant tenant-id --name Laptop --json
dacli auth whoami --provider https://control.example --tenant tenant-id --device device-id --json
dacli auth logout --provider https://control.example --tenant tenant-id --device device-id --json
```

Replace the example endpoint and IDs with the configured control plane and the
identity returned by login. HTTPS is required except for explicit loopback
HTTP development endpoints. The server's owner configures the external OAuth
issuer/endpoints, approved subject-to-account mappings, current membership and
signing keys. A tenant flag selects a route; it does not enroll an account or
create membership.

Login prints only the verification URI, human user code and polling information.
In JSON mode the challenge goes to stderr and the final public identity goes to
stdout. The private device code is never printed. Polling waits for the supplied
interval and adds five seconds after each `slow_down`. Denial and challenge
expiry terminate login; interrupting it reports cancellation separately.

Credentials and the device private signing key live only in the native OS
credential store. Local device-profile files contain the provider, tenant,
account, device, scopes, expiry and public signing-key metadata. Credentials are
never command arguments. Public identity output omits tokens and credential-store
locators. Select each tenant/device explicitly; there is no ambient tenant switch
that silently overwrites another identity.

`auth whoami` is mutating: it can refresh credentials and repair non-secret
metadata before revalidating the live session, device and membership. Expiry
calculations use relative server lifetimes and request elapsed time rather than
assuming matching server and laptop clocks. A refresh request ID is saved in the
OS store before sending the request, so a retry after an ambiguous response uses
the same identity. Successful rotation invalidates the previous bearer session;
the current response can be replayed without imposing a 32-refresh lifetime cap.
This is a bounded replay window, not indefinite replay of superseded sessions.

Logout always attempts to delete the credential for the exact
provider/tenant/device tuple, including when its profile metadata is missing or
corrupt. Remote revocation and local removal are reported separately. A failed
or unreachable provider produces `remote_revoke_unknown` after successful local
removal; this does not establish remote revocation. Revoke that exact device at
the provider when reachable. A native store refusal reports
`local_removal_refused`; it must not report successful cleanup. Other selected
identities retain their credentials and profiles.

Policy refusals such as unsupported storage, denial, revoked grants and malformed
provider responses exit 3. Retryable connectivity errors exit 1. Output contains
stable diagnostic codes and repair guidance rather than raw provider bodies,
HTTP errors containing credentials or native credential-store paths.

## Local qualification and its limits

The existing release verifier checks artifact build settings, framework links,
archives, checksums and SBOMs, then exercises extracted Darwin binaries through
a loopback fixture. The extended lifecycle check is optional and local:

```sh
python3 scripts/qualify-device-identity.py \
  --binary /path/to/extracted/dacli --arch arm64 \
  --evidence-dir /private/tmp/dacli-device-identity-evidence
```

Repeat with the corresponding `amd64` executable and a separate evidence
directory; Apple Silicon needs Rosetta for that architecture. The check exits 77
when the host cannot execute the selected architecture. It exercises native
Keychain access through the ordinary auth commands with controlled identities,
short-lived refresh, revoked/offline outcomes, exact tenant isolation and
missing/corrupt metadata logout. Its mode-0600 JSON report records sanitized
results; synthetic secret values remain in process memory and are scanned
against captured outputs, sampled process arguments and temporary profile files.
The provider fixture is synthetic; it does not log
into an external identity service or certify a deployed control plane. Provider
adapter tests separately exercise the OAuth device/token/UserInfo HTTP boundary,
and service tests exercise refresh rotation, replay and revocation.

Live operator qualification still requires a configured external OAuth provider,
approved account mappings, native OS-store consent and the deployed service's
persistence and membership policy. No local receipt implies production readiness,
provider availability, release publication or deployment. See
[release capabilities](RELEASE_ARTIFACTS.md), [control-plane assembly](CONTROL_PLANE.md)
and [trust boundaries](TRUST.md#device-identity-and-connected-projects).

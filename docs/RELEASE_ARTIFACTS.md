# Release artifact capabilities

GoReleaser runs on a native macOS runner. Its Darwin `amd64` and `arm64`
artifacts are built with cgo and link Apple's Security and CoreFoundation
frameworks, which selects the native Keychain credential store used by device
login. Linux and Windows artifacts keep cgo disabled and explicitly refuse
native credential storage. macOS builds made with cgo disabled have the same
unsupported behavior.

`./scripts/verify-release-artifacts.sh` makes a local snapshot without
publishing. It checks both Darwin artifact build settings and framework links,
the platform archives, per-archive SBOMs, and checksums. On a native macOS host,
the verifier runs each executable artifact that the host can execute through a
loopback-only synthetic device provider: `auth login` writes the generated
identity to Keychain, `auth whoami` reads it, and `auth logout` removes it. It
uses isolated temporary profile directories and removes the exact unique test
Keychain account even after an interrupted flow. Synthetic credential markers
are checked against outputs and profile files. The focused credential-store
test also checks Keychain operation failure paths; unsupported builds have a
separate refusal test. No provider credentials or file fallback are used.

The explicitly selected `--portable-snapshot` mode is for Ubuntu CI. It retains
all six archives, checksums and SBOMs, but builds every target with cgo disabled;
its macOS archives are therefore unsupported for login. Its separate
GoReleaser config disables release publication. The native release config is
used only on macOS and does not inherit a portable-build environment override.

Publication remains a maintainer action: only a manually pushed `v*` tag starts
`.github/workflows/release.yml`. The workflow builds a release; it does not
create or push tags, and verification scripts only create local snapshots.

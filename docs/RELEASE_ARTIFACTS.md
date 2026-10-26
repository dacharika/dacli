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

Publication remains a separate maintainer action. `.github/workflows/release.yml`
has no tag-push trigger: a maintainer manually dispatches it from the default
branch with an existing `vX.Y.Z` tag, the full commit SHA that tag is expected
to name, and a confirmation that repeats the tag. A resolver checked out at the
trusted workflow revision peels lightweight and annotated tags through the
GitHub API, verifies the exact SHA before checking out release source, and pins
the tag ref object as well as the peeled commit. It rechecks both before release
work and immediately before GoReleaser. The source checkout is pinned to that
SHA and GoReleaser receives the selected tag explicitly. A moved, deleted,
malformed, or mismatched tag fails closed; no
workflow creates or pushes tags. Tag resolution and GitHub Release creation are
separate API operations, so the last ref check cannot make their boundary
atomic. The resolver also refuses a tag that already has a GitHub Release; a
failed or partial publication therefore is not an automatic retry. GoReleaser
can reject duplicate asset uploads, so inspect an existing release before any
manual recovery. There is no automated rollback: a maintainer must assess and
remove a bad release through GitHub's release controls while preserving the
selected source tag, then use a corrected new version tag if republishing is
needed. After a successful run, verify the published `checksums.txt` against
the six archives and confirm each archive has its `.sbom.json` asset. The
workflow reports publication success; it does not download and independently
qualify the assets after upload. Verification scripts only create local
snapshots.

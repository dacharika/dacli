#!/usr/bin/env bash
# Prove the release path WITHOUT publishing anything.
#
#   ./scripts/verify-release-artifacts.sh
#   ./scripts/verify-release-artifacts.sh --portable-snapshot
#
# Builds a snapshot, then checks the things a consumer actually depends on:
# every supported platform is present, each archive has an SBOM, the checksums
# file verifies, and the binary a user would extract by following the README
# actually runs.
#
# The default native snapshot is built on macOS with real Keychain-capable
# Darwin artifacts. The explicit portable mode is for Ubuntu CI and labels its
# Darwin binaries cgo-off/unsupported. Publishing is a separate, human act —
# the maintainer pushes a `v*` tag and .github/workflows/release.yml takes it.
# Nothing here creates or pushes a tag, and this script is the evidence that
# when a tag IS pushed, the path works.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

verifier_tmp=$(mktemp -d "${TMPDIR:-/tmp}/dacli-release-artifacts.XXXXXX")
verifier_tmp=$(cd "$verifier_tmp" && pwd -P)
cleanup() {
  status=$?
  trap - EXIT INT TERM
  rm -rf "$verifier_tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() { echo "✗ $1"; exit 1; }
print_sanitized_qualification_output() {
  printf '%s\n' "$1" \
    | sed 's/dacli-synthetic-access-marker/[redacted synthetic marker]/g; s/dacli-synthetic-refresh-marker/[redacted synthetic marker]/g' >&2
}

mode=${1:-native}
case "$mode" in
  native|--portable-snapshot) ;;
  *) fail "usage: $0 [--portable-snapshot]" ;;
esac

OS=$(go env GOOS)
command -v goreleaser >/dev/null || { echo "✗ goreleaser not installed"; exit 1; }
command -v syft       >/dev/null || { echo "✗ syft not installed (needed for SBOMs)"; exit 1; }

if [ "$mode" = native ]; then
  [ "$OS" = darwin ] || fail "native mode must run on macOS so GoReleaser can link Security.framework"
  command -v otool >/dev/null || fail "otool not installed (needed to inspect native framework links)"
  # Guard the source selection as well as the final binaries.
  darwin_files=$(CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go list -f '{{.GoFiles}} {{.CgoFiles}}' ./internal/credentialstore)
  case "$darwin_files" in
    *keychain_darwin.go*) ;;
    *) fail "Darwin+cgo did not select keychain_darwin.go: $darwin_files" ;;
  esac
  case "$darwin_files" in
    *unsupported.go*) fail "Darwin+cgo also selected unsupported.go: $darwin_files" ;;
  esac
else
  # The portable profile is explicitly incapable of native login. Keep the
  # source and final artifact checks tied to that documented boundary.
  darwin_files=$(CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go list -f '{{.GoFiles}} {{.CgoFiles}}' ./internal/credentialstore)
  case "$darwin_files" in
    *unsupported.go*) ;;
    *) fail "portable Darwin snapshot did not select unsupported.go: $darwin_files" ;;
  esac
  case "$darwin_files" in
    *keychain_darwin.go*) fail "portable Darwin snapshot unexpectedly selected Keychain: $darwin_files" ;;
  esac
fi
for target_os in linux windows; do
  files=$(CGO_ENABLED=0 GOOS="$target_os" GOARCH=amd64 go list -f '{{.GoFiles}} {{.CgoFiles}}' ./internal/credentialstore)
  case "$files" in
    *unsupported.go*) ;;
    *) fail "$target_os/CGO=0 did not select unsupported.go: $files" ;;
  esac
done
if [ "$mode" = native ]; then
  echo "✓ Darwin+cgo selects Keychain; Linux and Windows select explicit unsupported store"
else
  echo "✓ portable Darwin/Linux/Windows snapshots select explicit unsupported store"
fi

echo "▸ snapshot build"
if [ "$mode" = native ]; then
  goreleaser release --snapshot --clean --skip=homebrew >/dev/null 2>&1
else
  goreleaser release --config .goreleaser.portable-snapshot.yaml --snapshot --clean >/dev/null 2>&1
fi

# 1. Every supported platform, or a consumer on the missing one has no install.
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64; do
  # Either extension counts: windows ships .zip, the rest .tar.gz. Globbing
  # both at once fails whenever one does not match, which is always.
  # || true: the assignment takes the LAST command's status, and one of the
  # two globs never matches, which under set -e would kill the script.
  found=$( { ls dist/*"${target}".tar.gz 2>/dev/null; ls dist/*"${target}".zip 2>/dev/null; } || true )
  [ -n "$found" ] || fail "no archive for ${target}"
done
echo "✓ 6 platform archives"

if [ "$mode" = native ]; then
  # The actual archives must contain cgo-enabled Mach-O executables linked to
  # Apple's credential frameworks; archive names and checksums alone miss a
  # config drift back to unsupported.go.
  for arch in amd64 arm64; do
    archive=$(ls dist/*_darwin_"${arch}".tar.gz 2>/dev/null | head -1)
    [ -n "$archive" ] || fail "no darwin_${arch} archive to qualify"
    target_tmp=$(mktemp -d "$verifier_tmp/target.XXXXXX")
    tar xz -C "$target_tmp" -f "$archive"
    binary="$target_tmp/dacli"
    [ -x "$binary" ] || fail "darwin_${arch} archive contains no executable dacli"
    build_info=$(go version -m "$binary")
    printf '%s\n' "$build_info" | grep -q 'build[[:space:]]*CGO_ENABLED=1' \
      || fail "darwin_${arch} artifact was not built with cgo enabled"
    printf '%s\n' "$build_info" | grep -q "build[[:space:]]*GOARCH=${arch}" \
      || fail "darwin_${arch} artifact has the wrong Go architecture"
    linked=$(otool -L "$binary")
    printf '%s\n' "$linked" | grep -q 'Security.framework' \
      || fail "darwin_${arch} artifact does not link Security.framework"
    printf '%s\n' "$linked" | grep -q 'CoreFoundation.framework' \
      || fail "darwin_${arch} artifact does not link CoreFoundation.framework"
    set +e
    qualification_output=$(bash ./scripts/qualify-darwin-release-artifact.sh "$binary" "$arch" 2>&1)
    qualification_status=$?
    set -e
    if [ "$qualification_status" -eq 0 ]; then
      echo "$qualification_output"
    elif [ "$qualification_status" -eq 77 ]; then
      print_sanitized_qualification_output "$qualification_output"
      fail "darwin_${arch} artifact execution is unverified; native release qualification requires both architectures"
    else
      print_sanitized_qualification_output "$qualification_output"
      fail "darwin_${arch} artifact Keychain qualification failed (exit $qualification_status)"
    fi
  done
  echo "✓ both Darwin artifacts are cgo-enabled and link Security/CoreFoundation"
else
  for arch in amd64 arm64; do
    archive=$(ls dist/*_darwin_"${arch}".tar.gz 2>/dev/null | head -1)
    [ -n "$archive" ] || fail "no darwin_${arch} portable archive"
    target_tmp=$(mktemp -d "$verifier_tmp/target.XXXXXX")
    tar xz -C "$target_tmp" -f "$archive"
    build_info=$(go version -m "$target_tmp/dacli")
    printf '%s\n' "$build_info" | grep -q 'build[[:space:]]*CGO_ENABLED=0' \
      || fail "portable darwin_${arch} artifact was not explicitly built cgo-off"
    printf '%s\n' "$build_info" | grep -q "build[[:space:]]*GOARCH=${arch}" \
      || fail "portable darwin_${arch} artifact has the wrong Go architecture"
  done
  echo "✓ portable Darwin artifacts are explicitly cgo-off and unsupported for login"
fi

# 2. An SBOM per archive. A release note claiming contents is not evidence.
for a in dist/*.tar.gz dist/*.zip; do
  [ -f "${a}.sbom.json" ] || fail "no SBOM for $(basename "$a")"
done
echo "✓ an SBOM per archive"

# 3. The checksums file actually verifies. A checksums.txt nobody checks is
#    decoration.
( cd dist && shasum -a 256 -c checksums.txt >/dev/null 2>&1 \
    || sha256sum -c checksums.txt >/dev/null 2>&1 ) \
  || fail "checksums.txt does not verify"
echo "✓ checksums verify"

# 4. THE README PATH. Extract the way the README tells a user to, and run it.
#    This is the acceptance criterion: a tagged release must be installable and
#    exercisable using README commands only.
ARCH=$(go env GOARCH)
ARCHIVE=$(ls dist/*_"${OS}"_"${ARCH}".tar.gz 2>/dev/null | head -1)
[ -n "$ARCHIVE" ] || fail "no archive for this host (${OS}_${ARCH})"

TMP=$(mktemp -d "$verifier_tmp/readme.XXXXXX")
tar xz -C "$TMP" -f "$ARCHIVE"          # the README's `| tar xz`
[ -x "$TMP/dacli" ] || fail "the archive contains no executable dacli"
"$TMP/dacli" version >/dev/null          || fail "the extracted binary does not run"
"$TMP/dacli" --help  >/dev/null          || fail "the extracted binary cannot print help"
echo "✓ the README install path yields a working binary ($(basename "$ARCHIVE"))"

echo
echo "✓ release artifacts verified — nothing published, no tag created"

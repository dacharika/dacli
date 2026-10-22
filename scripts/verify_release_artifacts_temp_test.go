//go:build darwin || linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyReleaseArtifactsHonorsTmpDirAndCleansOwnedRoot(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate verifier script")
	}
	scriptPath := filepath.Join(filepath.Dir(source), "verify-release-artifacts.sh")
	repoRoot := t.TempDir()
	tmpDir := t.TempDir()
	binDir := t.TempDir()
	realMkdirTemp, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(tmpDir, "created-root")
	writeTestExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
if [ "$1" = rev-parse ] && [ "$2" = --show-toplevel ]; then
  printf '%s\n' "$SYNTHETIC_REPO_ROOT"
  exit 0
fi
exit 2
`)
	writeTestExecutable(t, filepath.Join(binDir, "mktemp"), `#!/bin/sh
is_dir=false
if [ "$1" = -d ]; then is_dir=true; shift; fi
template=$1
case "$template" in
  "$TMPDIR"/*) ;;
  *) echo "mktemp template outside isolated TMPDIR: $template" >&2; exit 91 ;;
esac
if [ "$is_dir" = true ]; then
  created=$("$REAL_MKTEMP" -d "$@") || exit $?
else
  created=$("$REAL_MKTEMP" "$@") || exit $?
fi
created=$(cd "$created" && pwd -P) || exit $?
printf '%s\n' "$created" > "$MKTEMP_RECORD"
printf '%s\n' "$created"
`)
	// Invalid mode reaches usage validation immediately after temp-root creation,
	// before Go, GoReleaser, archive tools, or native credential code are needed.
	cmd := exec.Command("bash", scriptPath, "--invalid-mode")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+tmpDir,
		"REAL_MKTEMP="+realMkdirTemp,
		"MKTEMP_RECORD="+record,
		"SYNTHETIC_REPO_ROOT="+repoRoot,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("invalid mode unexpectedly succeeded: %s", output)
	}
	if !strings.Contains(string(output), "usage:") {
		t.Fatalf("did not reach mode usage validation: %s", output)
	}
	createdBytes, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("verifier did not create its temp root through isolated TMPDIR: %v; output=%s", err, output)
	}
	created := strings.TrimSpace(string(createdBytes))
	canonicalTmpDir, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(created) || filepath.Dir(created) != canonicalTmpDir {
		t.Fatalf("verifier temp root %q is not an absolute child of isolated TMPDIR %q", created, tmpDir)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("verifier did not clean its owned temp root %q (stat err %v)", created, err)
	}
}

func writeTestExecutable(t *testing.T, path, source string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o700); err != nil {
		t.Fatal(err)
	}
}

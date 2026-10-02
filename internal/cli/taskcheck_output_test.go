package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskCheckVerificationOutputIsCompactUnlessRequested(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, 0, "init", "--name", "compact-verification")
	run(t, dir, 0, "project", "add", "P", "--slug", "p")
	run(t, dir, 0, "task", "add", "Verify compactly", "--project", "p", "--accept", "command: test")
	compact := run(t, dir, 1, "task", "check", "001", "--n", "1", "--verify", "printf sensitive-detail; exit 9")
	if strings.Contains(compact, "sensitive-detail") || !strings.Contains(compact, "verification failed: exit=9") || !strings.Contains(compact, "--full-output") {
		t.Fatalf("compact failure leaked output or omitted summary:\n%s", compact)
	}
	artifacts, err := os.ReadDir(filepath.Join(dir, ".dacli", "verification-artifacts"))
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("failed verification artifact was not retained: entries=%d err=%v", len(artifacts), err)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, ".dacli", "verification-artifacts", artifacts[0].Name()))
	if err != nil || string(artifact) != "sensitive-detail" {
		t.Fatalf("retained artifact = %q err=%v", artifact, err)
	}
	full := run(t, dir, 1, "task", "check", "001", "--n", "1", "--verify", "printf sensitive-detail; exit 9", "--full-output")
	if !strings.Contains(full, "sensitive-detail") || !strings.Contains(full, "verification failed: exit=9") {
		t.Fatalf("expanded failure omitted bounded output or summary:\n%s", full)
	}
}

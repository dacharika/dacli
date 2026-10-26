//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package guidance

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiagnosisAndPreviewRefuseFIFO(t *testing.T) {
	root := fixtureTree(t, "legacy")
	target := filepath.Join(root, "SKILL.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(target, 0o600); err != nil {
		t.Fatal(err)
	}
	d := Diagnose([]string{root}, "", "", "unknown", "")
	if d.CombinedStatus != StatusUnsafe {
		t.Fatalf("diagnosis=%+v", d)
	}
	if _, err := Preview(root, ReviewedSourceID); err == nil {
		t.Fatal("FIFO target previewed")
	}
}

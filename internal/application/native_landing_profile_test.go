package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenNativeLandingOwnerRequiresPinnedOwnerProfile(t *testing.T) {
	f := acceptanceSetup(t)
	owner, err := OpenNativeLandingOwner(f.access.owner.path, "p")
	if err != nil || owner == nil || owner.workspace.ID != f.w.ID || owner.project != "p" {
		t.Fatalf("open explicit owner profile: owner=%+v err=%v", owner, err)
	}
	if _, err := OpenNativeLandingOwner(f.access.owner.path, "../p"); err == nil {
		t.Fatal("unsafe project segment accepted")
	}
	if _, err := OpenNativeLandingOwner(f.access.owner.path, "missing"); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err := OpenNativeLandingOwner(f.workerToken, "p"); err == nil {
		t.Fatal("worker token accepted as owner profile")
	}
	if err := owner.selectedOwnerCurrent(f.owner); err != nil {
		t.Fatalf("current owner selection refused: %v", err)
	}
	// A second owner profile with the same workspace but a different selected
	// authority must not be substituted after construction.
	alternate := filepath.Join(t.TempDir(), "alternate-owner.json")
	if err := os.WriteFile(alternate, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owner.owner.path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := owner.selectedOwnerCurrent(f.owner); err == nil {
		t.Fatal("changed selected owner profile was accepted")
	}
}

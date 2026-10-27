package application

import (
	"context"
	"testing"
)

func TestOpenNativeLandingOwnerRequiresPinnedOwnerProfile(t *testing.T) {
	f := acceptanceSetup(t)
	observer := NativeLandingPRObserver(func(context.Context, string, int) (NativeLandingPRObservation, error) {
		return NativeLandingPRObservation{}, nil
	})
	owner, err := OpenNativeLandingOwner(f.access.owner.path, "p", observer)
	if err != nil || owner == nil || owner.workspace.ID != f.w.ID || owner.project != "p" {
		t.Fatalf("open explicit owner profile: owner=%+v err=%v", owner, err)
	}
	if _, err := OpenNativeLandingOwner(f.access.owner.path, "../p", observer); err == nil {
		t.Fatal("unsafe project segment accepted")
	}
	if _, err := OpenNativeLandingOwner(f.access.owner.path, "missing", observer); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err := OpenNativeLandingOwner(f.workerToken, "p", observer); err == nil {
		t.Fatal("worker token accepted as owner profile")
	}
	if _, err := OpenNativeLandingOwner(f.access.owner.path, "p", nil); err == nil {
		t.Fatal("missing PR observer accepted")
	}
	if err := owner.selectedOwnerCurrent(f.owner); err != nil {
		t.Fatalf("current owner selection refused: %v", err)
	}
}

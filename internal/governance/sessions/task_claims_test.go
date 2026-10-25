package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnerTaskClaimsProtectionHoldsForRetainedReservationWithoutWritingAuthority(t *testing.T) {
	f := swarmSetup(t)
	if err := f.a.WithOwnerTaskClaimsProtection(f.owner, "task-a", func(p TaskClaimsProtection) error {
		if len(p.Holds) != 0 {
			t.Fatalf("unexpected initial holds: %v", p.Holds)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wave := swarmReserve(t, f, "claims-protection", swarmPlan(t, f))
	if len(wave.Dispatches) == 0 {
		t.Fatal("fixture did not reserve a task")
	}
	dispatch := wave.Dispatches[0]
	if err := f.a.transaction(false, true, func(s *state) error {
		d := s.Dispatches[dispatch.ID]
		d.Reservation.ExpiresAt = time.Now().Add(-time.Hour)
		s.Dispatches[dispatch.ID] = d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	guardBefore, err := os.ReadFile(f.a.path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.a.WithOwnerTaskClaimsProtection(f.owner, dispatch.Reservation.TaskID, func(TaskClaimsProtection) error {
		t.Fatal("callback ran while reservation retained")
		return nil
	})
	if err == nil {
		t.Fatal("retained reservation did not protect claims")
	}
	guardAfter, err := os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(guardBefore, guardAfter) {
		t.Fatal("refused guard changed authority bytes", err)
	}
	_ = f.a.WithOwnerTaskClaimsProtection(f.owner, "task-a", func(TaskClaimsProtection) error { return nil })
	guardAfter, err = os.ReadFile(f.a.path)
	if err != nil || !bytes.Equal(guardBefore, guardAfter) {
		t.Fatal("read/lock guard changed authority bytes", err)
	}
}

func TestCanonicalResourcesExtractionPreservesExistingAliasesAndDuplicates(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonicalResources(root, "task-a", []Resource{{Kind: "path", Path: ".dacli/runs"}, {Kind: "path", Path: ".dacli/runs"}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, resource := range got {
		if resource.Kind == "path" && resource.Path == ".dacli/runs" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("legacy native resource behavior changed, got %+v", got)
	}
}

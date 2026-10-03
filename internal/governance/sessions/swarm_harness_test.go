package sessions

import (
	"errors"
	"testing"
)

// core/530 is now a native resolver regression, not a launcher dry-run test.
func TestNativeSwarmPinnedReviewerPreviewReservationAndWorkerDispatch(t *testing.T) {
	f := swarmSetup(t)
	f.p.Revision = "two-harness-reviewers"
	f.p.Roles = append(f.p.Roles, SwarmRole{ID: "aaa-other-review", Kind: "review", Harness: "other", Capacity: 10})
	if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
		t.Fatal(e)
	}
	plan := swarmPlan(t, f)
	wave := swarmReserve(t, f, "pinned-review-wave", plan)
	if len(wave.Dispatches) != 2 {
		t.Fatalf("expected two pinned-reviewer dispatches, got %d", len(wave.Dispatches))
	}
	for _, d := range wave.Dispatches {
		var selected SwarmVerdict
		for _, v := range plan.Verdicts {
			if v.TaskID == d.Reservation.TaskID {
				selected = v
			}
		}
		if !selected.Selected || selected.ReviewRole != "codex-review" || d.ReviewRole != selected.ReviewRole || d.Harness != "codex" {
			t.Fatalf("preview/reservation selected reviewer outside requested harness: preview=%+v dispatch=%+v", selected, d)
		}
		live, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence})
		if e != nil || live.ReviewRole != selected.ReviewRole || live.Harness != "codex" {
			t.Fatalf("live admission changed pinned reviewer: %+v %v", live, e)
		}
		worker, token, at := leaseAttempt(t, f.a, f.owner, d.Reservation.TaskID, "pinned-worker-"+d.Reservation.TaskID, d.Reservation.Checkout)
		lease, e := f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{OperationKey: "pinned-handoff-" + d.ID, DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence})
		if e != nil || lease.SessionID != worker.ID {
			t.Fatalf("pinned worker dispatch refused: %+v %v", lease, e)
		}
		if e = f.a.transaction(false, false, func(s *state) error {
			dispatched := s.Dispatches[d.ID]
			if !dispatched.Converted || dispatched.ReviewRole != selected.ReviewRole || dispatched.Harness != "codex" || s.Leases[lease.ID].SessionID != worker.ID {
				t.Fatalf("worker dispatch changed reviewer/harness: %+v", dispatched)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestNativeSwarmMissingPinnedRolePreviewAndLiveAdmission(t *testing.T) {
	for _, kind := range []string{"implement", "review"} {
		for _, boundary := range []string{"preview", "admit", "handoff"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				f := swarmSetup(t)
				if boundary == "preview" {
					f.p.Revision = "missing-pinned-" + kind
					for i := range f.p.Roles {
						if f.p.Roles[i].Kind == kind {
							f.p.Roles[i].Harness = "other"
						}
					}
					if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
						t.Fatal(e)
					}
					plan := swarmPlan(t, f)
					if len(plan.Verdicts) != 3 {
						t.Fatalf("expected three missing-role preview verdicts, got %d", len(plan.Verdicts))
					}
					for _, v := range plan.Verdicts {
						if v.Selected || v.Reason != "compatible_role_missing" {
							t.Fatalf("missing pinned %s role selected in preview: %+v", kind, v)
						}
					}
					return
				}
				wave := swarmReserve(t, f, "before-role-withdrawal", swarmPlan(t, f))
				d := wave.Dispatches[0]
				// A revision mismatch would refuse first and never exercise live harness
				// validation. This isolated authority fixture models a stale/malformed role
				// snapshot under the same revision, retaining every other admission input.
				if e := f.a.transaction(false, true, func(s *state) error {
					p := s.SwarmPolicies[f.p.ProjectID]
					for i := range p.Roles {
						if p.Roles[i].Kind == kind {
							p.Roles[i].Harness = "other"
						}
					}
					s.SwarmPolicies[f.p.ProjectID] = p
					return nil
				}); e != nil {
					t.Fatal(e)
				}
				if boundary == "admit" {
					_, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: d.ID, ExpectedFence: d.Reservation.Fence})
					if !errors.Is(e, ErrDenied) {
						t.Fatalf("missing pinned %s role admitted live: %v", kind, e)
					}
				} else {
					worker, token, at := leaseAttempt(t, f.a, f.owner, d.Reservation.TaskID, "missing-role-worker", d.Reservation.Checkout)
					_, e := f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{OperationKey: "missing-role-handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence})
					if !errors.Is(e, ErrDenied) {
						t.Fatalf("missing pinned %s role handed off live: %v", kind, e)
					}
				}
				if e := f.a.transaction(false, false, func(s *state) error {
					if s.Dispatches[d.ID].Converted {
						t.Fatal("refused live role selection converted reservation")
					}
					if _, ok := s.Leases[d.ID]; ok {
						t.Fatal("refused live role selection minted worker lease")
					}
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}

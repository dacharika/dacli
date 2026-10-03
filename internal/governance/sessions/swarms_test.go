package sessions

import (
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type swarmFixtureAdmission struct {
	capabilityCalls, withdrawAfter int
	snapshot                       SwarmSnapshot
	denied                         bool
	capabilityDenied               bool
}

func (a *swarmFixtureAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	a.capabilityCalls++
	if a.withdrawAfter > 0 && a.capabilityCalls > a.withdrawAfter {
		return ErrDenied
	}
	if a.capabilityDenied || h != "codex" || d != "owner-probe-v1" {
		return ErrDenied
	}
	return nil
}
func (a *swarmFixtureAdmission) ObserveSwarm(w, p string) (SwarmSnapshot, error) {
	if a.denied {
		return SwarmSnapshot{}, ErrDenied
	}
	return a.snapshot, nil
}
func (a *swarmFixtureAdmission) AdmitLease(w, p, t string) error {
	if a.denied {
		return ErrDenied
	}
	return nil
}

type swarmFixture struct {
	a            *Authority
	owner, token string
	s            Session
	ad           *swarmFixtureAdmission
	p            SwarmPolicy
	dirs         []string
}

func swarmSetup(t *testing.T) swarmFixture {
	t.Helper()
	a, owner, _, _, at, _ := leaseSetup(t)
	ad := &swarmFixtureAdmission{snapshot: SwarmSnapshot{Digest: "snapshot-1", Candidates: []SwarmCandidate{
		{TaskID: "task-a", ProjectID: "project", Status: "open", Reason: "ready", Priority: 0, Resources: []Resource{{Kind: "path", Path: "src/a"}}},
		{TaskID: "task-b", ProjectID: "project", Status: "open", Reason: "ready", Priority: 0, Resources: []Resource{{Kind: "path", Path: "src/a"}}},
		{TaskID: "task-c", ProjectID: "project", Status: "open", Reason: "ready", Priority: 1, Resources: []Resource{{Kind: "path", Path: "other"}}},
	}}}
	if e := a.UpgradeSwarms(owner); e != nil {
		t.Fatal(e)
	}
	upgraded, e := OpenSwarm(a.path, a.binding, ad)
	if e != nil {
		t.Fatal(e)
	}
	a = upgraded
	dirs := []string{at.Observation.Locator, secondCheckout(t, at), secondCheckout(t, at)}
	for i, d := range dirs {
		resolved, e := filepath.EvalSymlinks(d)
		if e != nil {
			t.Fatal(e)
		}
		dirs[i] = resolved
	}
	for _, id := range []string{"task-b", "task-c"} {
		if e := a.BindTask(owner, TaskBinding{TaskID: id, ProjectID: "project", Repository: at.Observation.Repository, AdmissionEnabled: true}); e != nil {
			t.Fatal(e)
		}
	}
	resources := []string{"project:project", "task:task-a", "task:task-b", "task:task-c", "path:src/a", "path:other"}
	s, token, e := a.Enroll(owner, Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	p := SwarmPolicy{ProjectID: "project", Revision: "policy-1", Harness: "codex", CapabilityDigest: "owner-probe-v1", Width: 2, WIP: 3, TTL: time.Hour, Roles: []SwarmRole{{ID: "codex-implement", Kind: "implement", Harness: "codex", Capacity: 2}, {ID: "codex-review", Kind: "review", Harness: "codex", Capacity: 2}}, Placements: map[string]SwarmPlacement{}}
	for i, id := range []string{"task-a", "task-b", "task-c"} {
		p.Placements[id] = SwarmPlacement{Checkout: dirs[i], ImplementKind: "implement", ReviewKind: "review"}
	}
	if e := a.ConfigureSwarm(owner, p); e != nil {
		t.Fatal(e)
	}
	return swarmFixture{a: a, owner: owner, token: token, s: s, ad: ad, p: p, dirs: dirs}
}
func swarmPlan(t *testing.T, f swarmFixture) SwarmPlan {
	t.Helper()
	p, e := f.a.PlanSwarm(f.token, f.s.ID, "project")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func swarmReserve(t *testing.T, f swarmFixture, key string, p SwarmPlan) SwarmWave {
	t.Helper()
	w, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: key, Plan: p, ExpiresAt: time.Now().Add(10 * time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	return w
}
func TestNativeSwarmCollisionBackfillAndHarnessPinning(t *testing.T) {
	f := swarmSetup(t)
	f.p.Revision = "policy-both-harnesses"
	f.p.Roles = append(f.p.Roles, SwarmRole{ID: "aaa-other-review", Kind: "review", Harness: "other", Capacity: 10})
	if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
		t.Fatal(e)
	}
	p := swarmPlan(t, f)
	if p.Verdicts[0].ReviewRole != "codex-review" {
		t.Fatalf("planner selected reviewer outside pinned harness: %+v", p.Verdicts[0])
	}
	if len(p.Verdicts) != 3 || !p.Verdicts[0].Selected || p.Verdicts[1].Selected || p.Verdicts[1].Reason != "resource_conflict" || !p.Verdicts[2].Selected {
		t.Fatalf("collision backfill violated: %+v", p.Verdicts)
	}
	w := swarmReserve(t, f, "reserve", p)
	if len(w.Dispatches) != 2 {
		t.Fatalf("wave consumed rejected slot: %+v", w)
	}
	for _, d := range w.Dispatches {
		v, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
		if e != nil || v.Harness != "codex" || v.ReviewRole != "codex-review" {
			t.Fatalf("pinned admission: %+v %v", v, e)
		}
	}
	var st state
	if e := f.a.transaction(false, false, func(s *state) error { st = *s; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(st.Sessions) != 2 || len(st.Attempts) != 1 {
		t.Fatal("planner created identity or checkout attempt")
	}
	// Two compatible harnesses may coexist in storage corruption / later host
	// policies; the shared resolver still pins the explicitly selected harness.
	if e := f.a.transaction(false, true, func(s *state) error {
		p := s.SwarmPolicies["project"]
		p.Roles = append([]SwarmRole{{ID: "aaa-other-review", Kind: "review", Harness: "other", Capacity: 10}}, p.Roles...)
		s.SwarmPolicies["project"] = p
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, d := range w.Dispatches {
		v, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
		if e != nil || v.ReviewRole != "codex-review" {
			t.Fatalf("wrong harness admission %v %+v", e, v)
		}
	}
}
func TestNativeSwarmAtomicConcurrentReservationAndLeaseExclusion(t *testing.T) {
	f := swarmSetup(t)
	p := swarmPlan(t, f)
	req := ReserveWaveRequest{OperationKey: "reserve-a", Plan: p, ExpiresAt: time.Now().Add(10 * time.Minute)}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"reserve-a", "reserve-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			r := req
			r.OperationKey = key
			_, e := f.a.ReserveWave(f.token, f.s.ID, r)
			results <- e
		}(key)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrDenied) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("atomic reservation admitted %d overlapping waves", success)
	}
	worker, token, at := leaseAttempt(t, f.a, f.owner, "task-b", "worker-b", f.dirs[1])
	_, e := f.a.AcquireLease(token, worker.ID, acquireReq(at, "worker-acquire", "src/a"))
	denied(t, e)
	if e := f.a.transaction(false, true, func(s *state) error {
		for id, d := range s.Dispatches {
			d.Reservation.ExpiresAt = time.Now().Add(-time.Hour)
			s.Dispatches[id] = d
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	_, e = f.a.AcquireLease(token, worker.ID, acquireReq(at, "worker-after-expiry", "src/a"))
	denied(t, e)
}
func TestNativeSwarmFreshAdmissionAndImmutableReplay(t *testing.T) {
	for _, change := range []string{"snapshot", "dependency", "claims", "policy", "stop", "revoked", "scope", "checkout", "capability"} {
		t.Run(change, func(t *testing.T) {
			f := swarmSetup(t)
			plan := swarmPlan(t, f)
			req := ReserveWaveRequest{OperationKey: "reserve", Plan: plan, ExpiresAt: time.Now().Add(10 * time.Minute)}
			w, e := f.a.ReserveWave(f.token, f.s.ID, req)
			if e != nil {
				t.Fatal(e)
			}
			switch change {
			case "capability":
				f.ad.capabilityDenied = true
			case "snapshot":
				f.ad.snapshot.Digest = "snapshot-2"
			case "dependency":
				f.ad.denied = true
			case "claims":
				f.ad.snapshot.Candidates[0].Resources = []Resource{{Kind: "path", Path: "other"}}
			case "policy":
				f.p.Revision = "policy-2"
				if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
					t.Fatal(e)
				}
			case "stop":
				f.p.Revision = "stopped"
				f.p.Stopped = true
				if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
					t.Fatal(e)
				}
			case "revoked":
				if e := f.a.Revoke(f.owner, f.s.ID); e != nil {
					t.Fatal(e)
				}
			case "scope":
				if e := f.a.transaction(false, true, func(s *state) error {
					r := s.Sessions[f.s.ID]
					r.Scope.Actions = []string{"swarm.plan"}
					s.Sessions[f.s.ID] = r
					return nil
				}); e != nil {
					t.Fatal(e)
				}
			case "checkout":
				if e := os.WriteFile(f.dirs[0]+"/dirty", []byte("dirty"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			// A snapshot-only change preserves resource admission but stales the plan.
			if change != "snapshot" {
				_, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{w.Dispatches[0].ID, w.Dispatches[0].Reservation.Fence})
				denied(t, e)
			}
			newreq := req
			newreq.OperationKey = "stale"
			_, e = f.a.ReserveWave(f.token, f.s.ID, newreq)
			denied(t, e)
			replay, e := f.a.ReserveWave(f.token, f.s.ID, req)
			if change == "revoked" || change == "scope" {
				denied(t, e)
			} else if e != nil || !reflect.DeepEqual(w, replay) {
				t.Fatalf("historical replay lost %v", e)
			}
		})
	}
}
func TestNativeSwarmIndependentAtomicHandoff(t *testing.T) {
	f := swarmSetup(t)
	w := swarmReserve(t, f, "reserve", swarmPlan(t, f))
	d := w.Dispatches[0]
	worker, token, at := leaseAttempt(t, f.a, f.owner, "task-a", "worker-a", f.dirs[0])
	req := HandoffDispatchRequest{OperationKey: "handoff", DispatchID: d.ID, AttemptID: at.ID, ExpectedFence: d.Reservation.Fence}
	l, e := f.a.HandoffDispatch(token, worker.ID, req)
	if e != nil || l.SessionID != worker.ID || l.Fence <= d.Reservation.Fence {
		t.Fatalf("handoff failed %v %+v", e, l)
	}
	replay, e := f.a.HandoffDispatch(token, worker.ID, req)
	if e != nil || !reflect.DeepEqual(l, replay) {
		t.Fatal("handoff replay changed", e)
	}
	_, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
	denied(t, e)
	_, e = f.a.AcquireLease(token, worker.ID, acquireReq(at, "separate-acquire", "src/a"))
	denied(t, e)
}
func TestNativeSwarmExplicitMigrationPreservesAuthorityAndRejectsLegacyWriter(t *testing.T) {
	a, owner := setup(t)
	s, token := enroll(t, a, owner)
	before, e := os.ReadFile(a.path)
	if e != nil {
		t.Fatal(e)
	}
	next, e := OpenSwarm(a.path, a.binding, &swarmFixtureAdmission{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = next.Authenticate(token, s.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("schema6 adopted without owner migration", e)
	}
	denied(t, next.UpgradeSwarms(token))
	after, _ := os.ReadFile(a.path)
	if string(before) != string(after) {
		t.Fatal("denied migration changed authority")
	}
	if e = next.UpgradeSwarms(owner); e != nil {
		t.Fatal(e)
	}
	if _, e = next.Authenticate(token, s.ID); e != nil {
		t.Fatal("migration lost existing session", e)
	}
	legacy := *a
	legacy.version = 5
	if _, e = legacy.Authenticate(token, s.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("legacy writer accepted schema7", e)
	}
	if _, e = a.Authenticate(token, s.ID); e != nil {
		t.Fatal("current worker factory cannot read explicitly upgraded schema7", e)
	}
	if e = next.transaction(false, false, func(st *state) error {
		if len(st.Sessions) != 1 || st.OwnerHash != hash(owner) {
			t.Fatal("history not preserved")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(a.path)
	var fields map[string]json.RawMessage
	if e = json.Unmarshal(raw, &fields); e != nil {
		t.Fatal(e)
	}
	delete(fields, "Dispatches")
	raw, _ = json.Marshal(fields)
	if e = os.WriteFile(a.path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = next.Authenticate(token, s.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("missing schema7 records accepted", e)
	}
}

func TestNativeSwarmFreshCheckoutCommitAndClaimAliases(t *testing.T) {
	t.Run("clean_commit_advance", func(t *testing.T) {
		f := swarmSetup(t)
		wave := swarmReserve(t, f, "reserve", swarmPlan(t, f))
		d := wave.Dispatches[0]
		gitTest(t, f.dirs[0], "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "advance")
		_, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
		denied(t, e)
		worker, token, at := leaseAttempt(t, f.a, f.owner, "task-a", "worker-after-advance", f.dirs[0])
		_, e = f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{"handoff", d.ID, at.ID, d.Reservation.Fence})
		denied(t, e)
	})
	t.Run("tracked_scope_symlink", func(t *testing.T) {
		f := swarmSetup(t)
		if e := os.Symlink(t.TempDir(), filepath.Join(f.dirs[0], "src")); e != nil {
			t.Fatal(e)
		}
		gitTest(t, f.dirs[0], "add", "src")
		gitTest(t, f.dirs[0], "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "tracked alias")
		plan := swarmPlan(t, f)
		if plan.Verdicts[0].Selected || plan.Verdicts[0].Reason != "claims_invalid" {
			t.Fatalf("checkout claim alias admitted: %+v", plan.Verdicts[0])
		}
	})
}
func TestNativeSwarmFreshRoleAndWIPCapacity(t *testing.T) {
	for _, kind := range []string{"external_lease", "canonical_active"} {
		t.Run(kind, func(t *testing.T) {
			f := swarmSetup(t)
			wave := swarmReserve(t, f, "reserve", swarmPlan(t, f))
			d := wave.Dispatches[0]
			if kind == "external_lease" {
				dir := secondCheckout(t, Attempt{Observation: waveObservation(t, f)})
				worker, token, at := leaseAttempt(t, f.a, f.owner, "task-d", "external-worker", dir)
				mustAcquire(t, f.a, worker, token, acquireReq(at, "external-lease", "src/b"))
			} else {
				f.ad.snapshot.Candidates = append(f.ad.snapshot.Candidates, SwarmCandidate{TaskID: "task-d", ProjectID: "project", Status: "active", Reason: "status_active"})
			}
			_, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("fresh role capacity ignored: %v", e)
			}
			worker, token, at := leaseAttempt(t, f.a, f.owner, "task-a", "worker-a", f.dirs[0])
			_, e = f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{"handoff", d.ID, at.ID, d.Reservation.Fence})
			if !errors.Is(e, ErrDenied) {
				t.Fatalf("fresh handoff capacity ignored: %v", e)
			}
		})
	}
}
func waveObservation(t *testing.T, f swarmFixture) checkout.Observation {
	t.Helper()
	obs, e := checkout.Observe(f.dirs[0])
	if e != nil {
		t.Fatal(e)
	}
	return obs
}
func TestNativeSwarmMissingPinnedReviewRoleFailsClosed(t *testing.T) {
	f := swarmSetup(t)
	f.p.Revision = "other-review-only"
	f.p.Roles = []SwarmRole{{ID: "implement", Kind: "implement", Harness: "codex", Capacity: 2}, {ID: "review", Kind: "review", Harness: "other", Capacity: 2}}
	if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
		t.Fatal(e)
	}
	plan := swarmPlan(t, f)
	for _, v := range plan.Verdicts {
		if v.Selected || v.Reason != "compatible_role_missing" {
			t.Fatalf("missing pinned review role admitted: %+v", v)
		}
	}
}

func TestNativeSwarmFinalCapabilityBoundary(t *testing.T) {
	for _, operation := range []string{"reserve", "admit", "handoff"} {
		t.Run(operation, func(t *testing.T) {
			f := swarmSetup(t)
			plan := swarmPlan(t, f)
			if operation == "reserve" {
				f.ad.capabilityCalls = 0
				f.ad.withdrawAfter = 2
				_, e := f.a.ReserveWave(f.token, f.s.ID, ReserveWaveRequest{OperationKey: "reserve", Plan: plan, ExpiresAt: time.Now().Add(time.Minute)})
				denied(t, e)
				return
			}
			wave := swarmReserve(t, f, "reserve", plan)
			d := wave.Dispatches[0]
			f.ad.capabilityCalls = 0
			f.ad.withdrawAfter = 1
			if operation == "admit" {
				_, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
				denied(t, e)
			} else {
				worker, token, at := leaseAttempt(t, f.a, f.owner, "task-a", "worker-a", f.dirs[0])
				_, e := f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{"handoff", d.ID, at.ID, d.Reservation.Fence})
				denied(t, e)
			}
		})
	}
}
func TestNativeSwarmMalformedPolicyCannotAttestBindings(t *testing.T) {
	f := swarmSetup(t)
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	bad := f.p
	bad.Width = 0
	bad.Revision = "malformed"
	e = f.a.ConfigureSwarmWithBindings(f.owner, bad, []TaskBinding{{TaskID: "new-task", ProjectID: "project", Repository: f.p.Placements["task-a"].Checkout, AdmissionEnabled: true}})
	denied(t, e)
	after, e := os.ReadFile(f.a.path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("malformed owner policy changed bindings", e)
	}
}

func TestNativeSwarmMigrationPreservesVerificationReviewPublicationHistory(t *testing.T) {
	for _, kind := range []string{"verification_review", "publication"} {
		t.Run(kind, func(t *testing.T) {
			var a *Authority
			var owner string
			if kind == "verification_review" {
				f := nativeReviewSetup(t)
				f.propose(t)
				a = f.a
				owner = f.owner
			} else {
				f := publicationSetup(t, true)
				d := f.decision(t)
				if _, e := f.a.ConsumePublication(f.owner, consumption(d)); e != nil {
					t.Fatal(e)
				}
				a = f.a
				owner = f.owner
			}
			before, e := os.ReadFile(a.path)
			if e != nil {
				t.Fatal(e)
			}
			if e = a.UpgradeSwarms(owner); e != nil {
				t.Fatal(e)
			}
			after, e := os.ReadFile(a.path)
			if e != nil {
				t.Fatal(e)
			}
			var old, new map[string]json.RawMessage
			if e = json.Unmarshal(before, &old); e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(after, &new); e != nil {
				t.Fatal(e)
			}
			for key, value := range old {
				if key == "Version" || key == "SwarmPolicies" || key == "SwarmOperations" || key == "Dispatches" {
					continue
				}
				if !reflect.DeepEqual(value, new[key]) {
					t.Fatalf("migration changed existing %s history", key)
				}
			}
			if len(new["Dispatches"]) == 0 || string(new["Dispatches"]) != "{}" {
				t.Fatal("new empty maps not persisted")
			}
		})
	}
}

func TestNativeSwarmWIPOnlyPlanAndFreshAdmission(t *testing.T) {
	for _, operation := range []string{"planner", "admit", "handoff"} {
		t.Run(operation, func(t *testing.T) {
			f := swarmSetup(t)
			f.p.Revision = "wip-only"
			f.p.WIP = 2
			for i := range f.p.Roles {
				f.p.Roles[i].Capacity = 10
			}
			if e := f.a.ConfigureSwarm(f.owner, f.p); e != nil {
				t.Fatal(e)
			}
			active := SwarmCandidate{TaskID: "task-d", ProjectID: "project", Status: "active", Reason: "status_active"}
			if operation == "planner" {
				f.ad.snapshot.Candidates = append(f.ad.snapshot.Candidates, active)
				plan := swarmPlan(t, f)
				selected := 0
				for _, v := range plan.Verdicts {
					if v.Selected {
						selected++
					}
				}
				reason := ""
				for _, v := range plan.Verdicts {
					if v.TaskID == "task-c" {
						reason = v.Reason
					}
				}
				if selected != 1 || reason != "capacity_exhausted" {
					t.Fatalf("WIP-only planner admitted %d workers: %+v", selected, plan.Verdicts)
				}
				return
			}
			wave := swarmReserve(t, f, "reserve", swarmPlan(t, f))
			d := wave.Dispatches[0]
			f.ad.snapshot.Candidates = append(f.ad.snapshot.Candidates, active)
			if operation == "admit" {
				_, e := f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{d.ID, d.Reservation.Fence})
				if !errors.Is(e, ErrDenied) {
					t.Fatalf("WIP-only dispatch admitted a third active task: %v", e)
				}
			} else {
				worker, token, at := leaseAttempt(t, f.a, f.owner, "task-a", "worker-a", f.dirs[0])
				_, e := f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{"handoff", d.ID, at.ID, d.Reservation.Fence})
				if !errors.Is(e, ErrDenied) {
					t.Fatalf("WIP-only handoff admitted a third active task: %v", e)
				}
			}
		})
	}
}

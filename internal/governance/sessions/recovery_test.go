package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func recoverySetup(t *testing.T, ack bool) (dispatchFixture, DispatchIntent) {
	t.Helper()
	f, i := lifecycleSetup(t, ack)
	if e := f.a.UpgradeRecovery(f.owner); e != nil {
		t.Fatal(e)
	}
	return f, i
}
func reconcileRequest(t *testing.T, f dispatchFixture, i DispatchIntent) RecoveryRequest {
	t.Helper()
	kind, id := "reservation", i.DispatchID
	if i.Lease != nil {
		kind, id = "lease", i.Lease.ID
	}
	target, e := f.a.OwnerRecoveryTarget(f.owner, kind, id)
	if e != nil {
		t.Fatal(e)
	}
	return RecoveryRequest{OperationKey: "recover", Target: target, Mode: "owner-reconciliation", Rationale: "owner inspected isolated fixture", Uncertainty: "host and all subprocess termination unverified", ReleaseRisk: "owner accepts overlapping writer risk"}
}
func TestNativeRecoveryOwnerEvidenceFenceAndImmutableReplay(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(map[bool]string{false: "reservation", true: "lease"}[ack], func(t *testing.T) {
			f, i := recoverySetup(t, ack)
			req := reconcileRequest(t, f, i)
			if _, e := f.a.Recover(f.workerToken, req); !errors.Is(e, ErrDenied) {
				t.Fatalf("worker credential authorized owner recovery: %v", e)
			}
			invalid := req
			invalid.ReleaseRisk = ""
			if _, e := f.a.Recover(f.owner, invalid); !errors.Is(e, ErrDenied) {
				t.Fatalf("missing reconciliation risk accepted: %v", e)
			}
			invalid = req
			invalid.Target.ExpectedFence++
			if _, e := f.a.Recover(f.owner, invalid); !errors.Is(e, ErrDenied) {
				t.Fatalf("stale target fence accepted: %v", e)
			}
			r, e := f.a.Recover(f.owner, req)
			if e != nil || r.GlobalFence != r.PreviousGlobalFence+1 {
				t.Fatalf("recovery fence receipt: %+v %v", r, e)
			}
			replay, e := f.a.Recover(f.owner, req)
			if e != nil || !reflect.DeepEqual(r, replay) {
				t.Fatalf("immutable replay: %+v %v", replay, e)
			}
			if _, e = f.a.Recover(strings.Repeat("0", 64), req); !errors.Is(e, ErrDenied) {
				t.Fatalf("forged owner replay accepted: %v", e)
			}
			invalid = req
			invalid.OperationKey = "duplicate"
			if _, e = f.a.Recover(f.owner, invalid); !errors.Is(e, ErrDenied) {
				t.Fatalf("second-key recovery accepted: %v", e)
			}
			invalid = req
			invalid.Rationale = "altered"
			if _, e = f.a.Recover(f.owner, invalid); !errors.Is(e, ErrDenied) {
				t.Fatalf("altered replay accepted: %v", e)
			}
			if e = f.a.transaction(false, false, func(s *state) error {
				if !workerTaskWithdrawn(s, f.worker.ID, i.Reservation.Reservation.TaskID) {
					t.Fatal("live old worker task authority restored")
				}
				if !reflect.DeepEqual(s.DispatchIntents[i.DispatchID], i) {
					t.Fatal("recovery changed immutable intent")
				}
				if ack {
					if !s.Leases[i.Lease.ID].Released {
						t.Fatal("matched lease retained")
					}
				} else {
					if s.Dispatches[i.DispatchID].Converted {
						t.Fatal("recovery fabricated conversion")
					}
					if reservationRetained(s, s.Dispatches[i.DispatchID]) {
						t.Fatal("recovered reservation still protects resources")
					}
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			status, e := f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
			if e != nil || status.ResourcesRetained {
				t.Fatalf("lifecycle resources retained after recovery: %+v %v", status, e)
			}
		})
	}
}
func TestNativeRecoveryTerminalRequiresExplicitOwnerAssurance(t *testing.T) {
	f, i := recoverySetup(t, true)
	req := reconcileRequest(t, f, i)
	req.Mode = "authenticated-terminal"
	req.Uncertainty = ""
	req.ReleaseRisk = ""
	req.PolicyRevision = "policy1"
	req.ObservationSequence = 1
	req.EvidenceDigest = "sha256:" + strings.Repeat("a", 64)
	if _, e := f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("no assurance policy permitted terminal recovery: %v", e)
	}
	p := RecoveryPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "policy1", Rationale: "owner explicitly accepts cooperative terminal report risk", AcceptCooperativeTerminal: true}
	if e := f.a.ConfigureRecovery(f.owner, p); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("missing terminal observation accepted: %v", e)
	}
	_, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, ObserveLifecycleRequest{OperationKey: "running", Binding: lifecycleBinding(i), HostSequence: 1, State: "running", Source: "owner-manual-native-host", EvidenceDigest: req.EvidenceDigest, SourceTime: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("authenticated running report permitted terminal recovery: %v", e)
	}
	req.ObservationSequence = 2
	_, e = f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, ObserveLifecycleRequest{OperationKey: "terminal", Binding: lifecycleBinding(i), ExpectedSequence: 1, HostSequence: 2, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: req.EvidenceDigest, SourceTime: time.Now()})
	if e != nil {
		t.Fatal(e)
	}
	disabled := p
	disabled.Revision = "disabled"
	disabled.AcceptCooperativeTerminal = false
	if e = f.a.ConfigureRecovery(f.owner, disabled); e != nil {
		t.Fatal(e)
	}
	req.PolicyRevision = "disabled"
	if _, e = f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("owner-disabled cooperative assurance accepted: %v", e)
	}
	req.PolicyRevision = "policy1"
	if _, e = f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("historical policy bypassed current assurance: %v", e)
	}
	p.Revision = "policy2"
	if e = f.a.ConfigureRecovery(f.owner, p); e != nil {
		t.Fatal(e)
	}
	req.PolicyRevision = "policy2"
	if e = f.a.Revoke(f.owner, f.bridge.ID); e != nil {
		t.Fatal(e)
	}
	// Stored evidence was authenticated at receipt; explicit current owner policy can accept it after bridge revocation.
	r, e := f.a.Recover(f.owner, req)
	if e != nil || r.Observation == nil || r.PolicyRevision != "policy2" {
		t.Fatalf("terminal recovery: %+v %v", r, e)
	}
}
func TestNativeRecoveryFailedPersistenceReturnsNoSuccess(t *testing.T) {
	f, i := recoverySetup(t, true)
	req := reconcileRequest(t, f, i)
	dir := filepath.Dir(f.a.path)
	f.a.persistOverride = func(data []byte) error {
		if e := os.Chmod(dir, 0500); e != nil {
			t.Fatal(e)
		}
		defer func() { _ = os.Chmod(dir, 0700) }()
		return f.a.persist(data)
	}
	r, e := f.a.Recover(f.owner, req)
	if !errors.Is(e, ErrStorage) || !reflect.DeepEqual(r, RecoveryReceipt{}) {
		t.Fatalf("persistence failure leaked receipt: %+v %v", r, e)
	}
	f.a.persistOverride = nil
	if _, e = f.a.OwnerRecoveryTarget(f.owner, "lease", i.Lease.ID); e != nil {
		t.Fatalf("failed write changed authority: %v", e)
	}
}

func TestNativeRecoveryRevokedExpiredAndConcurrentReplacement(t *testing.T) {
	for _, workerState := range []string{"live", "revoked", "expired"} {
		t.Run(workerState, func(t *testing.T) {
			f, i := recoverySetup(t, true)
			req := reconcileRequest(t, f, i)
			if e := f.a.transaction(false, true, func(s *state) error {
				r := s.Sessions[f.worker.ID]
				if workerState == "revoked" {
					r.Revoked = true
				}
				if workerState == "expired" {
					r.ExpiresAt = time.Now().Add(-time.Minute)
				}
				s.Sessions[r.ID] = r
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			replacement, token, e := f.a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
			if e != nil {
				t.Fatal(e)
			}
			at, e := f.a.RegisterAttempt(token, replacement.ID, AttemptRequest{RequestID: "replacement", TaskID: i.Reservation.Reservation.TaskID, Checkout: i.Reservation.Reservation.Checkout})
			if e != nil {
				t.Fatal(e)
			}
			acquisition := AcquireLeaseRequest{OperationKey: "replacement-lease", AttemptID: at.ID, Resources: i.Lease.Resources, ExpiresAt: time.Now().Add(time.Minute)}
			if _, e = f.a.AcquireLease(token, replacement.ID, acquisition); !errors.Is(e, ErrDenied) {
				t.Fatalf("unrecovered %s lease permitted replacement: %v", workerState, e)
			}
			r, e := f.a.Recover(f.owner, req)
			if e != nil {
				t.Fatal(e)
			}
			l, e := f.a.AcquireLease(token, replacement.ID, acquisition)
			if e != nil || l.Fence <= r.GlobalFence {
				t.Fatalf("separate replacement failed after recovery: %+v %v", l, e)
			}
			// A historical recovery never releases replacement protection, including after restart.
			reopened, e := Open(f.a.path, f.a.binding, f.a.admission)
			if e != nil {
				t.Fatal(e)
			}
			replay, e := reopened.Recover(f.owner, req)
			if e != nil || !reflect.DeepEqual(r, replay) {
				t.Fatalf("restart recovery replay %+v %v", replay, e)
			}
			if e = reopened.transaction(false, false, func(s *state) error {
				if s.Leases[l.ID].Released {
					t.Fatal("replay released reassigned lease")
				}
				if workerTaskWithdrawn(s, replacement.ID, at.Request.TaskID) {
					t.Fatal("replacement inherited old withdrawal")
				}
				if workerTaskWithdrawn(s, f.worker.ID, "task-b") {
					t.Fatal("unrelated old task withdrawn")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			if workerState == "live" {
				if _, e = reopened.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "old-new-attempt", TaskID: at.Request.TaskID, Checkout: at.Request.Checkout}); !errors.Is(e, ErrDenied) {
					t.Fatalf("live oldworker recreated recovered attempt: %v", e)
				}
			}
		})
	}
}
func TestNativeRecoveryStandaloneLeaseAndLegacyConvertedForms(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "legacy-converted"}[legacy], func(t *testing.T) {
			f, i := recoverySetup(t, true)
			if e := f.a.transaction(false, true, func(s *state) error {
				delete(s.DispatchIntents, i.DispatchID)
				if !legacy {
					delete(s.Dispatches, i.DispatchID)
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			target, e := f.a.OwnerRecoveryTarget(f.owner, "lease", i.Lease.ID)
			if e != nil || target.IntentID != "" {
				t.Fatalf("standalone/legacy target %+v %v", target, e)
			}
			if !legacy && target.DispatchID != "" {
				t.Fatal("standalone target invented dispatch")
			}
			req := RecoveryRequest{OperationKey: "recover", Target: target, Mode: "owner-reconciliation", Rationale: "owner reconciles historical lease", Uncertainty: "no host lifecycle evidence", ReleaseRisk: "possible oldwriter remains"}
			if _, e = f.a.Recover(f.owner, req); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestNativeRecoveryReservedBeforeIntentAndConcurrentKeys(t *testing.T) {
	f, i := recoverySetup(t, false)
	if e := f.a.transaction(false, true, func(s *state) error { delete(s.DispatchIntents, i.DispatchID); return nil }); e != nil {
		t.Fatal(e)
	}
	target, e := f.a.OwnerRecoveryTarget(f.owner, "reservation", i.DispatchID)
	if e != nil || target.IntentID != "" || target.SessionID != "" {
		t.Fatalf("preintent target %+v %v", target, e)
	}
	req := RecoveryRequest{OperationKey: "recover-a", Target: target, Mode: "owner-reconciliation", Rationale: "unused reservation reconciled", Uncertainty: "host action not observed", ReleaseRisk: "owner accepts unknown action risk"}
	replies := make(chan error, 2)
	for _, key := range []string{"recover-a", "recover-b"} {
		go func(key string) { r := req; r.OperationKey = key; _, e := f.a.Recover(f.owner, r); replies <- e }(key)
	}
	successes, denials := 0, 0
	for n := 0; n < 2; n++ {
		e := <-replies
		if e == nil {
			successes++
		} else if errors.Is(e, ErrDenied) {
			denials++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || denials != 1 {
		t.Fatalf("concurrent keys recovered twice: success%d denied%d", successes, denials)
	}
	if _, e = f.a.AdmitDispatch(f.token, f.s.ID, AdmitDispatchRequest{DispatchID: i.DispatchID, ExpectedFence: target.ExpectedFence}); !errors.Is(e, ErrDenied) {
		t.Fatalf("recovered preintent dispatch admitted: %v", e)
	}
}

func TestNativeRecoveryWithdrawalDescendantAndUnrelatedTask(t *testing.T) {
	f, i := recoverySetup(t, true)
	child, ct, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(10*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.Recover(f.owner, reconcileRequest(t, f, i)); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.RegisterAttempt(ct, child.ID, AttemptRequest{RequestID: "new-child", TaskID: i.Reservation.Reservation.TaskID, Checkout: i.Reservation.Reservation.Checkout}); !errors.Is(e, ErrDenied) {
		t.Fatalf("descendant reacquired recovered task: %v", e)
	}
	if _, e = f.a.RegisterAttempt(f.workerToken, f.worker.ID, AttemptRequest{RequestID: "unrelated", TaskID: "task-b", Checkout: f.dirs[1]}); e != nil {
		t.Fatalf("recovery withdrew unrelated task: %v", e)
	}
}
func TestNativeRecoveryLateObservationAndFenceOverflow(t *testing.T) {
	f, i := recoverySetup(t, true)
	req := reconcileRequest(t, f, i)
	if e := f.a.transaction(false, true, func(s *state) error { s.Fence = ^uint64(0); return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.Recover(f.owner, req); !errors.Is(e, ErrDenied) {
		t.Fatalf("overflow recovery accepted: %v", e)
	}
	if e := f.a.transaction(false, true, func(s *state) error { s.Fence = i.Lease.Fence; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.Recover(f.owner, req); e != nil {
		t.Fatal(e)
	}
	if e := f.a.Revoke(f.owner, f.worker.ID); e != nil {
		t.Fatal(e)
	}
	obs, e := f.a.ObserveLifecycle(f.bridgeToken, f.bridge.ID, ObserveLifecycleRequest{OperationKey: "late", Binding: lifecycleBinding(i), HostSequence: 1, State: "completed", Source: "owner-manual-native-host", EvidenceDigest: "sha256:" + strings.Repeat("a", 64), SourceTime: time.Now()})
	if e != nil || obs.State != "completed" {
		t.Fatalf("late recovered observation lost: %+v %v", obs, e)
	}
	status, e := f.a.LifecycleStatus(f.bridgeToken, f.bridge.ID, i.DispatchID)
	if e != nil || status.ResourcesRetained {
		t.Fatalf("late completion restored retained protection: %+v %v", status, e)
	}
}

func TestNativeRecoverySchema10PresenceAndOriginalHistory(t *testing.T) {
	f, i := lifecycleSetup(t, true)
	before := mustReadLifecycle(t, f.a.path)
	if e := f.a.UpgradeRecovery(f.owner); e != nil {
		t.Fatal(e)
	}
	after := mustReadLifecycle(t, f.a.path)
	var old, updated map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &updated) != nil {
		t.Fatal("decode")
	}
	for k, v := range old {
		if k != "Version" && k != "Recoveries" && k != "RecoveryPolicies" && k != "RecoveryPolicyHistory" && k != "RecoveryOperations" && !bytes.Equal(v, updated[k]) {
			t.Fatalf("recovery migration changed historical field%s", k)
		}
	}
	if !reflect.DeepEqual(i, fIntent(t, f)) {
		t.Fatal("upgrade changed intent")
	}
	for _, field := range []string{"Recoveries", "RecoveryOperations", "RecoveryPolicies", "RecoveryPolicyHistory"} {
		changed := map[string]json.RawMessage{}
		for k, v := range updated {
			changed[k] = v
		}
		delete(changed, field)
		data, _ := json.Marshal(changed)
		if e := os.WriteFile(f.a.path, data, 0600); e != nil {
			t.Fatal(e)
		}
		if e := f.a.AuthenticateOwner(f.owner); !errors.Is(e, ErrStorage) {
			t.Fatalf("schema10 missing%s accepted: %v", field, e)
		}
	}
	if e := os.WriteFile(f.a.path, after, 0600); e != nil {
		t.Fatal(e)
	}
}
func fIntent(t *testing.T, f dispatchFixture) DispatchIntent {
	t.Helper()
	var i DispatchIntent
	if e := f.a.transaction(false, false, func(s *state) error { i = s.DispatchIntents[f.d.ID]; return nil }); e != nil {
		t.Fatal(e)
	}
	return i
}
func TestNativeRecoveryFreshAcknowledgementRefusesRecoveredReservation(t *testing.T) {
	f, i := recoverySetup(t, false)
	if _, e := f.a.Recover(f.owner, reconcileRequest(t, f, i)); e != nil {
		t.Fatal(e)
	}
	if _, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i)); !errors.Is(e, ErrDenied) {
		t.Fatalf("recovered reservation received fresh worker lease: %v", e)
	}
}

func TestNativeRecoveryDelegatedStandaloneWithdrawsEnrolledWorkerFamily(t *testing.T) {
	f, i := recoverySetup(t, true)
	if _, e := f.a.ReleaseLease(f.workerToken, f.worker.ID, ReleaseLeaseRequest{OperationKey: "release-initial", AttemptID: f.at.ID, LeaseID: i.Lease.ID, ExpectedFence: i.Lease.Fence}); e != nil {
		t.Fatal(e)
	}
	child, ct, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(10*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	sibling, st, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(10*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.a.RegisterAttempt(ct, child.ID, AttemptRequest{RequestID: "child-standalone", TaskID: i.Lease.TaskID, Checkout: i.Lease.Checkout})
	if e != nil {
		t.Fatal(e)
	}
	l, e := f.a.AcquireLease(ct, child.ID, AcquireLeaseRequest{OperationKey: "standalone-child", AttemptID: at.ID, Resources: i.Lease.Resources, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	target, e := f.a.OwnerRecoveryTarget(f.owner, "lease", l.ID)
	if e != nil || target.SessionID != child.ID || target.WorkerFamilyID != f.worker.ID {
		t.Fatalf("exact child provenance lost: %+v %v", target, e)
	}
	req := RecoveryRequest{OperationKey: "recover-child", Target: target, Mode: "owner-reconciliation", Rationale: "owner retires old enrolled worker", Uncertainty: "all subprocesses unverified", ReleaseRisk: "stale writer risk"}
	if _, e = f.a.Recover(f.owner, req); e != nil {
		t.Fatal(e)
	}
	fresh, ft, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(5*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	for _, actor := range []struct{ id, token string }{{f.worker.ID, f.workerToken}, {sibling.ID, st}, {child.ID, ct}, {fresh.ID, ft}} {
		if _, e = f.a.RegisterAttempt(actor.token, actor.id, AttemptRequest{RequestID: "family-" + actor.id, TaskID: l.TaskID, Checkout: l.Checkout}); !errors.Is(e, ErrDenied) {
			t.Fatalf("recovered delegated lease family reacquired through actor%s: %v", actor.id, e)
		}
	}
}

func TestNativeRecoveryPolicyHistoryAndStructuralFamilyRefusal(t *testing.T) {
	f, _ := recoverySetup(t, true)
	p := RecoveryPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "a", Rationale: "initial explicit owner assurance", AcceptCooperativeTerminal: true}
	if e := f.a.ConfigureRecovery(f.owner, p); e != nil {
		t.Fatal(e)
	}
	next := p
	next.Revision = "b"
	next.AcceptCooperativeTerminal = false
	if e := f.a.ConfigureRecovery(f.owner, next); e != nil {
		t.Fatal(e)
	}
	altered := p
	altered.Rationale = "changedhistorical"
	if e := f.a.ConfigureRecovery(f.owner, altered); !errors.Is(e, ErrDenied) {
		t.Fatalf("historical recovery policy contents altered: %v", e)
	}
	if e := f.a.ConfigureRecovery(f.owner, p); e != nil {
		t.Fatal(e)
	}
	if e := f.a.transaction(false, false, func(s *state) error {
		if !reflect.DeepEqual(s.RecoveryPolicies["project"], next) {
			t.Fatal("historical replay reset current recovery policy")
		}
		for _, id := range []string{"absent", "cycle"} {
			s.Sessions["cycle"] = record{Session: Session{ID: "cycle", Parent: "cycle"}}
			if _, e := historicalWorkerFamily(s, id); !errors.Is(e, ErrDenied) {
				t.Fatalf("invalid historical family accepted: %s %v", id, e)
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestNativeRecoveryConcurrentIndependentReassignmentPreservesWinner(t *testing.T) {
	f, i := recoverySetup(t, true)
	req := reconcileRequest(t, f, i)
	recovered, e := f.a.Recover(f.owner, req)
	if e != nil {
		t.Fatal(e)
	}
	type acquisition struct {
		session Session
		token   string
		request AcquireLeaseRequest
	}
	actors := []acquisition{}
	for n := 0; n < 2; n++ {
		actor, token, e := f.a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		key := fmt.Sprintf("replacement-%d", n)
		at, e := f.a.RegisterAttempt(token, actor.ID, AttemptRequest{RequestID: key, TaskID: i.Lease.TaskID, Checkout: i.Lease.Checkout})
		if e != nil {
			t.Fatal(e)
		}
		actors = append(actors, acquisition{actor, token, AcquireLeaseRequest{OperationKey: key, AttemptID: at.ID, Resources: i.Lease.Resources, ExpiresAt: time.Now().Add(time.Minute)}})
	}
	type reply struct {
		lease   Lease
		err     error
		session string
	}
	replies := make(chan reply, 2)
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	for _, actor := range actors {
		go func(actor acquisition) {
			ready <- struct{}{}
			<-start
			l, e := f.a.AcquireLease(actor.token, actor.session.ID, actor.request)
			replies <- reply{l, e, actor.session.ID}
		}(actor)
	}
	<-ready
	<-ready
	close(start)
	successes, denials := 0, 0
	var winner Lease
	for n := 0; n < 2; n++ {
		r := <-replies
		if r.err == nil {
			successes++
			winner = r.lease
			if winner.SessionID != r.session || winner.Fence <= recovered.GlobalFence {
				t.Fatalf("replacement winner identity/fence: %+v %+v", winner, recovered)
			}
		} else if errors.Is(r.err, ErrDenied) {
			denials++
		} else {
			t.Fatal(r.err)
		}
	}
	if successes != 1 || denials != 1 {
		t.Fatalf("concurrent replacements violated exclusion: successes%d denials%d", successes, denials)
	}
	replay, e := f.a.Recover(f.owner, req)
	if e != nil || !reflect.DeepEqual(recovered, replay) {
		t.Fatalf("historical recovery replay changed: %+v %v", replay, e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		if s.Leases[winner.ID].Released || !reflect.DeepEqual(s.Leases[winner.ID], winner) {
			t.Fatal("old recovery replay released or changed replacement winner")
		}
		if workerTaskWithdrawn(s, winner.SessionID, winner.TaskID) {
			t.Fatal("independent winner inherited old worker-family withdrawal")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestNativeRecoveryPreIntentPlannerAndLeaseConsumers(t *testing.T) {
	f, i := recoverySetup(t, false)
	if e := f.a.transaction(false, true, func(s *state) error { delete(s.DispatchIntents, i.DispatchID); return nil }); e != nil {
		t.Fatal(e)
	}
	req := reconcileRequest(t, f, i)
	r, e := f.a.Recover(f.owner, req)
	if e != nil {
		t.Fatal(e)
	}
	plan := swarmPlan(t, f.swarmFixture)
	selected := false
	for _, v := range plan.Verdicts {
		if v.TaskID == req.Target.TaskID && v.Selected {
			selected = true
		}
	}
	if !selected {
		t.Fatal("recovered preintent reservation still blocks planner selection")
	}
	replacement, token, e := f.a.Enroll(f.owner, f.worker.Scope, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	at, e := f.a.RegisterAttempt(token, replacement.ID, AttemptRequest{RequestID: "preintent-replacement", TaskID: req.Target.TaskID, Checkout: req.Target.Checkout})
	if e != nil {
		t.Fatal(e)
	}
	lease, e := f.a.AcquireLease(token, replacement.ID, AcquireLeaseRequest{OperationKey: "preintent-lease", AttemptID: at.ID, Resources: i.Reservation.Reservation.Resources, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil || lease.Fence <= r.GlobalFence {
		t.Fatalf("recovered preintent reservation still blocks replacement lease: %+v %v", lease, e)
	}
}

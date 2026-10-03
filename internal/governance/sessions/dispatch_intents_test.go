package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type dispatchAdmission struct {
	*swarmFixtureAdmission
	calls, withdraw int
	hook            func()
}

func (a *dispatchAdmission) AdmitDispatchCapability(w, p, h, d string) error {
	a.calls++
	if a.hook != nil {
		a.hook()
	}
	if a.withdraw > 0 && a.calls >= a.withdraw {
		return ErrDenied
	}
	return a.AdmitSwarmCapability(w, p, h, d)
}

type dispatchFixture struct {
	swarmFixture
	bridge, worker           Session
	bridgeToken, workerToken string
	at                       Attempt
	d                        DispatchReservation
	req                      RequestDispatchIntent
	observer                 *dispatchAdmission
}

func dispatchSetup(t *testing.T) dispatchFixture {
	t.Helper()
	f := swarmSetup(t)
	w := swarmReserve(t, f, "wave", swarmPlan(t, f))
	d := w.Dispatches[0]
	if e := f.a.UpgradeDispatchIntents(f.owner); e != nil {
		t.Fatal(e)
	}
	observer := &dispatchAdmission{swarmFixtureAdmission: f.ad}
	f.a.admission = observer
	resources := []string{"project:project"}
	for _, r := range d.Reservation.Resources {
		resources = append(resources, r.Kind+":"+r.Path)
	}
	bridge, bt, e := f.a.Enroll(f.owner, Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: resources}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	worker, wt, at := leaseAttempt(t, f.a, f.owner, "task-a", "expected-worker", f.dirs[0])
	req := RequestDispatchIntent{OperationKey: "intent", DispatchID: d.ID, BridgeSessionID: bridge.ID, WorkerSessionID: worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence}
	return dispatchFixture{f, bridge, worker, bt, wt, at, d, req, observer}
}
func requestIntent(t *testing.T, f dispatchFixture) DispatchIntentResult {
	t.Helper()
	r, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
	if e != nil || !r.HostActionAuthorized {
		t.Fatalf("initial durable permit: %+v %v", r, e)
	}
	return r
}
func correlation(i DispatchIntent, key, state string) CorrelateDispatchIntent {
	h := ""
	if state == "host_correlated" {
		h = "/root/native_dispatch_worker"
	}
	return CorrelateDispatchIntent{OperationKey: key, DispatchID: i.DispatchID, IntentID: i.ID, HostAgentID: h, State: state, ExpectedFence: i.Reservation.Reservation.Fence, ExpectedSequence: i.Sequence}
}
func acknowledgement(f dispatchFixture, i DispatchIntent) AcknowledgeDispatchIntent {
	return AcknowledgeDispatchIntent{OperationKey: "ack", DispatchID: i.DispatchID, IntentID: i.ID, AttemptID: f.at.ID, HostAgentID: i.HostAgentID, ExpectedFence: f.d.Reservation.Fence, ExpectedSequence: i.Sequence}
}
func TestNativeDispatchDurableIntentAndSingleLaunchReplay(t *testing.T) {
	f := dispatchSetup(t)
	var wg sync.WaitGroup
	permits := make(chan DispatchIntentResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
			permits <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(permits)
	close(errs)
	count := 0
	for e := range errs {
		if e != nil && !errors.Is(e, ErrStorage) {
			t.Fatal(e)
		}
	}
	for r := range permits {
		if r.HostActionAuthorized {
			count++
			if e := f.a.transaction(false, false, func(s *state) error {
				if s.DispatchIntents[f.d.ID].ID != r.Intent.ID || s.DispatchIntentOperations[f.req.OperationKey].Intent.ID != r.Intent.ID {
					t.Fatal("permit before durable intent")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if count != 1 {
		t.Fatalf("launch permits=%d", count)
	}
	replay, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
	if e != nil || replay.HostActionAuthorized {
		t.Fatalf("replay authorized another launch: %+v %v", replay, e)
	}
	changed := f.req
	changed.OperationKey = "second-launch"
	if _, e = f.a.RequestDispatchIntent(f.token, f.s.ID, changed); !errors.Is(e, ErrDenied) {
		t.Fatal("reservation launched under second key", e)
	}
	changed = f.req
	changed.PromptSpecDigest = "changed"
	if _, e = f.a.RequestDispatchIntent(f.token, f.s.ID, changed); !errors.Is(e, ErrDenied) {
		t.Fatal("altered replay", e)
	}
}
func TestNativeDispatchSeparateCredentialsAtomicAcknowledgementAndNoBypass(t *testing.T) {
	f := dispatchSetup(t)
	r := requestIntent(t, f)
	if _, e := f.a.HandoffDispatch(f.workerToken, f.worker.ID, HandoffDispatchRequest{"bypass", f.d.ID, f.at.ID, f.d.Reservation.Fence}); !errors.Is(e, ErrDenied) {
		t.Fatal("raw handoff bypassed intent", e)
	}
	cr := correlation(r.Intent, "correlation", "host_correlated")
	if _, e := f.a.CorrelateDispatchIntent(f.token, f.s.ID, cr); !errors.Is(e, ErrDenied) {
		t.Fatal("controller authenticated bridge", e)
	}
	i, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, cr)
	if e != nil || i.HostAgentID != "/root/native_dispatch_worker" || i.Sequence != 2 {
		t.Fatalf("native opaque host identity: %+v %v", i, e)
	}
	ack := acknowledgement(f, i)
	if _, e := f.a.AcknowledgeDispatchIntent(f.bridgeToken, f.bridge.ID, ack); !errors.Is(e, ErrDenied) {
		t.Fatal("bridge authenticated worker", e)
	}
	other, ot, oa := leaseAttempt(t, f.a, f.owner, "task-a", "different-worker", f.dirs[0])
	changed := ack
	changed.AttemptID = oa.ID
	if _, e := f.a.AcknowledgeDispatchIntent(ot, other.ID, changed); !errors.Is(e, ErrDenied) {
		t.Fatal("unassigned worker accepted", e)
	}
	i, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, ack)
	if e != nil || i.State != "acknowledged" || i.Lease == nil || i.Lease.SessionID != f.worker.ID || i.Sequence != 3 {
		t.Fatalf("ack: %+v %v", i, e)
	}
	if e = f.a.transaction(false, false, func(s *state) error {
		d := s.Dispatches[f.d.ID]
		if !d.Converted || d.WorkerSessionID != f.worker.ID || !reflect.DeepEqual(s.Leases[i.Lease.ID], *i.Lease) || s.DispatchIntents[f.d.ID].State != "acknowledged" {
			t.Fatal("ack and lease did not commit atomically")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.observer.withdraw = 1
	history, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, ack)
	if e != nil || !reflect.DeepEqual(history, i) {
		t.Fatal("ack history replay", e)
	}
	changed = ack
	changed.HostAgentID = "/root/forged"
	if _, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, changed); !errors.Is(e, ErrDenied) {
		t.Fatal("changed ack replay", e)
	}
	if _, e = f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(i, "regress", "dispatch_unknown")); !errors.Is(e, ErrDenied) {
		t.Fatal("ack state regressed", e)
	}
}
func TestNativeDispatchUnknownAndManualReconcileWithoutRedispatch(t *testing.T) {
	for _, state := range []string{"dispatch_unknown", "manual_handoff_required"} {
		t.Run(state, func(t *testing.T) {
			f := dispatchSetup(t)
			r := requestIntent(t, f)
			cr := correlation(r.Intent, "lost", state)
			i, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, cr)
			if e != nil {
				t.Fatal(e)
			}
			replay, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
			if e != nil || replay.HostActionAuthorized {
				t.Fatal("unknown replay launched", e)
			}
			if _, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i)); !errors.Is(e, ErrDenied) {
				t.Fatal("unknown worker acknowledged before correlation", e)
			}
			other, ot, oa := leaseAttempt(t, f.a, f.owner, "task-b", "unknown-writer", f.dirs[1])
			if _, e = f.a.AcquireLease(ot, other.ID, acquireReq(oa, "unknown-collision", "src/a")); !errors.Is(e, ErrDenied) {
				t.Fatal("unknown reservation lost protection", e)
			}
			recovered := correlation(i, "located", "host_correlated")
			recovered.ExpectedSequence--
			if _, e = f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, recovered); !errors.Is(e, ErrDenied) {
				t.Fatal("stale observation sequence", e)
			}
			recovered.ExpectedSequence++
			i, e = f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, recovered)
			if e != nil || i.Sequence != 3 {
				t.Fatal("reconcile original execution", e)
			}
			if _, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i)); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestNativeDispatchFreshCapabilityAndOwnerBindings(t *testing.T) {
	for _, mode := range []string{"request-withdraw", "ack-withdraw", "delegated-worker", "revoked-worker"} {
		t.Run(mode, func(t *testing.T) {
			f := dispatchSetup(t)
			if mode == "delegated-worker" {
				child, _, e := f.a.Delegate(f.workerToken, f.worker.ID, f.worker.Scope, time.Now().Add(20*time.Minute))
				if e != nil {
					t.Fatal(e)
				}
				f.req.WorkerSessionID = child.ID
			}
			if mode == "revoked-worker" {
				if e := f.a.Revoke(f.owner, f.worker.ID); e != nil {
					t.Fatal(e)
				}
			}
			if mode != "ack-withdraw" {
				if mode == "request-withdraw" {
					f.observer.withdraw = 2
				}
				r, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
				if !errors.Is(e, ErrDenied) || r.HostActionAuthorized {
					t.Fatal("fresh/owner binding refusal", r, e)
				}
				return
			}
			r := requestIntent(t, f)
			i, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(r.Intent, "correlated", "host_correlated"))
			if e != nil {
				t.Fatal(e)
			}
			f.observer.withdraw = f.observer.calls + 2
			if _, e = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i)); !errors.Is(e, ErrDenied) {
				t.Fatal("late withdrawn capability", e)
			}
			if e = f.a.transaction(false, false, func(s *state) error {
				if s.Dispatches[f.d.ID].Converted || s.DispatchIntents[f.d.ID].State != "host_correlated" {
					t.Fatal("failed ack partially persisted")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestNativeDispatchSchemaMigrationPreservesAndStrictReopen(t *testing.T) {
	f := swarmSetup(t)
	swarmReserve(t, f, "history", swarmPlan(t, f))
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.a.UpgradeDispatchIntents("forged"); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
	if e = f.a.UpgradeDispatchIntents(f.owner); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	var old, new map[string]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &new) != nil {
		t.Fatal("decode")
	}
	for k, v := range old {
		if k == "Version" || k == "DispatchIntents" || k == "DispatchIntentOperations" {
			continue
		}
		if !reflect.DeepEqual(v, new[k]) {
			t.Fatalf("migration changed %s", k)
		}
	}
	for _, version := range []int{6, 7, 8} {
		a := *f.a
		a.version = version
		if _, e = a.Authenticate(f.token, f.s.ID); e != nil {
			t.Fatalf("current%d factory rejects8: %v", version, e)
		}
	}
	delete(new, "DispatchIntents")
	data, _ := json.Marshal(new)
	if e = os.WriteFile(f.a.path, data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.Authenticate(f.token, f.s.ID); !errors.Is(e, ErrStorage) {
		t.Fatal("missing8 map normalized", e)
	}
}
func TestNativeDispatchPersistenceFailureNeverReturnsPermit(t *testing.T) {
	f := dispatchSetup(t)
	dir := filepath.Dir(f.a.path)
	f.observer.hook = func() {
		if f.observer.calls == 2 {
			if e := os.Chmod(dir, 0500); e != nil {
				t.Fatal(e)
			}
		}
	}
	defer func() { _ = os.Chmod(dir, 0700) }()
	result, e := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
	if e == nil {
		t.Fatal("storage failure fixture unexpectedly writable")
	}
	if result.HostActionAuthorized || result.Intent.ID != "" {
		t.Fatalf("uncommitted host permit leaked: %+v %v", result, e)
	}
}

func TestNativeDispatchAcknowledgementStorageFailureReturnsNoLease(t *testing.T) {
	f := dispatchSetup(t)
	r := requestIntent(t, f)
	i, e := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(r.Intent, "host", "host_correlated"))
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Dir(f.a.path)
	target := f.observer.calls + 2
	f.observer.hook = func() {
		if f.observer.calls == target {
			if e := os.Chmod(dir, 0500); e != nil {
				t.Fatal(e)
			}
		}
	}
	defer func() { _ = os.Chmod(dir, 0700) }()
	result, e := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, i))
	if e == nil {
		t.Fatal("ack storage failure unexpectedly writable")
	}
	if result.Lease != nil || result.ID != "" {
		t.Fatalf("uncommitted acknowledgement leaked: %+v %v", result, e)
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed write altered durable authority", e)
	}
}
func TestNativeDispatchOpaqueHostIdentifiers(t *testing.T) {
	for _, id := range []string{"/root/native_worker", "iab:child/42"} {
		if !validHostAgentID(id) {
			t.Fatalf("canonical host id rejected: %q", id)
		}
	}
	for _, id := range []string{"", "/root/child\n", "bad id", string([]byte{0}), strings.Repeat("x", 1025)} {
		if validHostAgentID(id) {
			t.Fatalf("unsafe host id accepted: %q", id)
		}
	}
}

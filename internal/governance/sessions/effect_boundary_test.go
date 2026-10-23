package sessions

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func ownerNewPin() []RoleRegistryProvenance {
	return []RoleRegistryProvenance{{RoleID: "builder", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("a", 64), PublisherKeyID: "publisher", LifecycleDigest: "sha256:" + strings.Repeat("b", 64), LifecycleExpiresAt: time.Now().Add(time.Hour), LifecycleGeneration: 1, PinGeneration: 1, ProjectGeneration: 1}}
}

func TestRootFirstPinDuringFinalDispatchCallbackRefusesHostPermit(t *testing.T) {
	f := dispatchSetup(t)
	f.observer.hook = func() {
		if f.observer.calls == 2 {
			f.ad.snapshot.RoleRegistry = ownerNewPin()
		}
	}
	result, err := f.a.RequestDispatchIntent(f.token, f.s.ID, f.req)
	if !errors.Is(err, ErrDenied) || result.HostActionAuthorized {
		t.Fatalf("late first role pin still authorized native host dispatch: permit=%v err=%v", result.HostActionAuthorized, err)
	}
}

func TestRootFirstPinDuringFinalAcknowledgementCallbackRefusesLease(t *testing.T) {
	f := dispatchSetup(t)
	intent := requestIntent(t, f).Intent
	correlated, err := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, correlation(intent, "root-correlation", "host_correlated"))
	if err != nil {
		t.Fatal(err)
	}
	threshold := f.observer.calls + 2
	f.observer.hook = func() {
		if f.observer.calls == threshold {
			f.ad.snapshot.RoleRegistry = ownerNewPin()
		}
	}
	result, err := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, acknowledgement(f, correlated))
	if !errors.Is(err, ErrDenied) || result.Lease != nil {
		t.Fatalf("late first role pin still acknowledged native worker lease: lease=%v err=%v", result.Lease != nil, err)
	}
}

type ownerHandoffAdmission struct {
	*swarmFixtureAdmission
	calls int
}

func (a *ownerHandoffAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	a.calls++
	if a.calls == 2 {
		a.snapshot.RoleRegistry = ownerNewPin()
	}
	return a.swarmFixtureAdmission.AdmitSwarmCapability(w, p, h, d)
}

func TestRootFirstPinDuringFinalDirectHandoffCallbackRefusesLease(t *testing.T) {
	f := swarmSetup(t)
	wave := swarmReserve(t, f, "root-wave", swarmPlan(t, f))
	dispatch := wave.Dispatches[0]
	worker, token, attempt := leaseAttempt(t, f.a, f.owner, "task-a", "root-worker", f.dirs[0])
	f.a.admission = &ownerHandoffAdmission{swarmFixtureAdmission: f.ad}
	result, err := f.a.HandoffDispatch(token, worker.ID, HandoffDispatchRequest{OperationKey: "root-handoff", DispatchID: dispatch.ID, AttemptID: attempt.ID, ExpectedFence: dispatch.Reservation.Fence})
	if !errors.Is(err, ErrDenied) || result.ID != "" {
		t.Fatalf("late first role pin still handed off native lease: lease=%v err=%v", result.ID != "", err)
	}
}

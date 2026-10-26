package executor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
)

type testAdmission struct{ snapshot sessions.SwarmSnapshot }

func (a *testAdmission) AdmitLease(string, string, string) error { return nil }
func (a *testAdmission) ObserveSwarm(string, string) (sessions.SwarmSnapshot, error) {
	return a.snapshot, nil
}
func (a *testAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	if w == "workspace" && p == "project" && h == "codex" && d == "cap-v1" {
		return nil
	}
	return sessions.ErrDenied
}
func (a *testAdmission) AdmitDispatchCapability(w, p, h, d string) error {
	return a.AdmitSwarmCapability(w, p, h, d)
}

type authorityFixture struct {
	a                      *sessions.Authority
	owner, controllerToken string
	controller             sessions.Session
	dispatch               sessions.DispatchReservation
	worker                 sessions.Session
	workerToken            string
	attempt                sessions.Attempt
	bridge                 sessions.Session
	bridgeToken            string
}

func makeAuthorityFixture(t *testing.T) authorityFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := checkout.Observe(repo)
	if err != nil {
		t.Fatal(err)
	}
	a, err := sessions.Open(filepath.Join(root, "authority.json"), sessions.Binding{Workspace: "workspace", Installation: "installation"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.BindTask(owner, sessions.TaskBinding{TaskID: "task-a", ProjectID: "project", Repository: obs.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []sessions.TaskBinding{{TaskID: "task-b", ProjectID: "project", Repository: obs.Repository, AdmissionEnabled: true}, {TaskID: "task-c", ProjectID: "other-project", Repository: obs.Repository, AdmissionEnabled: true}} {
		if err = a.BindTask(owner, binding); err != nil {
			t.Fatal(err)
		}
	}
	ad := &testAdmission{snapshot: sessions.SwarmSnapshot{Digest: "snapshot-v1", Candidates: []sessions.SwarmCandidate{{TaskID: "task-a", ProjectID: "project", Status: "open", Reason: "ready", Resources: []sessions.Resource{{Kind: "path", Path: "src/a"}}}}}}
	if err = a.UpgradeSwarms(owner); err != nil {
		t.Fatal(err)
	}
	a, err = sessions.OpenSwarm(filepath.Join(root, "authority.json"), sessions.Binding{Workspace: "workspace", Installation: "installation"}, ad)
	if err != nil {
		t.Fatal(err)
	}
	policy := sessions.SwarmPolicy{ProjectID: "project", Revision: "policy-v1", Harness: "codex", CapabilityDigest: "cap-v1", Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implement", Kind: "implement", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{"task-a": {Checkout: repo, ImplementKind: "implement", ReviewKind: "review"}}}
	if err = a.ConfigureSwarm(owner, policy); err != nil {
		t.Fatalf("configure swarm: %v", err)
	}
	resources := []string{"project:project", "project:other-project", "task:task-a", "task:task-b", "task:task-c", "path:src/a"}
	controller, ct, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "swarm.plan", "swarm.reserve", "swarm.dispatch", "dispatch.cancel"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("enroll controller: %v", err)
	}
	plan, err := a.PlanSwarm(ct, controller.ID, "project")
	if err != nil {
		t.Fatalf("plan wave: %v", err)
	}
	wave, err := a.ReserveWave(ct, controller.ID, sessions.ReserveWaveRequest{OperationKey: "wave-1", Plan: plan, ExpiresAt: time.Now().Add(30 * time.Minute)})
	if err != nil {
		t.Fatalf("reserve wave: %v", err)
	}
	if err = a.UpgradeDispatchIntents(owner); err != nil {
		t.Fatalf("upgrade dispatch intents: %v", err)
	}
	d := wave.Dispatches[0]
	bridge, bt, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	worker, wt, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit"}, Resources: append(resources, "checkout:")}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := a.RegisterAttempt(wt, worker.ID, sessions.AttemptRequest{RequestID: "worker-attempt", TaskID: "task-a", Checkout: repo})
	if err != nil {
		t.Fatal(err)
	}
	return authorityFixture{a: a, owner: owner, controllerToken: ct, controller: controller, dispatch: d, worker: worker, workerToken: wt, attempt: attempt, bridge: bridge, bridgeToken: bt}
}

type fakeStore struct {
	byKey         map[string]HostResult
	digests       map[string]string
	observed      map[string]Observation
	dispatches    int
	dispatchCalls int
	cancelCalls   int
	registerCalls int
	cancel        string
	loseNext      bool
}
type fakeHost struct {
	store              *fakeStore
	enrolled           Binding
	capabilityOverride *Capabilities
}

func (f *fakeHost) Register(r Registration) error {
	f.store.registerCalls++
	return validateRegistration(f.enrolled, r, time.Now())
}
func (f *fakeHost) Capabilities() Capabilities {
	if f.capabilityOverride != nil {
		return *f.capabilityOverride
	}
	return Capabilities{Version: 1, Host: f.enrolled.Host, Model: Capability{State: Unqualified}, Dispatch: Capability{State: Supported}, Lookup: Capability{State: Supported}, PrivateProfileHandoff: Capability{State: Supported}, Observe: Capability{State: Supported}, Cancel: Capability{State: Supported}}
}
func (f *fakeHost) Dispatch(d Dispatch) (HostResult, error) {
	f.store.dispatchCalls++
	if d.Version != 1 || d.OperationKey == "" || ValidateBinding(f.enrolled, d.Binding, time.Now()) != nil || ValidateProfile(d.Profile, d.Binding, time.Now()) != nil {
		return HostResult{}, ErrDenied
	}
	if old, ok := f.store.byKey[d.OperationKey]; ok {
		if f.store.digests[d.OperationKey] != DispatchSpecDigest(d) {
			return HostResult{}, ErrDenied
		}
		return old, nil
	}
	f.store.dispatches++
	r := HostResult{Version: 1, OperationKey: d.OperationKey, HostExecutionID: "exec-1", State: "accepted", Binding: d.Binding}
	f.store.byKey[d.OperationKey] = r
	f.store.digests[d.OperationKey] = DispatchSpecDigest(d)
	if f.store.loseNext {
		f.store.loseNext = false
		return HostResult{}, errors.New("simulated lost response")
	}
	return r, nil
}
func (f *fakeHost) Lookup(key string, b Binding) (HostResult, error) {
	if ValidateBinding(f.enrolled, b, time.Now()) != nil {
		return HostResult{}, ErrDenied
	}
	r, ok := f.store.byKey[key]
	if !ok {
		return HostResult{}, errors.New("unknown")
	}
	return r, nil
}
func (f *fakeHost) Observe(o Observation) error {
	if ValidateBinding(f.enrolled, o.Binding, time.Now()) != nil {
		return ErrDenied
	}
	old, ok := f.store.observed[o.Binding.Attempt]
	var prev string
	var seq uint64
	if ok {
		prev, seq = old.State, old.Sequence
	}
	if ValidateObservation(prev, seq, o) != nil {
		return ErrDenied
	}
	f.store.observed[o.Binding.Attempt] = o
	return nil
}
func (f *fakeHost) Cancel(key string, b Binding) (string, error) {
	f.store.cancelCalls++
	if key == "" || ValidateBinding(f.enrolled, b, time.Now()) != nil {
		return "", ErrDenied
	}
	if f.store.cancel == "" {
		return "unknown", nil
	}
	return f.store.cancel, nil
}

func fixture() (Binding, ProfileRef, Capabilities) {
	now := time.Now()
	b := Binding{Workspace: "workspace-1", Project: "core", Task: "task-7", Attempt: "attempt-2", Host: "codex", Session: "session-4", Instance: "process-9", CredentialID: "worker-profile-2", ExpiresAt: now.Add(time.Minute)}
	p := ProfileRef{ID: "worker-profile-2", WorkerSession: b.Session, ScopeDigest: "sha256:scope", ExpiresAt: now.Add(30 * time.Second)}
	return b, p, Capabilities{Version: 1, Host: b.Host, Model: Capability{State: Unqualified}, Dispatch: Capability{State: Supported}, Lookup: Capability{State: Supported}, PrivateProfileHandoff: Capability{State: Supported}, Observe: Capability{State: Supported}, Cancel: Capability{State: Unsupported}}
}

func TestExactBindingAndProfileExpiryRevocation(t *testing.T) {
	b, p, c := fixture()
	now := time.Now()
	if err := validateRegistration(b, Registration{Version: 1, Binding: b, Capabilities: c}, now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Binding){"host": func(x *Binding) { x.Host = "other" }, "session": func(x *Binding) { x.Session = "other" }, "instance": func(x *Binding) { x.Instance = "replacement" }, "credential": func(x *Binding) { x.CredentialID = "substituted" }, "task": func(x *Binding) { x.Task = "other" }, "attempt": func(x *Binding) { x.Attempt = "other" }, "expired": func(x *Binding) { x.ExpiresAt = now }, "revoked": func(x *Binding) { x.Revoked = true }} {
		t.Run(name, func(t *testing.T) {
			bad := b
			mutate(&bad)
			if ValidateBinding(b, bad, now) == nil {
				t.Fatal("substituted binding accepted")
			}
		})
	}
	if err := ValidateProfile(p, b, now); err != nil {
		t.Fatal(err)
	}
	p.WorkerSession = "other"
	if ValidateProfile(p, b, now) == nil {
		t.Fatal("foreign worker profile accepted")
	}
	p.WorkerSession = b.Session
	p.ExpiresAt = now
	if ValidateProfile(p, b, now) == nil {
		t.Fatal("expired profile accepted")
	}
}

func TestCapabilitiesDefaultToUnqualified(t *testing.T) {
	c := UnqualifiedCapabilities("codex")
	if c.Version != ContractVersion || c.Host != "codex" {
		t.Fatalf("bad default capability identity: %+v", c)
	}
	for name, capability := range map[string]Capability{"model": c.Model, "dispatch": c.Dispatch, "lookup": c.Lookup, "profile": c.PrivateProfileHandoff, "observe": c.Observe, "cancel": c.Cancel} {
		if capability.State != Unqualified {
			t.Errorf("%s default is %q", name, capability.State)
		}
	}
}

func TestLostDispatchResponseLooksUpOriginalKeyWithoutRelaunch(t *testing.T) {
	f := makeAuthorityFixture(t)
	p := ProfileRef{ID: "worker-profile-2", WorkerSession: f.worker.ID, ScopeDigest: "sha256:scope", ExpiresAt: f.worker.ExpiresAt}
	b := Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "codex", Session: f.worker.ID, Instance: "owner-configured-process", CredentialID: p.ID, ExpiresAt: f.worker.ExpiresAt}
	authenticated, err := AuthenticateWorker(f.a, f.workerToken, WorkerEnrollment{Binding: b, Profile: p, AttemptRequest: f.attempt.Request}, time.Now())
	if err != nil {
		t.Fatalf("authenticate worker: %v", err)
	}
	b = authenticated.Claim()
	store := &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}, loseNext: true}
	h := &fakeHost{store: store, enrolled: b}
	if err := ValidateRegistration(f.a, authenticated, Registration{Version: 1, Binding: b, Capabilities: h.Capabilities()}, time.Now()); err != nil {
		t.Fatalf("authenticated registration: %v", err)
	}
	d := Dispatch{Version: 1, OperationKey: "dispatch-original", DispatchID: f.dispatch.ID, BridgeSession: f.bridge.ID, ExpectedFence: f.dispatch.Reservation.Fence, Binding: b, Profile: p, Prompt: "Implement the assigned task.", Repository: f.attempt.Observation.Repository, Checkout: f.attempt.Observation.Locator}
	d.PromptSpecDigest = DispatchSpecDigest(d)
	coordinator := Coordinator{Backend: h, Authority: f.a, ControllerToken: f.controllerToken, ControllerSession: f.controller.ID}
	got, err := coordinator.DispatchOnce(d, authenticated, time.Now())
	if err != nil || got.OperationKey != d.OperationKey || got.HostExecutionID == "" {
		t.Fatalf("lost response reconciliation: %+v %v", got, err)
	}
	replay, err := coordinator.DispatchOnce(d, authenticated, time.Now())
	if err != nil || replay.HostExecutionID != got.HostExecutionID || store.dispatches != 1 || store.dispatchCalls != 1 {
		t.Fatalf("replay dispatched again: %+v %v launches=%d calls=%d", replay, err, store.dispatches, store.dispatchCalls)
	}
	if _, err = h.Lookup("replacement-key", b); err == nil {
		t.Fatal("unknown key was treated as an authorization to relaunch")
	}
	newKey := d
	newKey.OperationKey = "dispatch-replacement"
	if _, err = coordinator.DispatchOnce(newKey, authenticated, time.Now()); err == nil || store.dispatches != 1 || store.dispatchCalls != 1 {
		t.Fatalf("new key bypassed existing intent: %v launches=%d calls=%d", err, store.dispatches, store.dispatchCalls)
	}
	changed := d
	changed.Binding.Instance = "replacement-process"
	if _, err = coordinator.DispatchOnce(changed, authenticated, time.Now()); err == nil {
		t.Fatal("changed process label passed authenticated binding")
	}
	wrongHost := *h
	otherCaps := h.Capabilities()
	otherCaps.Host = "other-host"
	wrongHost.capabilityOverride = &otherCaps
	if _, err = (Coordinator{Backend: &wrongHost, Authority: f.a, ControllerToken: f.controllerToken, ControllerSession: f.controller.ID}).DispatchOnce(d, authenticated, time.Now()); err == nil || store.dispatchCalls != 1 {
		t.Fatalf("other-host capabilities reached effect: %v", err)
	}
	for name, mutate := range map[string]func(*Dispatch){"project": func(x *Dispatch) { x.Binding.Project = "other-project" }, "task": func(x *Dispatch) { x.Binding.Task = "other-task" }, "checkout": func(x *Dispatch) { x.Checkout = "/tmp/other-checkout" }, "prompt": func(x *Dispatch) { x.Prompt = "Run a different task."; x.PromptSpecDigest = DispatchSpecDigest(*x) }, "profile": func(x *Dispatch) {
		x.Profile.ScopeDigest = "sha256:other-scope"
		x.PromptSpecDigest = DispatchSpecDigest(*x)
	}} {
		t.Run("substitution-"+name, func(t *testing.T) {
			bad := d
			mutate(&bad)
			if _, err := coordinator.DispatchOnce(bad, authenticated, time.Now()); err == nil || store.dispatchCalls != 1 {
				t.Fatalf("%s substitution passed or dispatched: %v calls=%d", name, err, store.dispatchCalls)
			}
		})
	}
	stalePrompt := d
	stalePrompt.Prompt = "Different prompt, replayed with old digest"
	if _, err = coordinator.DispatchOnce(stalePrompt, authenticated, time.Now()); err == nil || store.dispatchCalls != 1 {
		t.Fatalf("prompt changed under original digest: %v calls=%d", err, store.dispatchCalls)
	}
	if _, err = AuthenticateWorker(f.a, "substituted-token", WorkerEnrollment{Binding: Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "codex", Session: f.worker.ID, Instance: "owner-configured-process", CredentialID: p.ID, ExpiresAt: f.worker.ExpiresAt}, Profile: p, AttemptRequest: f.attempt.Request}, time.Now()); err == nil {
		t.Fatal("different credential authenticated as enrolled worker")
	}
	if err = f.a.Revoke(f.owner, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.DispatchOnce(d, authenticated, time.Now()); err == nil || store.dispatchCalls != 1 {
		t.Fatalf("revoked worker retained dispatch authority: %v calls=%d", err, store.dispatchCalls)
	}
}

func TestCallbacksAreMonotonicAndCancellationUnknownIsNotTerminal(t *testing.T) {
	b, p, _ := fixture()
	store := &fakeStore{byKey: map[string]HostResult{}, observed: map[string]Observation{}}
	h := &fakeHost{store: store, enrolled: b}
	first := Observation{Version: 1, OperationKey: "obs-1", Binding: b, Sequence: 1, State: "running", EvidenceDigest: "sha256:one", SourceTime: time.Now()}
	if err := h.Observe(first); err != nil {
		t.Fatal(err)
	}
	older := first
	older.OperationKey = "obs-old"
	if err := h.Observe(older); err == nil {
		t.Fatal("duplicate callback accepted")
	}
	terminal := first
	terminal.OperationKey = "obs-2"
	terminal.Sequence = 2
	terminal.State = "completed"
	if err := h.Observe(terminal); err != nil {
		t.Fatal(err)
	}
	late := terminal
	late.OperationKey = "obs-3"
	late.Sequence = 3
	late.State = "running"
	if err := h.Observe(late); err == nil {
		t.Fatal("terminal state regressed")
	}
	if state, err := h.Cancel("cancel-1", b); err != nil || state != "unknown" {
		t.Fatalf("unknown cancellation: %q %v", state, err)
	}
	_ = p
}

func TestRealLifecycleAuthorityKeepsCancelAckSeparateFromTerminalObservation(t *testing.T) {
	f := makeAuthorityFixture(t)
	if err := f.a.UpgradeLifecycle(f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.a.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "lifecycle-v1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatal(err)
	}
	requested, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "lifecycle-intent", DispatchID: f.dispatch.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("b", 64), ExpectedFence: f.dispatch.Reservation.Fence})
	if err != nil || !requested.HostActionAuthorized {
		t.Fatalf("first intent: %+v %v", requested, err)
	}
	i := requested.Intent
	correlated, err := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, sessions.CorrelateDispatchIntent{OperationKey: "lifecycle-correlation", DispatchID: i.DispatchID, IntentID: i.ID, HostAgentID: "host-exec-1", State: "host_correlated", ExpectedFence: f.dispatch.Reservation.Fence, ExpectedSequence: i.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	i, err = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, sessions.AcknowledgeDispatchIntent{OperationKey: "lifecycle-worker-ack", DispatchID: i.DispatchID, IntentID: i.ID, AttemptID: f.attempt.ID, HostAgentID: correlated.HostAgentID, ExpectedFence: f.dispatch.Reservation.Fence, ExpectedSequence: correlated.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	profile := ProfileRef{ID: "worker-profile", WorkerSession: f.worker.ID, ScopeDigest: "sha256:scope", ExpiresAt: f.worker.ExpiresAt}
	claim := Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "codex", Session: f.worker.ID, Instance: "owner-configured-process", CredentialID: profile.ID, ExpiresAt: f.worker.ExpiresAt}
	worker, err := AuthenticateWorker(f.a, f.workerToken, WorkerEnrollment{Binding: claim, Profile: profile, AttemptRequest: f.attempt.Request}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bridge := LifecycleAuthority{Authority: f.a, BridgeToken: f.bridgeToken, BridgeSession: f.bridge.ID, ControllerToken: f.controllerToken, ControllerSession: f.controller.ID}
	status, err := bridge.Status(i.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	obs := Observation{Version: 1, OperationKey: "lifecycle-running", Binding: worker.Claim(), Sequence: 1, State: "running", EvidenceDigest: sessions.OutputDigest([]byte("running")), SourceTime: time.Now().UTC()}
	if _, err = bridge.AcceptObservation(worker, status.Binding, obs, 0, time.Now()); err != nil {
		t.Fatalf("real running observation: %v", err)
	}
	store := &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}
	host := &fakeHost{store: store, enrolled: worker.Claim()}
	cancelReq := sessions.CancelLifecycleRequest{OperationKey: "cancel-host", Binding: status.Binding}
	request, ack, err := bridge.CancelHost(host, worker, cancelReq)
	if err != nil || !request.HostActionAuthorized || ack.State != "dispatch_unknown" {
		t.Fatalf("unknown cancel receipt: %+v %+v %v", request, ack, err)
	}
	after, err := bridge.Status(i.DispatchID)
	if err != nil || after.State != "running" || after.Cancellation == nil || after.Cancellation.State != "dispatch_unknown" || !after.ResourcesRetained {
		t.Fatalf("cancellation acknowledgement implied termination: %+v %v", after, err)
	}
	_, _, err = bridge.CancelHost(host, worker, cancelReq)
	if err != nil || store.cancelCalls != 1 {
		t.Fatalf("replayed cancellation called host again: calls=%d err=%v", store.cancelCalls, err)
	}
	terminal := Observation{Version: 1, OperationKey: "lifecycle-completed", Binding: worker.Claim(), Sequence: 2, State: "completed", EvidenceDigest: sessions.OutputDigest([]byte("completed")), SourceTime: time.Now().UTC()}
	if _, err = bridge.AcceptObservation(worker, status.Binding, terminal, 1, time.Now()); err != nil {
		t.Fatalf("independent terminal observation: %v", err)
	}
	after, err = bridge.Status(i.DispatchID)
	if err != nil || after.State != "completed" || after.Cancellation == nil || after.Cancellation.State != "dispatch_unknown" || !after.ResourcesRetained {
		t.Fatalf("terminal observation conflated with cancellation/release: %+v %v", after, err)
	}
}

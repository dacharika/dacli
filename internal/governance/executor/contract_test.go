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
	resources := []string{"project:project", "task:task-a", "path:src/a"}
	controller, ct, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: resources}, time.Now().Add(time.Hour))
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
	bridge, _, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(time.Hour))
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
	return authorityFixture{a: a, owner: owner, controllerToken: ct, controller: controller, dispatch: d, worker: worker, workerToken: wt, attempt: attempt, bridge: bridge}
}

type fakeStore struct {
	byKey         map[string]HostResult
	observed      map[string]Observation
	dispatches    int
	dispatchCalls int
	cancel        string
	loseNext      bool
}
type fakeHost struct {
	store    *fakeStore
	enrolled Binding
}

func (f *fakeHost) Register(r Registration) error {
	return validateRegistration(f.enrolled, r, time.Now())
}
func (f *fakeHost) Capabilities() Capabilities {
	return Capabilities{Version: 1, Host: f.enrolled.Host, Model: Capability{State: Unqualified}, Dispatch: Capability{State: Supported}, Lookup: Capability{State: Supported}, PrivateProfileHandoff: Capability{State: Supported}, Observe: Capability{State: Supported}, Cancel: Capability{State: Unqualified}}
}
func (f *fakeHost) Dispatch(d Dispatch) (HostResult, error) {
	f.store.dispatchCalls++
	if d.Version != 1 || d.OperationKey == "" || ValidateBinding(f.enrolled, d.Binding, time.Now()) != nil || ValidateProfile(d.Profile, d.Binding, time.Now()) != nil {
		return HostResult{}, ErrDenied
	}
	if old, ok := f.store.byKey[d.OperationKey]; ok {
		return old, nil
	}
	f.store.dispatches++
	r := HostResult{Version: 1, OperationKey: d.OperationKey, HostExecutionID: "exec-1", State: "accepted", Binding: d.Binding}
	f.store.byKey[d.OperationKey] = r
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
	store := &fakeStore{byKey: map[string]HostResult{}, observed: map[string]Observation{}, loseNext: true}
	h := &fakeHost{store: store, enrolled: b}
	if err := ValidateRegistration(f.a, authenticated, Registration{Version: 1, Binding: b, Capabilities: h.Capabilities()}, time.Now()); err != nil {
		t.Fatalf("authenticated registration: %v", err)
	}
	d := Dispatch{Version: 1, OperationKey: "dispatch-original", DispatchID: f.dispatch.ID, BridgeSession: f.bridge.ID, ExpectedFence: f.dispatch.Reservation.Fence, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), Binding: b, Profile: p, Prompt: "Implement the assigned task."}
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

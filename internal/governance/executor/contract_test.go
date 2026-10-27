package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/team"
)

type testAdmission struct {
	snapshot  sessions.SwarmSnapshot
	preflight map[string]sessions.PreflightObservation
}

func (a *testAdmission) AdmitLease(string, string, string) error { return nil }
func (a *testAdmission) ObserveSwarm(string, string) (sessions.SwarmSnapshot, error) {
	return a.snapshot, nil
}
func (a *testAdmission) ObservePreflight(_, _, task string, _ sessions.PreflightTask, _ sessions.SwarmPlacement) (sessions.PreflightObservation, error) {
	return a.preflight[task], nil
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
	hostSlotScope          ExpectedHostSlotScope
}

func makeAuthorityFixture(t *testing.T) authorityFixture {
	return makeAuthorityFixtureWithTTLs(t, time.Hour, 30*time.Minute)
}

func makeAuthorityFixtureWithTTLs(t *testing.T, bridgeTTL, reservationTTL time.Duration) authorityFixture {
	return makeAuthorityFixtureConfigured(t, bridgeTTL, reservationTTL, false)
}

func makeAuthorityFixtureWithHostSlots(t *testing.T) authorityFixture {
	return makeAuthorityFixtureConfigured(t, time.Hour, 5*time.Minute, true)
}

func makeAuthorityFixtureConfigured(t *testing.T, bridgeTTL, reservationTTL time.Duration, hostSlots bool) authorityFixture {
	t.Helper()
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
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
	repo, err = filepath.EvalSymlinks(repo)
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
	if hostSlots {
		roleLifecycleExpiry := time.Now().UTC().Round(0).Add(time.Hour)
		ad.snapshot.RoleRegistry = []sessions.RoleRegistryProvenance{{RoleID: "implement", Version: "1.2.3", ContentDigest: "sha256:" + strings.Repeat("d", 64), LifecycleDigest: "sha256:" + strings.Repeat("e", 64), LifecycleExpiresAt: roleLifecycleExpiry}, {RoleID: "review", Version: "2.3.4", ContentDigest: "sha256:" + strings.Repeat("f", 64), LifecycleDigest: "sha256:" + strings.Repeat("a", 64), LifecycleExpiresAt: roleLifecycleExpiry}}
	}
	if err = a.UpgradeSwarms(owner); err != nil {
		t.Fatal(err)
	}
	if hostSlots {
		for _, upgrade := range []func(string) error{a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration, a.UpgradeNativeExternal, a.UpgradeNativeRoleRegistry, a.UpgradeHostCapacity, a.UpgradeHostSlots} {
			if err = upgrade(owner); err != nil {
				t.Fatalf("host-slot fixture schema upgrade: %v", err)
			}
		}
	}
	a, err = sessions.OpenSwarm(filepath.Join(root, "authority.json"), sessions.Binding{Workspace: "workspace", Installation: "installation"}, ad)
	if err != nil {
		t.Fatal(err)
	}
	var hostScope ExpectedHostSlotScope
	var slotObserver sessions.HostCapacityObserver
	var slotObserverToken string
	policy := sessions.SwarmPolicy{ProjectID: "project", Revision: "policy-v1", Harness: "codex", CapabilityDigest: "cap-v1", Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implement", Kind: "implement", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{"task-a": {Checkout: repo, ImplementKind: "implement", ReviewKind: "review"}}}
	if hostSlots {
		a, err = sessions.OpenHostSlots(filepath.Join(root, "authority.json"), sessions.Binding{Workspace: "workspace", Installation: "installation"}, ad)
		if err != nil {
			t.Fatal(err)
		}
		host, _, enrollErr := a.Enroll(owner, sessions.Scope{Actions: []string{"host.observe"}, Resources: []string{"workspace:workspace"}}, time.Now().Add(time.Hour))
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		observer, observerToken, enrollErr := a.EnrollHostCapacityObserver(owner, sessions.HostCapacityObserver{SchemaVersion: 1, HostSessionID: host.ID, ExecutorInstanceID: "executor-slots", HostID: "host-slots", Runtime: "codex", Build: "build-slots", CapabilityDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(30 * time.Minute)})
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		slotObserver, slotObserverToken = observer, observerToken
		capacityPolicy := sessions.HostCapacityPolicy{SchemaVersion: 1, WorkspaceID: "workspace", InstallationID: "installation", HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, CapabilityDigest: observer.CapabilityDigest, State: sessions.CapacityKnown, RequestedSlots: 2, Freshness: 10 * time.Minute, Revision: "capacity-slots-v1"}
		if _, err = a.ConfigureHostCapacityPolicy(owner, capacityPolicy); err != nil {
			t.Fatal(err)
		}
		if _, err = a.QualifyHostCapacity(owner, sessions.HostCapacityQualification{SchemaVersion: 1, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, HostID: observer.HostID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, State: sessions.CapacityKnown, EvidenceDigest: strings.Repeat("b", 64), QualificationID: "qualified-slots-v1", QualifiedSlots: 2, ExpiresAt: time.Now().Add(15 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		terminalExpires := time.Now().Add(15 * time.Minute)
		policy.HostSlots = &sessions.HostSlotAdmissionPolicy{SchemaVersion: 1, Mode: sessions.HostSlotSingleAuthority, Revision: "host-slots-rev1", AuthorityDomainID: "domain-slots-1", WorkspaceID: "workspace", InstallationID: "installation", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, CapacityPolicyRevision: capacityPolicy.Revision, QualificationID: "qualified-slots-v1", TerminalObservationSource: "owner-bridge-terminal", TerminalQualificationID: "terminal-qualification-v1", TerminalQualification: &sessions.HostSlotTerminalQualification{SchemaVersion: 1, ID: "terminal-qualification-v1", State: "supported", Source: "owner-bridge-terminal", EvidenceDigest: strings.Repeat("c", 64), AuthorityDomainID: "domain-slots-1", HostID: observer.HostID, HostSessionID: observer.HostSessionID, ExecutorInstanceID: observer.ExecutorInstanceID, Runtime: observer.Runtime, Build: observer.Build, CapabilityDigest: observer.CapabilityDigest, QualifiedAt: time.Now().Add(-time.Second), ExpiresAt: terminalExpires}}
		policy.Revision = "policy-host-slots"
		hostScope = ExpectedHostSlotScope{SchemaVersion: 1, AuthorityDomainID: "domain-slots-1", HostSessionID: observer.HostSessionID, CapabilityDigest: observer.CapabilityDigest}
	}
	if err = a.ConfigureSwarm(owner, policy); err != nil {
		t.Fatalf("configure swarm: %v", err)
	}
	if hostSlots {
		if _, _, err = a.RecordHostCapacityObservationV2(slotObserverToken, slotObserver.ObserverSessionID, sessions.HostCapacityObservationRequestV2{HostCapacityObservationRequest: sessions.HostCapacityObservationRequest{SchemaVersion: 2, OperationKey: "capacity-slots-empty", HostID: slotObserver.HostID, Runtime: slotObserver.Runtime, Build: slotObserver.Build, HostSessionID: slotObserver.HostSessionID, ExecutorInstanceID: slotObserver.ExecutorInstanceID, CapabilityDigest: slotObserver.CapabilityDigest, Sequence: 1, DeclarationState: sessions.CapacityKnown, DeclaredSlots: 2, ObservedState: sessions.CapacityKnown, OccupiedSlots: 0, AvailableSlots: 2, ObservedAt: time.Now().Add(-time.Second).UTC()}, Census: sessions.HostCapacityCensus{SchemaVersion: 1, AuthorityDomainID: hostScope.AuthorityDomainID, IncludedLocalExecutions: []sessions.HostSlotObservationBinding{}}}); err != nil {
			t.Fatal(err)
		}
	}
	resources := []string{"project:project", "project:other-project", "task:task-a", "task:task-b", "task:task-c", "path:src/a"}
	controller, ct, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "swarm.plan", "swarm.reserve", "swarm.dispatch", "dispatch.cancel"}, Resources: resources}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("enroll controller: %v", err)
	}
	var worker sessions.Session
	var wt string
	var attempt sessions.Attempt
	if hostSlots {
		view, viewErr := a.ReadHostSlotAdmission(ct, controller.ID, "project")
		if viewErr != nil || !view.Known {
			t.Fatalf("host-slot fixture admission is not known: view=%+v err=%v", view, viewErr)
		}
		workerResources := append(append([]string(nil), resources...), "checkout:", "checkout:"+repo)
		worker, wt, err = a.Enroll(owner, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "verification.request", "evidence.submit"}, Resources: workerResources}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		tokenDigest := sha256.Sum256([]byte(wt))
		role := func(name, kind, grant string) team.Role {
			return team.Role{Name: name, Kind: kind, Runtime: "codex", Model: "model", Grant: grant, MaxPoints: 8}
		}
		ad.preflight = map[string]sessions.PreflightObservation{"task-a": {AuthorityPath: filepath.Join(canonicalRoot, "authority.json"), InstallationID: "installation", TaskDigest: strings.Repeat("1", 64), RosterDigest: strings.Repeat("2", 64), ClaimsDigest: strings.Repeat("3", 64), ProfileDigest: strings.Repeat("4", 64), Harness: "codex", ClaimResources: []string{"path:src/a"}, Required: 1, Sized: true, Implementation: role("implement", "implement", "rw"), Review: role("review", "review", "ro"), Recovery: role("implement", "implement", "rw"), Worker: sessions.PreflightActor{SessionID: worker.ID, CredentialHash: hex.EncodeToString(tokenDigest[:])}, RoleRegistry: ad.snapshot.RoleRegistry, ExpiresAt: time.Now().Add(20 * time.Minute), Phases: []sessions.PreflightPhase{{Phase: "implementation", Verdict: "ready", Assurance: "owner-manual", Required: true}, {Phase: "review", Verdict: "ready", Assurance: "owner-manual", Required: false}, {Phase: "verification", Verdict: "ready", Assurance: "owner-manual", Required: false}, {Phase: "integration", Verdict: "ready", Assurance: "owner-manual", Required: false}, {Phase: "recovery", Verdict: "ready", Assurance: "owner-manual", Required: true}, {Phase: "fallback", Verdict: "ready", Assurance: "owner-manual", Required: false}, {Phase: "tokens", Verdict: "ready", Assurance: "owner-manual", Required: false}, {Phase: "budget", Verdict: "ready", Assurance: "owner-manual", Required: false}}}}
		preflight := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "project", Revision: "preflight-slots", Harness: "codex", Rationale: "fixture for bound slot coordinator", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(20 * time.Minute), Tasks: map[string]sessions.PreflightTask{"task-a": {ImplementationRole: "implement", ReviewRole: "review", RecoveryRole: "implement", WorkerConnection: filepath.Join(root, "worker.json"), ContractProfile: filepath.Join(root, "contract.json"), RequireReview: false, RequireVerification: false}}}
		if err = a.ConfigurePreflight(owner, preflight); err != nil {
			t.Fatalf("configure preflight fixture: %v", err)
		}
	}
	plan, err := a.PlanSwarm(ct, controller.ID, "project")
	if err != nil {
		t.Fatalf("plan wave: %v", err)
	}
	if hostSlots && (len(plan.Verdicts) == 0 || !plan.Verdicts[0].Selected) {
		var report any
		if len(plan.Verdicts) > 0 {
			report = plan.Verdicts[0].Preflight
		}
		t.Fatalf("host-slot fixture plan did not select task: verdicts=%+v preflight=%+v", plan.Verdicts, report)
	}
	wave, err := a.ReserveWave(ct, controller.ID, sessions.ReserveWaveRequest{OperationKey: "wave-1", Plan: plan, ExpiresAt: time.Now().Add(reservationTTL)})
	if err != nil {
		t.Fatalf("reserve wave: %v (plan=%+v)", err, plan)
	}
	if !hostSlots {
		err = a.UpgradeDispatchIntents(owner)
	}
	if err != nil {
		t.Fatalf("upgrade dispatch intents: %v", err)
	}
	d := wave.Dispatches[0]
	bridge, bt, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: resources}, time.Now().Add(bridgeTTL))
	if err != nil {
		t.Fatal(err)
	}
	if hostSlots {
		attempt, err = a.RegisterAttempt(wt, worker.ID, sessions.AttemptRequest{RequestID: "worker-attempt", TaskID: "task-a", Checkout: repo})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		worker, wt, err = a.Enroll(owner, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit"}, Resources: append(resources, "checkout:")}, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		attempt, err = a.RegisterAttempt(wt, worker.ID, sessions.AttemptRequest{RequestID: "worker-attempt", TaskID: "task-a", Checkout: repo})
		if err != nil {
			t.Fatal(err)
		}
	}
	return authorityFixture{a: a, owner: owner, controllerToken: ct, controller: controller, dispatch: d, worker: worker, workerToken: wt, attempt: attempt, bridge: bridge, bridgeToken: bt, hostSlotScope: hostScope}
}

type fakeStore struct {
	byKey            map[string]HostResult
	digests          map[string]string
	observed         map[string]Observation
	dispatches       int
	dispatchCalls    int
	cancelCalls      int
	registerCalls    int
	cancel           string
	loseNext         bool
	lastCancellation CancellationRequest
}
type fakeHost struct {
	store              *fakeStore
	enrolled           Binding
	capabilityOverride *Capabilities
	capabilityCalls    int
}

func (f *fakeHost) Register(r Registration) error {
	f.store.registerCalls++
	return validateRegistration(f.enrolled, r, time.Now())
}
func (f *fakeHost) VerifyBinding(b Binding) error { return ValidateBinding(f.enrolled, b, time.Now()) }
func (f *fakeHost) Capabilities() Capabilities {
	f.capabilityCalls++
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
	r := HostResult{Version: 1, OperationKey: d.OperationKey, DispatchID: d.DispatchID, HostExecutionID: "exec-1", State: "accepted", Repository: d.Repository, Checkout: d.Checkout, Binding: d.Binding}
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
func (f *fakeHost) Cancel(req CancellationRequest) (string, error) {
	f.store.cancelCalls++
	f.store.lastCancellation = req
	if req.Version != ContractVersion || req.OperationKey == "" || req.Lifecycle.DispatchID == "" || req.Lifecycle.IntentID == "" || req.Lifecycle.HostAgentID == "" || req.Lifecycle.LeaseID == "" || req.Repository == "" || req.Checkout == "" || ValidateBinding(f.enrolled, req.Worker, time.Now()) != nil {
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

func TestCredentialBearingHelpersRedactFormattingAndJSON(t *testing.T) {
	worker := AuthenticatedBinding{token: "WORKER_TOKEN_SENTINEL"}
	controller := NewCoordinator(nil, nil, "CONTROLLER_TOKEN_SENTINEL", "controller-session")
	bridge := NewLifecycleAuthority(nil, "BRIDGE_TOKEN_SENTINEL", "bridge-session", "LIFECYCLE_CONTROLLER_SENTINEL", "controller-session")
	objects := []any{worker, controller, bridge}
	secrets := []string{"WORKER_TOKEN_SENTINEL", "CONTROLLER_TOKEN_SENTINEL", "BRIDGE_TOKEN_SENTINEL", "LIFECYCLE_CONTROLLER_SENTINEL"}
	for _, object := range objects {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			got := fmt.Sprintf(format, object)
			for _, secret := range secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("%s leaked through %s", secret, format)
				}
			}
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range secrets {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("%s leaked through JSON", secret)
			}
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
	coordinator := NewCoordinator(h, f.a, f.controllerToken, f.controller.ID)
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
	if _, err = NewCoordinator(&wrongHost, f.a, f.controllerToken, f.controller.ID).DispatchOnce(d, authenticated, time.Now()); err == nil || store.dispatchCalls != 1 {
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
	cancelReq := CancellationRequest{Version: 1, OperationKey: "cancel-1", Worker: b, Lifecycle: sessions.LifecycleBinding{DispatchID: "dispatch", IntentID: "intent", HostAgentID: "host-exec", LeaseID: "lease"}, Repository: "repo", Checkout: "/repo"}
	if state, err := h.Cancel(cancelReq); err != nil || state != "unknown" {
		t.Fatalf("unknown cancellation: %q %v", state, err)
	}
	_ = p
}

func TestRealLifecycleAuthorityKeepsCancelAckSeparateFromTerminalObservation(t *testing.T) {
	f := makeAuthorityFixtureWithTTLs(t, 12*time.Second, 10*time.Second)
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
	bridge := NewLifecycleAuthority(f.a, f.bridgeToken, f.bridge.ID, f.controllerToken, f.controller.ID)
	status, err := bridge.Status(i.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	obs := Observation{Version: 1, OperationKey: "lifecycle-running", Binding: worker.Claim(), Sequence: 1, State: "running", EvidenceDigest: sessions.OutputDigest([]byte("running")), SourceTime: time.Now().UTC()}
	for name, mutate := range map[string]func(*Observation){"task": func(o *Observation) { o.Binding.Task = "task-c" }, "attempt": func(o *Observation) { o.Binding.Attempt = "other-attempt" }, "instance": func(o *Observation) { o.Binding.Instance = "replacement-process" }} {
		bad := obs
		mutate(&bad)
		if _, e := bridge.AcceptObservation(worker, status.Binding, bad, 0); e == nil {
			t.Fatalf("substituted %s callback accepted", name)
		}
	}
	wrongLifecycle := status.Binding
	wrongLifecycle.AttemptID = "other-attempt"
	if _, e := bridge.AcceptObservation(worker, wrongLifecycle, obs, 0); e == nil {
		t.Fatal("substituted lifecycle attempt accepted")
	}
	if _, err = bridge.AcceptObservation(worker, status.Binding, obs, 0); err != nil {
		t.Fatalf("real running observation: %v", err)
	}
	wrongBridge := NewLifecycleAuthority(f.a, "wrong-bridge-credential", f.bridge.ID, f.controllerToken, f.controller.ID)
	wrongBridgeCallback := Observation{Version: 1, OperationKey: "wrong-bridge-running", Binding: worker.Claim(), Sequence: 2, State: "completed", EvidenceDigest: sessions.OutputDigest([]byte("wrong-bridge")), SourceTime: time.Now().UTC()}
	if _, e := wrongBridge.AcceptObservation(worker, status.Binding, wrongBridgeCallback, 1); e == nil {
		t.Fatal("wrong bridge credential accepted while lifecycle was running")
	}
	store := &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}
	host := &fakeHost{store: store, enrolled: worker.Claim()}
	cancelReq := sessions.CancelLifecycleRequest{OperationKey: "cancel-host", Binding: status.Binding}
	wrongTask := cancelReq
	wrongTask.Binding.TaskID = "task-c"
	if _, _, e := bridge.CancelHost(host, worker, wrongTask, "lifecycle-intent"); e == nil || store.cancelCalls != 0 {
		t.Fatalf("wrong-task cancellation reached host: %v calls=%d", e, store.cancelCalls)
	}
	wrongInstance := worker
	wrongInstance.binding.Instance = "replacement-process"
	if _, _, e := bridge.CancelHost(host, wrongInstance, cancelReq, "lifecycle-intent"); e == nil || store.cancelCalls != 0 {
		t.Fatalf("wrong-instance cancellation reached host: %v calls=%d", e, store.cancelCalls)
	}
	wrongHost := *host
	wrongCaps := host.Capabilities()
	wrongCaps.Host = "another-host"
	wrongHost.capabilityOverride = &wrongCaps
	if _, _, e := bridge.CancelHost(&wrongHost, worker, cancelReq, "lifecycle-intent"); e == nil || store.cancelCalls != 0 {
		t.Fatalf("wrong-host cancellation reached host: %v calls=%d", e, store.cancelCalls)
	}
	if _, _, e := bridge.CancelHost(host, worker, cancelReq, "substituted-original-operation"); e == nil || store.cancelCalls != 0 {
		t.Fatalf("wrong original operation key reached host: %v calls=%d", e, store.cancelCalls)
	}
	request, ack, err := bridge.CancelHost(host, worker, cancelReq, "lifecycle-intent")
	if err != nil || !request.HostActionAuthorized || ack.State != "dispatch_unknown" {
		t.Fatalf("unknown cancel receipt: %+v %+v %v", request, ack, err)
	}
	target := store.lastCancellation
	if target.OperationKey != cancelReq.OperationKey || target.OriginalOperationKey != "lifecycle-intent" || target.Lifecycle != status.Binding || !target.Worker.same(worker.Claim()) || target.Repository != worker.attempt.Observation.Repository || target.Checkout != worker.attempt.Observation.Locator {
		t.Fatalf("cancellation target lost exact execution binding: %+v", target)
	}
	after, err := bridge.Status(i.DispatchID)
	if err != nil || after.State != "running" || after.Cancellation == nil || after.Cancellation.State != "dispatch_unknown" || !after.ResourcesRetained {
		t.Fatalf("cancellation acknowledgement implied termination: %+v %v", after, err)
	}
	_, _, err = bridge.CancelHost(host, worker, cancelReq, "lifecycle-intent")
	if err != nil || store.cancelCalls != 1 {
		t.Fatalf("replayed cancellation called host again: calls=%d err=%v", store.cancelCalls, err)
	}
	if err = f.a.Revoke(f.owner, f.worker.ID); err != nil {
		t.Fatal(err)
	}
	expiredWorker := worker
	expiredWorker.binding.ExpiresAt = time.Now().Add(-time.Hour)
	expiredWorker.binding.Revoked = true
	terminal := Observation{Version: 1, OperationKey: "lifecycle-completed", Binding: expiredWorker.Claim(), Sequence: 2, State: "completed", EvidenceDigest: sessions.OutputDigest([]byte("completed")), SourceTime: time.Now().UTC()}
	if _, err = bridge.AcceptObservation(expiredWorker, status.Binding, terminal, 1); err != nil {
		t.Fatalf("independent terminal observation: %v", err)
	}
	after, err = bridge.Status(i.DispatchID)
	if err != nil || after.State != "completed" || after.Cancellation == nil || after.Cancellation.State != "dispatch_unknown" || !after.ResourcesRetained {
		t.Fatalf("terminal observation conflated with cancellation/release: %+v %v", after, err)
	}
}

func TestExpiredBridgeCannotAcceptRunningLifecycleObservation(t *testing.T) {
	f := makeAuthorityFixtureWithTTLs(t, 3*time.Second, 2*time.Second)
	if err := f.a.UpgradeLifecycle(f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.a.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "project", Revision: "lifecycle-v1", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatal(err)
	}
	requested, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "expired-bridge-intent", DispatchID: f.dispatch.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("c", 64), ExpectedFence: f.dispatch.Reservation.Fence})
	if err != nil || !requested.HostActionAuthorized {
		t.Fatalf("first intent: %+v %v", requested, err)
	}
	i := requested.Intent
	correlated, err := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, sessions.CorrelateDispatchIntent{OperationKey: "expired-bridge-correlation", DispatchID: i.DispatchID, IntentID: i.ID, HostAgentID: "host-exec-expired", State: "host_correlated", ExpectedFence: f.dispatch.Reservation.Fence, ExpectedSequence: i.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	i, err = f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, sessions.AcknowledgeDispatchIntent{OperationKey: "expired-bridge-worker-ack", DispatchID: i.DispatchID, IntentID: i.ID, AttemptID: f.attempt.ID, HostAgentID: correlated.HostAgentID, ExpectedFence: f.dispatch.Reservation.Fence, ExpectedSequence: correlated.Sequence})
	if err != nil {
		t.Fatal(err)
	}
	profile := ProfileRef{ID: "worker-profile", WorkerSession: f.worker.ID, ScopeDigest: "sha256:scope", ExpiresAt: f.worker.ExpiresAt}
	claim := Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "codex", Session: f.worker.ID, Instance: "owner-configured-process", CredentialID: profile.ID, ExpiresAt: f.worker.ExpiresAt}
	worker, err := AuthenticateWorker(f.a, f.workerToken, WorkerEnrollment{Binding: claim, Profile: profile, AttemptRequest: f.attempt.Request}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewLifecycleAuthority(f.a, f.bridgeToken, f.bridge.ID, f.controllerToken, f.controller.ID)
	status, err := bridge.Status(i.DispatchID)
	if err != nil {
		t.Fatalf("lifecycle did not remain running: %+v %v", status, err)
	}
	first := Observation{Version: 1, OperationKey: "expired-bridge-first-running", Binding: worker.Claim(), Sequence: 1, State: "running", EvidenceDigest: sessions.OutputDigest([]byte("running")), SourceTime: time.Now().UTC()}
	if _, err := bridge.AcceptObservation(worker, status.Binding, first, 0); err != nil {
		t.Fatalf("initial running observation: %v", err)
	}
	status, err = bridge.Status(i.DispatchID)
	if err != nil || status.State != "running" {
		t.Fatalf("lifecycle did not enter running: %+v %v", status, err)
	}
	if wait := time.Until(f.bridge.ExpiresAt) + 25*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	callback := Observation{Version: 1, OperationKey: "expired-bridge-completed", Binding: worker.Claim(), Sequence: 2, State: "completed", EvidenceDigest: sessions.OutputDigest([]byte("expired-bridge")), SourceTime: time.Now().UTC()}
	if _, err := bridge.AcceptObservation(worker, status.Binding, callback, 1); err == nil {
		t.Fatal("expired bridge accepted observation for a running lifecycle")
	}
}

func TestHostSlotClaimMustMatchAuthenticatedWorkerScope(t *testing.T) {
	worker := Binding{Host: "host-a", Session: "worker-session-a", Instance: "executor-a"}
	expected := ExpectedHostSlotScope{SchemaVersion: 1, AuthorityDomainID: "domain-a", HostSessionID: "host-session-a", CapabilityDigest: strings.Repeat("a", 64)}
	req := Dispatch{DispatchID: "dispatch-a", ExpectedFence: 9}
	claim := &sessions.HostSlotClaim{SchemaVersion: 1, AuthorityDomainID: "domain-a", HostID: "host-a", HostSessionID: "host-session-a", ExecutorInstanceID: "executor-a", CapabilityDigest: strings.Repeat("a", 64), DispatchID: "dispatch-a", DispatchFence: 9, Units: 1}
	if !hostSlotClaimMatchesWorker(claim, req, worker, expected, true) {
		t.Fatal("exact authority-returned host-slot claim did not match the authenticated worker")
	}
	for name, mutate := range map[string]func(*sessions.HostSlotClaim){
		"host":     func(c *sessions.HostSlotClaim) { c.HostID = "host-b" },
		"session":  func(c *sessions.HostSlotClaim) { c.HostSessionID = "worker-session-b" },
		"instance": func(c *sessions.HostSlotClaim) { c.ExecutorInstanceID = "executor-b" },
		"dispatch": func(c *sessions.HostSlotClaim) { c.DispatchID = "dispatch-b" },
		"fence":    func(c *sessions.HostSlotClaim) { c.DispatchFence++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *claim
			mutate(&changed)
			if hostSlotClaimMatchesWorker(&changed, req, worker, expected, true) {
				t.Fatalf("substituted %s claim passed worker-scope check", name)
			}
		})
	}
	if hostSlotClaimMatchesWorker(claim, req, worker, ExpectedHostSlotScope{}, false) {
		t.Fatal("bound claim without an owner-enrolled expected host scope was accepted")
	}
	if hostSlotClaimMatchesWorker(nil, req, worker, expected, true) {
		t.Fatal("owner-enrolled host scope silently downgraded to a manual reservation")
	}
	if !hostSlotClaimMatchesWorker(nil, req, worker, ExpectedHostSlotScope{}, false) {
		t.Fatal("legacy manual reservation was not preserved")
	}
}

func TestCoordinatorPreflightsBoundHostScopeBeforeBackendAndIntent(t *testing.T) {
	f := makeAuthorityFixtureWithHostSlots(t)
	preflight, err := f.a.CheckPreflight(f.controllerToken, f.controller.ID, "project", "task-a", "preflight-debug")
	if err != nil || !preflight.Report.Allowed || preflight.Report.ProofDigest != f.dispatch.PreflightDigest {
		t.Fatalf("stable selected preflight unavailable before coordinator: allowed=%v checked-digest=%s reservation-digest=%s err=%v report=%+v", preflight.Report.Allowed, preflight.Report.ProofDigest, f.dispatch.PreflightDigest, err, preflight.Report)
	}
	p := ProfileRef{ID: "worker-profile-slots", WorkerSession: f.worker.ID, ScopeDigest: "sha256:scope", ExpiresAt: f.worker.ExpiresAt}
	b := Binding{Workspace: "workspace", Project: "project", Task: "task-a", Host: "host-slots", Session: f.worker.ID, Instance: "executor-slots", CredentialID: p.ID, ExpiresAt: f.worker.ExpiresAt}
	goodEnrollment := WorkerEnrollment{Binding: b, Profile: p, AttemptRequest: f.attempt.Request, ExpectedHostSlotScope: &f.hostSlotScope}
	goodWorker, err := AuthenticateWorker(f.a, f.workerToken, goodEnrollment, time.Now())
	if err != nil {
		t.Fatalf("authenticate bound worker: %v", err)
	}
	wrongScope := f.hostSlotScope
	goodEnrollment.ExpectedHostSlotScope.HostSessionID = "mutated-after-authentication"
	store := &fakeStore{byKey: map[string]HostResult{}, digests: map[string]string{}, observed: map[string]Observation{}}
	host := &fakeHost{store: store, enrolled: goodWorker.Claim()}
	coordinator := NewCoordinator(host, f.a, f.controllerToken, f.controller.ID)
	dispatch := Dispatch{Version: 1, OperationKey: "bound-dispatch-original", DispatchID: f.dispatch.ID, BridgeSession: f.bridge.ID, ExpectedFence: f.dispatch.Reservation.Fence, Binding: goodWorker.Claim(), Profile: p, Prompt: "Implement the assigned task.", Repository: f.attempt.Observation.Repository, Checkout: f.attempt.Observation.Locator}
	dispatch.PromptSpecDigest = DispatchSpecDigest(dispatch)

	wrongScope.HostSessionID = "substituted-host-session"
	wrongEnrollment := goodEnrollment
	wrongEnrollment.ExpectedHostSlotScope = &wrongScope
	wrongWorker, err := AuthenticateWorker(f.a, f.workerToken, wrongEnrollment, time.Now())
	if err != nil {
		t.Fatalf("authenticate worker with substituted owner scope: %v", err)
	}
	if _, err = coordinator.DispatchOnce(dispatch, wrongWorker, time.Now()); err == nil {
		t.Fatal("substituted reserved host session passed coordinator preflight")
	}
	if host.capabilityCalls != 0 || store.registerCalls != 0 || store.dispatchCalls != 0 || store.dispatches != 0 {
		t.Fatalf("wrong host scope reached backend: capabilities=%d registers=%d dispatch-calls=%d dispatches=%d", host.capabilityCalls, store.registerCalls, store.dispatchCalls, store.dispatches)
	}
	if _, err = f.a.DispatchIntentStatus(f.controllerToken, f.controller.ID, f.dispatch.ID); err == nil {
		t.Fatal("wrong host scope created a durable dispatch intent")
	}

	result, err := coordinator.DispatchOnce(dispatch, goodWorker, time.Now())
	if err != nil || result.OperationKey != dispatch.OperationKey || store.registerCalls != 1 || store.dispatchCalls != 1 || store.dispatches != 1 {
		intent, intentErr := f.a.DispatchIntentStatus(f.controllerToken, f.controller.ID, f.dispatch.ID)
		t.Fatalf("bound host scope failed coordinator dispatch: result=%+v err=%v intent=%+v intentErr=%v registers=%d dispatch-calls=%d dispatches=%d", result, err, intent, intentErr, store.registerCalls, store.dispatchCalls, store.dispatches)
	}
}

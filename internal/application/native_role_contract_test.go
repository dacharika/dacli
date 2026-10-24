package application

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/rolebundle"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeRoleContractRequirementsAreObservedBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(*rolebundle.RoleContract)
		wantAllow bool
	}{
		{name: "supported cooperative contract", wantAllow: true},
		{name: "required network containment", mutate: func(c *rolebundle.RoleContract) { c.Host.Network = "disabled" }},
		{name: "required native gate without policy reference", mutate: func(c *rolebundle.RoleContract) { c.Gates = []string{"preflight"} }},
		{name: "required unobserved tool", mutate: func(c *rolebundle.RoleContract) { c.Tools = []rolebundle.CapabilityNeed{{ID: "shell", Required: true}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w, _ := readFixture(t)
			if _, err := store.CreateProject(w, "owner", "Core", "core", "role contract", ""); err != nil {
				t.Fatal(err)
			}
			role := team.Role{Name: "builder", Summary: "build", Grant: "rw", Scope: []string{"src/**"}, Prompt: "inspect then build", Runtime: "codex", Model: "model-a"}
			if err := store.CreateRole(w, "owner", role); err != nil {
				t.Fatal(err)
			}
			roles, err := store.ReadCheckpointRoles(w)
			if err != nil || len(roles) != 1 {
				t.Fatalf("canonical role read: roles=%d err=%v", len(roles), err)
			}
			role = roles[0]
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			registryDir := filepath.Join(w.Root, ".dacli", "role-registry")
			if err := os.MkdirAll(registryDir, 0700); err != nil {
				t.Fatal(err)
			}
			keyring, err := json.Marshal(map[string][]byte{"test-publisher": public})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(registryDir, "publishers.json"), keyring, 0600); err != nil {
				t.Fatal(err)
			}
			contract := rolebundle.RoleContract{
				Purpose: role.Summary, Instructions: role.Prompt,
				CapabilityScope: rolebundle.CapabilityScope{Grant: role.Grant, Scope: role.Scope},
				Runtime:         rolebundle.RuntimePreference{Runtime: role.Runtime, Model: role.Model, Profile: role.Profile},
				Host:            rolebundle.HostRequirements{Capabilities: []string{"native-policy-observation"}, Sandbox: "cooperative", Network: "unrestricted", Filesystem: "role-scope", Process: "unrestricted"},
				Budget:          rolebundle.BudgetRequirement{Mode: "none"},
			}
			if test.mutate != nil {
				test.mutate(&contract)
			}
			references := []rolebundle.Reference{{Kind: "policy", ID: "native-swarm", Version: "policy-v1", Digest: "sha256:" + strings.Repeat("a", 64)}}
			release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.ReleaseContractSchemaVersion, Version: "2.0.0", Role: role, References: references, Contract: &contract}, "test-publisher", private)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"test-publisher": public})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.StoreRelease(release); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: role.Name, Version: release.Content.Version, ContentDigest: release.ContentDigest, Generation: 1, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "test-publisher"}, private)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.StoreLifecycle(status, now); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Pin("core", role.Name, release.Content.Version, release.ContentDigest, "owner", "pin schema-2 role", now); err != nil {
				t.Fatal(err)
			}
			roles, err = store.ReadCheckpointRoles(w)
			if err != nil {
				t.Fatal(err)
			}
			if !store.RoleFileMatches(release, roles) {
				t.Fatalf("v2 role projection differs from canonical role: got=%+v want=%+v", roles, role)
			}
			admission, err := NewLeaseAdmission(w)
			if err != nil {
				t.Fatal(err)
			}
			_, err = admission.ObserveSwarm(w.ID, "core")
			if (err == nil) != test.wantAllow {
				t.Fatalf("native admission allowed=%v, want %v; err=%v", err == nil, test.wantAllow, err)
			}
		})
	}
}

type roleContractBoundaryAdmission struct {
	*LeaseAdmission
	mutate                 func() error
	mutateAtCapabilityCall int
	mutationCapabilityCall int
	capabilityCalls        []string
	err                    error
	observePreflight       func(string, string, string, sessions.PreflightTask, sessions.SwarmPlacement) (sessions.PreflightObservation, error)
}

func (a *roleContractBoundaryAdmission) beforeCapability(kind string) error {
	a.capabilityCalls = append(a.capabilityCalls, kind)
	if a.mutate == nil || len(a.capabilityCalls) != a.mutateAtCapabilityCall {
		return nil
	}
	mutate := a.mutate
	a.mutate = nil
	a.mutationCapabilityCall = len(a.capabilityCalls)
	a.err = mutate()
	return a.err
}

func (a *roleContractBoundaryAdmission) AdmitSwarmCapability(w, p, h, d string) error {
	if err := a.beforeCapability("swarm"); err != nil {
		return err
	}
	return a.LeaseAdmission.AdmitSwarmCapability(w, p, h, d)
}

func (a *roleContractBoundaryAdmission) AdmitDispatchCapability(w, p, h, d string) error {
	if err := a.beforeCapability("dispatch"); err != nil {
		return err
	}
	return a.LeaseAdmission.AdmitDispatchCapability(w, p, h, d)
}

func (a *roleContractBoundaryAdmission) ObservePreflight(w, p, task string, spec sessions.PreflightTask, placement sessions.SwarmPlacement) (sessions.PreflightObservation, error) {
	if a.observePreflight == nil {
		return sessions.PreflightObservation{}, sessions.ErrDenied
	}
	return a.observePreflight(w, p, task, spec, placement)
}

type roleContractBoundaryFixture struct {
	w                        *workspace.Workspace
	a                        *sessions.Authority
	owner, controllerToken   string
	controller               sessions.Session
	bridge, worker           sessions.Session
	bridgeToken, workerToken string
	attempt                  sessions.Attempt
	dispatch                 sessions.DispatchReservation
	registry                 *store.RoleRegistry
	public                   ed25519.PublicKey
	private                  ed25519.PrivateKey
	role                     team.Role
	admission                *roleContractBoundaryAdmission
	capabilityPath           string
	capabilityDigest         string
}

func upgradeRoleContractAuthorityTo20(t *testing.T, path string, binding sessions.Binding, owner string, admission sessions.LeaseAdmission) *sessions.Authority {
	t.Helper()
	for version := 6; version < 20; version++ {
		a, err := sessions.OpenCurrent(path, binding, admission)
		if err != nil {
			t.Fatal(err)
		}
		var upgrade func(string) error
		switch version {
		case 6:
			upgrade = a.UpgradeSwarms
		case 7:
			upgrade = a.UpgradeDispatchIntents
		case 8:
			upgrade = a.UpgradeLifecycle
		case 9:
			upgrade = a.UpgradeRecovery
		case 10:
			upgrade = a.UpgradePreflight
		case 11:
			upgrade = a.UpgradeCheckpoints
		case 12:
			upgrade = a.UpgradeBudgets
		case 13:
			upgrade = a.UpgradeNativeAcceptance
		case 14:
			upgrade = a.UpgradeNativePolicy
		case 15:
			upgrade = a.UpgradeController
		case 16:
			upgrade = a.UpgradeNativeModelRouting
		case 17:
			upgrade = a.UpgradeNativeIntegration
		case 18:
			upgrade = a.UpgradeNativeExternal
		case 19:
			upgrade = a.UpgradeNativeRoleRegistry
		}
		if err := upgrade(owner); err != nil {
			t.Fatalf("explicit disposable schema migration from %d: %v", version, err)
		}
	}
	a, err := sessions.OpenNativeRoleRegistry(path, binding, admission)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func readyPreflightPhases() []sessions.PreflightPhase {
	phases := []sessions.PreflightPhase{}
	for _, phase := range []string{"implementation", "review", "verification", "integration", "recovery", "fallback", "tokens", "budget"} {
		phases = append(phases, sessions.PreflightPhase{Phase: phase, Verdict: "ready", Assurance: "owner-manual", Required: phase != "integration" && phase != "tokens" && phase != "budget"})
	}
	return phases
}

func setupRoleContractBoundary(t *testing.T) roleContractBoundaryFixture {
	t.Helper()
	w, _ := readFixture(t)
	if _, err := store.CreateProject(w, "owner", "Core", "core", "v2 role contract boundary", ""); err != nil {
		t.Fatal(err)
	}
	role := team.Role{Name: "builder", Kind: "implementer", Summary: "build", Prompt: "inspect then build", Grant: "rw", Scope: []string{"tracked"}, Runtime: "codex", Model: "model-a", MaxPoints: 8, Profile: team.ModelProfile{ID: "model-a", MaxTaskPoints: 8, ContextLimit: 80000}}
	reviewerRole := team.Role{Name: "reviewer", Kind: "reviewer", Summary: "review", Prompt: "review changes", Grant: "ro", Runtime: "codex", Model: "model-a", MaxPoints: 8, Profile: team.ModelProfile{ID: "model-a", MaxTaskPoints: 8, ContextLimit: 80000}}
	for _, canonicalRole := range []team.Role{role, reviewerRole} {
		if err := store.CreateRole(w, "owner", canonicalRole); err != nil {
			t.Fatal(err)
		}
	}
	roles, err := store.ReadCheckpointRoles(w)
	if err != nil || len(roles) != 2 {
		t.Fatalf("canonical role: count=%d err=%v", len(roles), err)
	}
	roleByID := map[string]team.Role{}
	for _, canonicalRole := range roles {
		roleByID[canonicalRole.Name] = canonicalRole
	}
	role, reviewerRole = roleByID["builder"], roleByID["reviewer"]
	checkoutPath := filepath.Join(t.TempDir(), "role-contract-worker")
	if err := os.Mkdir(checkoutPath, 0700); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, checkoutPath, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(checkoutPath, "tracked"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, checkoutPath, "add", "tracked")
	nativeGit(t, checkoutPath, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	checkoutPath, err = filepath.EvalSymlinks(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := checkout.Observe(checkoutPath)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(w, "owner", "core", "Signed role contract boundary", store.TaskOpts{Priority: "must", Estimate: "1,2,3", Claims: []string{"tracked"}})
	if err != nil {
		t.Fatal(err)
	}
	capabilityPath := filepath.Join(t.TempDir(), "manual-capability.json")
	probePath := filepath.Join(t.TempDir(), "dispatch-probe.json")
	probe := []byte(strings.Replace(`{"schema_version":1,"probe":"dacli-codex-native-dispatch/v1","observed_at":"2000-01-01T00:00:00Z","host_version":"codex-cli 0.159.2","binary_sha256":"50ac633af64851511f9bbc71032cdae7f1ba20b3234c189687d61ba846c354c5","client_protocol_sha256":"f21e6307c8c2c7f2b5a32d434cdca238e3ec19b71a1f40b60f8c305289860436","declared_method_count":170,"app_server_dispatch":{"agent/spawn":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"},"collaboration/spawn_agent":{"declared_in_protocol":false,"unknown_method_refusal_observed":true,"status":"unsupported"}},"thread_config_schema":{"ThreadStartParams":{"config_field_declared":true,"schema_sha256":"dc8bebec4181a5502fc141b0122cc48e066b3b5c77d9fd2a4de875fcd6d66a68"},"ThreadForkParams":{"config_field_declared":true,"schema_sha256":"ea9dd8fb39979f32293a31f98e599509430b7cc80840b2a0afc90a2b953e942e"}},"native_collaboration":{"status":"requires-native-host-attestation"},"private_child_profile_delivery":{"status":"unverified"},"stable_dispatch_key_lookup":{"status":"unverified"},"cancellation":{"method_declared":true,"recognized_method_refusal":true,"running_turn_cancellation":"unverified"},"scope":{"isolated_codex_home":true,"thread_created":false,"model_turn_requested":false,"worker_credentials_read":false,"native_child_dispatched":false},"app_server_exit_code":0}`, "2000-01-01T00:00:00Z", time.Now().UTC().Format(time.RFC3339Nano), 1))
	if err := os.WriteFile(probePath, probe, 0600); err != nil {
		t.Fatal(err)
	}
	probeDigest := sha256.Sum256(probe)
	capability := ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: w.ID, ProjectID: "core", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(40 * time.Minute).UTC(), Codex: &CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probePath, ProbeSHA256: hex.EncodeToString(probeDigest[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}}
	capabilityBytes, err := json.Marshal(capability)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capabilityPath, capabilityBytes, 0600); err != nil {
		t.Fatal(err)
	}
	capabilityDigest := sha256.Sum256(capabilityBytes)
	capabilityDigestText := "sha256:" + hex.EncodeToString(capabilityDigest[:])
	if err := (manualSwarmCapabilityFile{path: capabilityPath}).AdmitDispatchCapability(w.ID, "core", "codex", capabilityDigestText); err != nil {
		t.Fatalf("fixture dispatch capability: %v", err)
	}
	baseAdmission, err := NewLeaseAdmission(w)
	if err != nil {
		t.Fatal(err)
	}
	baseAdmission, err = NewSwarmAdmission(w, manualSwarmCapabilityFile{path: capabilityPath})
	if err != nil {
		t.Fatal(err)
	}
	admission := &roleContractBoundaryAdmission{LeaseAdmission: baseAdmission}
	path := filepath.Join(t.TempDir(), "authority.json")
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(canonicalParent, filepath.Base(path))
	binding := sessions.Binding{Workspace: w.ID, Installation: "role-contract-boundary"}
	a, err := sessions.Open(path, binding, admission)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := a.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.BindTask(owner, sessions.TaskBinding{TaskID: task.ID, ProjectID: "core", Repository: observation.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	a = upgradeRoleContractAuthorityTo20(t, path, binding, owner, admission)
	worker, workerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"attempt.register", "lease.acquire", "lease.renew", "lease.release"}, Resources: []string{"project:core", "task:" + task.ID, "checkout:" + checkoutPath, "path:tracked"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	preflightExpiresAt := time.Now().Add(30 * time.Minute).UTC()
	admission.observePreflight = func(workspaceID, projectID, taskID string, spec sessions.PreflightTask, placement sessions.SwarmPlacement) (sessions.PreflightObservation, error) {
		currentRoles, err := store.ReadCheckpointRoles(w)
		if err != nil {
			return sessions.PreflightObservation{}, err
		}
		byID := map[string]team.Role{}
		for _, current := range currentRoles {
			byID[current.Name] = current
		}
		provenance, err := admission.validateRoleRegistry(projectID, currentRoles, time.Now().UTC())
		if err != nil {
			return sessions.PreflightObservation{}, err
		}
		obs := sessions.PreflightObservation{AuthorityPath: path, InstallationID: binding.Installation, Harness: "codex", TaskDigest: strings.Repeat("a", 64), RosterDigest: strings.Repeat("b", 64), ClaimsDigest: strings.Repeat("c", 64), ProfileDigest: strings.Repeat("d", 64), ClaimResources: []string{"path:tracked"}, Required: 2, Sized: true, Implementation: byID[spec.ImplementationRole], Review: byID[spec.ReviewRole], Recovery: byID[spec.RecoveryRole], Worker: sessions.PreflightActor{SessionID: worker.ID, CredentialHash: sha256Hex(workerToken)}, RoleRegistry: provenance, ExpiresAt: preflightExpiresAt, Phases: readyPreflightPhases()}
		return obs, nil
	}
	attempt, err := a.RegisterAttempt(workerToken, worker.ID, sessions.AttemptRequest{RequestID: "role-contract-worker", TaskID: task.ID, Checkout: checkoutPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ConfigureSwarm(owner, sessions.SwarmPolicy{ProjectID: "core", Revision: "role-contract-policy", Harness: "codex", CapabilityDigest: capabilityDigestText, Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "builder", Kind: "implement", Harness: "codex", Capacity: 1}, {ID: "reviewer", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{task.ID: {Checkout: checkoutPath, ImplementKind: "implement", ReviewKind: "review"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfigurePreflight(owner, sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "core", Revision: "role-contract-preflight", Harness: "codex", Rationale: "disposable cooperative boundary fixture", AcceptManualAssurance: true, ExpiresAt: preflightExpiresAt, Tasks: map[string]sessions.PreflightTask{task.ID: {ImplementationRole: "builder", ReviewRole: "reviewer", RecoveryRole: "builder", WorkerConnection: "/private/role-contract-worker.json", ContractProfile: "/private/role-contract-profile.json"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfigureLifecycle(owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "core", Revision: "role-contract-lifecycle", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "owner-manual-native-host"}); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	registryDir := filepath.Join(w.Root, ".dacli", "role-registry")
	if err := os.MkdirAll(registryDir, 0700); err != nil {
		t.Fatal(err)
	}
	keyring, err := json.Marshal(map[string][]byte{"test-publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryDir, "publishers.json"), keyring, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.NewRoleRegistry(w, map[string]ed25519.PublicKey{"test-publisher": public})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	contract := rolebundle.RoleContract{Purpose: role.Summary, Instructions: role.Prompt, CapabilityScope: rolebundle.CapabilityScope{Grant: role.Grant, Scope: role.Scope, OutOfScope: role.OutOfScope, Shortcuts: role.Shortcuts}, Runtime: rolebundle.RuntimePreference{Runtime: role.Runtime, Model: role.ModelID(), Profile: role.Profile}, Host: rolebundle.HostRequirements{Capabilities: []string{"native-swarm-admission"}, Sandbox: "cooperative", Network: "unrestricted", Filesystem: "role-scope", Process: "unrestricted"}, Budget: rolebundle.BudgetRequirement{Mode: "none"}}
	if err := installBoundaryRelease(t, w, registry, role, contract, "2.0.0", private, now, true); err != nil {
		t.Fatal(err)
	}
	controller, controllerToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: []string{"project:core", "task:" + task.ID, "checkout:" + checkoutPath, "path:tracked"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	bridge, bridgeToken, err := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "dispatch.correlate"}, Resources: []string{"project:core", "task:" + task.ID, "checkout:" + checkoutPath, "path:tracked"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.PlanSwarm(controllerToken, controller.ID, "core")
	if err != nil {
		t.Fatal(err)
	}
	wave, err := a.ReserveWave(controllerToken, controller.ID, sessions.ReserveWaveRequest{OperationKey: "role-contract-wave", Plan: plan, ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil || len(wave.Dispatches) != 1 {
		t.Fatalf("role contract dispatch reservation: plan=%+v preflight=%+v dispatches=%d err=%v", plan.Verdicts, plan.Verdicts[0].Preflight, len(wave.Dispatches), err)
	}
	return roleContractBoundaryFixture{w: w, a: a, owner: owner, controllerToken: controllerToken, controller: controller, bridge: bridge, bridgeToken: bridgeToken, worker: worker, workerToken: workerToken, attempt: attempt, dispatch: wave.Dispatches[0], registry: registry, public: public, private: private, role: role, admission: admission, capabilityPath: capabilityPath, capabilityDigest: capabilityDigestText}
}

func installBoundaryRelease(t *testing.T, w *workspace.Workspace, registry *store.RoleRegistry, role team.Role, contract rolebundle.RoleContract, version string, private ed25519.PrivateKey, now time.Time, pin bool) error {
	t.Helper()
	var generation uint64 = 1
	var previousStatusDigest string
	if pins, err := registry.ReadPins("core"); err != nil {
		return err
	} else if current, ok := pins.Pins[role.Name]; ok {
		previous, err := registry.LifecycleForRelease(role.Name, current.Version, current.Digest, now)
		if err != nil {
			return err
		}
		previousStatusDigest, err = rolebundle.LifecycleDigest(previous)
		if err != nil {
			return err
		}
		generation = previous.Generation + 1
	}
	release, err := rolebundle.Sign(rolebundle.ReleaseContent{SchemaVersion: rolebundle.ReleaseContractSchemaVersion, Version: version, Role: role, Contract: &contract}, "test-publisher", private)
	if err != nil {
		return err
	}
	if err := registry.StoreRelease(release); err != nil {
		return err
	}
	status, err := rolebundle.SignStatus(rolebundle.LifecycleStatus{SchemaVersion: 1, RoleID: role.Name, Version: version, ContentDigest: release.ContentDigest, Generation: generation, PreviousStatusDigest: previousStatusDigest, State: "active", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PublisherKeyID: "test-publisher"}, private)
	if err != nil {
		return err
	}
	if err := registry.StoreLifecycle(status, now); err != nil {
		return err
	}
	if pin {
		if _, err := registry.Pin("core", role.Name, version, release.ContentDigest, "owner", "pin signed schema-2 contract fixture", now); err != nil {
			return err
		}
	}
	return nil
}

func replaceBoundaryRelease(t *testing.T, f *roleContractBoundaryFixture, failure string) error {
	contract := rolebundle.RoleContract{Purpose: f.role.Summary, Instructions: f.role.Prompt, CapabilityScope: rolebundle.CapabilityScope{Grant: f.role.Grant, Scope: f.role.Scope, OutOfScope: f.role.OutOfScope, Shortcuts: f.role.Shortcuts}, Runtime: rolebundle.RuntimePreference{Runtime: f.role.Runtime, Model: f.role.ModelID(), Profile: f.role.Profile}, Host: rolebundle.HostRequirements{Capabilities: []string{"native-swarm-admission"}, Sandbox: "cooperative", Network: "unrestricted", Filesystem: "role-scope", Process: "unrestricted"}, Budget: rolebundle.BudgetRequirement{Mode: "none"}}
	switch failure {
	case "unsupported-network-containment":
		contract.Host.Network = "disabled"
	case "missing-required-preflight-policy":
		contract.Host.Capabilities = append(contract.Host.Capabilities, "native-policy-observation")
		contract.Gates = []string{"preflight"}
	default:
		contract.Scenarios = []rolebundle.Scenario{{ID: "changed-contract", RequestedActions: []string{"dispatch"}, AllowedActions: []string{"dispatch"}, BudgetState: "valid", Escalation: "none", ExpectedOutcome: "allowed"}}
	}
	return installBoundaryRelease(t, f.w, f.registry, f.role, contract, "2.0.1", f.private, time.Now().UTC().Truncate(time.Second), true)
}

func prepareRoleContractAcknowledgement(t *testing.T, f *roleContractBoundaryFixture, prefix string) sessions.DispatchIntent {
	t.Helper()
	d := f.dispatch
	requested, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: prefix + "-request", DispatchID: d.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
	if err != nil {
		t.Fatalf("supported signed v2 contract refused intent: %v", err)
	}
	intent, err := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, sessions.CorrelateDispatchIntent{OperationKey: prefix + "-correlate", DispatchID: d.ID, IntentID: requested.Intent.ID, HostAgentID: "fixture-host", State: "host_correlated", ExpectedFence: d.Reservation.Fence, ExpectedSequence: requested.Intent.Sequence})
	if err != nil {
		t.Fatalf("supported signed v2 contract refused correlation: %v", err)
	}
	return intent
}

func measureSupportedBoundaryCapabilityCalls(t *testing.T, boundary string) []string {
	t.Helper()
	f := setupRoleContractBoundary(t)
	d := f.dispatch
	var intent sessions.DispatchIntent
	if boundary == "acknowledgement" {
		intent = prepareRoleContractAcknowledgement(t, &f, "measure-ack")
	}
	f.admission.capabilityCalls = nil
	switch boundary {
	case "dispatch":
		result, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "measure-dispatch", DispatchID: d.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
		if err != nil || !result.HostActionAuthorized || result.Intent.ID == "" {
			t.Fatalf("supported schema-2 dispatch control failed: %+v err=%v", result, err)
		}
	case "handoff":
		lease, err := f.a.HandoffDispatch(f.workerToken, f.worker.ID, sessions.HandoffDispatchRequest{OperationKey: "measure-handoff", DispatchID: d.ID, AttemptID: f.attempt.ID, ExpectedFence: d.Reservation.Fence})
		if err != nil || lease.ID == "" {
			t.Fatalf("supported schema-2 handoff control failed: %+v err=%v", lease, err)
		}
	case "acknowledgement":
		lease, err := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, sessions.AcknowledgeDispatchIntent{OperationKey: "measure-ack-final", DispatchID: d.ID, IntentID: intent.ID, AttemptID: f.attempt.ID, HostAgentID: intent.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: intent.Sequence})
		if err != nil || lease.Lease == nil || lease.Lease.ID == "" {
			t.Fatalf("supported schema-2 acknowledgement control failed: %+v err=%v", lease, err)
		}
	default:
		t.Fatalf("unknown public boundary %q", boundary)
	}
	if len(f.admission.capabilityCalls) == 0 {
		t.Fatalf("supported schema-2 %s control observed no capability callbacks", boundary)
	}
	return append([]string(nil), f.admission.capabilityCalls...)
}

func TestSignedV2UnsupportedContractReplacementRefusesAllFinalEffectBoundaries(t *testing.T) {
	controlTraces := map[string][]string{}
	for _, boundary := range []string{"dispatch", "handoff", "acknowledgement"} {
		controlTraces[boundary] = measureSupportedBoundaryCapabilityCalls(t, boundary)
	}
	for _, boundary := range []string{"dispatch", "handoff", "acknowledgement"} {
		for _, failure := range []string{"unsupported-network-containment", "missing-required-preflight-policy"} {
			t.Run(boundary+"/"+failure, func(t *testing.T) {
				f := setupRoleContractBoundary(t)
				d := f.dispatch
				mutate := func() error { return replaceBoundaryRelease(t, &f, failure) }
				switch boundary {
				case "dispatch":
					f.admission.capabilityCalls = nil
					f.admission.mutateAtCapabilityCall = len(controlTraces[boundary])
					f.admission.mutate = mutate
					result, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "blocked-dispatch", DispatchID: d.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
					if err == nil || result.HostActionAuthorized || result.Intent.ID != "" {
						t.Fatalf("unsupported signed v2 contract still permitted dispatch: %+v err=%v", result, err)
					}
				case "handoff":
					f.admission.capabilityCalls = nil
					f.admission.mutateAtCapabilityCall = len(controlTraces[boundary])
					f.admission.mutate = mutate
					lease, err := f.a.HandoffDispatch(f.workerToken, f.worker.ID, sessions.HandoffDispatchRequest{OperationKey: "blocked-handoff", DispatchID: d.ID, AttemptID: f.attempt.ID, ExpectedFence: d.Reservation.Fence})
					if err == nil || lease.ID != "" {
						t.Fatalf("unsupported signed v2 contract still handed off lease: %+v err=%v", lease, err)
					}
				case "acknowledgement":
					intent := prepareRoleContractAcknowledgement(t, &f, "before-blocked-ack")
					f.admission.capabilityCalls = nil
					f.admission.mutateAtCapabilityCall = len(controlTraces[boundary])
					f.admission.mutate = mutate
					lease, err := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, sessions.AcknowledgeDispatchIntent{OperationKey: "blocked-ack", DispatchID: d.ID, IntentID: intent.ID, AttemptID: f.attempt.ID, HostAgentID: intent.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: intent.Sequence})
					if err == nil || lease.Lease != nil {
						t.Fatalf("unsupported signed v2 contract still acknowledged lease: %+v err=%v", lease, err)
					}
				}
				if f.admission.err != nil {
					t.Fatalf("mutation callback itself failed: %v", f.admission.err)
				}
				if f.admission.mutationCapabilityCall != len(controlTraces[boundary]) || !reflect.DeepEqual(f.admission.capabilityCalls, controlTraces[boundary]) {
					t.Fatalf("contract replacement did not occur at the measured final capability callback: replacement_call=%d measured_trace=%v actual_trace=%v", f.admission.mutationCapabilityCall, controlTraces[boundary], f.admission.capabilityCalls)
				}
			})
		}
	}
}

func TestSignedV2SupportedContractAllowsAllFinalEffectBoundaries(t *testing.T) {
	for _, boundary := range []string{"dispatch", "handoff", "acknowledgement"} {
		t.Run(boundary, func(t *testing.T) {
			f := setupRoleContractBoundary(t)
			d := f.dispatch
			switch boundary {
			case "dispatch":
				result, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "supported-dispatch", DispatchID: d.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
				if err != nil || !result.HostActionAuthorized || result.Intent.ID == "" {
					t.Fatalf("supported signed v2 contract refused dispatch: %+v err=%v", result, err)
				}
			case "handoff":
				lease, err := f.a.HandoffDispatch(f.workerToken, f.worker.ID, sessions.HandoffDispatchRequest{OperationKey: "supported-handoff", DispatchID: d.ID, AttemptID: f.attempt.ID, ExpectedFence: d.Reservation.Fence})
				if err != nil || lease.ID == "" {
					t.Fatalf("supported signed v2 contract refused handoff: %+v err=%v", lease, err)
				}
			case "acknowledgement":
				requested, err := f.a.RequestDispatchIntent(f.controllerToken, f.controller.ID, sessions.RequestDispatchIntent{OperationKey: "supported-ack-request", DispatchID: d.ID, BridgeSessionID: f.bridge.ID, WorkerSessionID: f.worker.ID, PromptSpecDigest: "sha256:" + strings.Repeat("a", 64), ExpectedFence: d.Reservation.Fence})
				if err != nil {
					t.Fatalf("supported signed v2 contract refused intent: %v", err)
				}
				intent, err := f.a.CorrelateDispatchIntent(f.bridgeToken, f.bridge.ID, sessions.CorrelateDispatchIntent{OperationKey: "supported-ack-correlation", DispatchID: d.ID, IntentID: requested.Intent.ID, HostAgentID: "fixture-host", State: "host_correlated", ExpectedFence: d.Reservation.Fence, ExpectedSequence: requested.Intent.Sequence})
				if err != nil {
					t.Fatalf("supported signed v2 contract refused correlation: %v", err)
				}
				lease, err := f.a.AcknowledgeDispatchIntent(f.workerToken, f.worker.ID, sessions.AcknowledgeDispatchIntent{OperationKey: "supported-ack", DispatchID: d.ID, IntentID: intent.ID, AttemptID: f.attempt.ID, HostAgentID: intent.HostAgentID, ExpectedFence: d.Reservation.Fence, ExpectedSequence: intent.Sequence})
				if err != nil || lease.Lease == nil || lease.Lease.ID == "" {
					t.Fatalf("supported signed v2 contract refused acknowledgement: %+v err=%v", lease, err)
				}
			}
		})
	}
}

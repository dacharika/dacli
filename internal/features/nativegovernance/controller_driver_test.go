package nativegovernance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/driver"
	"github.com/mlnomadpy/dacli/internal/governance/executor"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

type actualDriverFixture struct {
	seed                                                     verificationFixture
	private, profile, workerCheckout, workerFile, capability string
	controller                                               *application.NativeControllerAccess
	worker                                                   native.Connection
	workerToken                                              string
	initial                                                  sessions.ControllerStatus
	template                                                 executor.Dispatch
	enrollment                                               executor.WorkerEnrollment
	host                                                     *actualDriverHost
	tasks                                                    []actualDriverTask
}

// This fixture uses the real canonical observer and private production profiles.
// Its fake host is explicit; no provider or coding-host process is launched.
func setupActualDriver(t *testing.T) actualDriverFixture { return setupActualDriverTasks(t, 1) }

type actualDriverTask struct {
	task                                                   *store.Task
	checkout, workerFile, workerToken, marker, claimedFile string
	worker                                                 native.Connection
	enrollment                                             executor.WorkerEnrollment
	template                                               executor.Dispatch
}

func setupActualDriverTasks(t *testing.T, count int) actualDriverFixture {
	return setupActualDriverVersion(t, count, 16)
}

func setupActualDriverVersion(t *testing.T, count, version int, failingCheck ...bool) actualDriverFixture {
	t.Helper()
	if count < 1 || count > 2 {
		t.Fatal("fixture supports one or two disjoint tasks")
	}
	f := setupVerification(t, "", true)
	if _, err := f.authority.ReleaseLease(f.workerToken, f.worker.SessionID, sessions.ReleaseLeaseRequest{OperationKey: "release-initial", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence}); err != nil {
		t.Fatal(err)
	}
	tasks := []*store.Task{f.task}
	for i := 1; i < count; i++ {
		task, err := store.CreateTask(f.w, "root", "p", "Implement second isolated native driver task", store.TaskOpts{})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for i, task := range tasks {
		if err := store.SetEstimate(f.w, task, "5,8,11"); err != nil {
			t.Fatal(err)
		}
		claim := fmt.Sprintf("src/task%d", i)
		task.Doc.Front.SetList("claims", []string{claim})
		task.Doc.SetSection("Acceptance", "- [ ] fixture command evidence")
		if err := store.SaveTask(task); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.w.Root, claim), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.w.Root, claim, "tracked"), []byte("base"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []team.Role{{Name: "implementation", Kind: "implementer", Runtime: "codex", Model: "model", Grant: "rw", MaxPoints: 13}, {Name: "review", Kind: "reviewer", Runtime: "codex", Model: "model", Grant: "ro", MaxPoints: 13}} {
		if err := store.CreateRole(f.w, "owner", role); err != nil {
			t.Fatal(err)
		}
	}
	verificationGit(t, f.w.Root, "rm", "-r", "--cached", "--ignore-unmatch", ".dacli")
	exclude := filepath.Join(f.w.Root, ".git", "info", "exclude")
	old, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(exclude, append(old, []byte("\n.dacli/\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, f.w.Root, "add", ".")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "isolated driver roles and disjoint task files")
	private := t.TempDir()
	probe := dispatchProbeFixture(t)
	probeFile := filepath.Join(private, "probe.json")
	if err = os.WriteFile(probeFile, probe, 0600); err != nil {
		t.Fatal(err)
	}
	probeSHA := sha256.Sum256(probe)
	capability := filepath.Join(private, "capability.json")
	verificationJSON(t, capability, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Codex: &application.CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probeFile, ProbeSHA256: hex.EncodeToString(probeSHA[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	raw, err := os.ReadFile(capability)
	if err != nil {
		t.Fatal(err)
	}
	capSHA := sha256.Sum256(raw)
	if err = f.authority.UpgradeSwarms(f.owner); err != nil {
		t.Fatal(err)
	}
	swarm := sessions.SwarmPolicy{ProjectID: "p", Revision: "swarm", Harness: "codex", CapabilityDigest: "sha256:" + hex.EncodeToString(capSHA[:]), Width: count, WIP: count, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implementation", Kind: "implementation", Harness: "codex", Capacity: count}, {ID: "review", Kind: "review", Harness: "codex", Capacity: count}}, Placements: map[string]sessions.SwarmPlacement{}}
	selected := make([]actualDriverTask, 0, count)
	for i, task := range tasks {
		wt := filepath.Join(t.TempDir(), "worker")
		verificationGit(t, f.w.Root, "worktree", "add", "--detach", wt, "HEAD")
		wt, err = filepath.EvalSymlinks(wt)
		if err != nil {
			t.Fatal(err)
		}
		claim := fmt.Sprintf("src/task%d", i)
		workerFile := filepath.Join(private, fmt.Sprintf("worker%d.json", i))
		if out, e := verificationRun(t, f.w, enroll, "--config", f.config, "--task", task.ID, "--connection", workerFile, "--credential", filepath.Join(private, fmt.Sprintf("worker%d.token", i)), "--path", claim, "--checkout", wt, "--verification"); e != nil {
			t.Fatal(out, e)
		}
		var worker native.Connection
		if err = privateJSON(workerFile, &worker); err != nil {
			t.Fatal(err)
		}
		token, e := native.ReadCredential(worker.CredentialFile)
		if e != nil {
			t.Fatal(e)
		}
		selected = append(selected, actualDriverTask{task: task, checkout: wt, workerFile: workerFile, workerToken: token, worker: worker, marker: filepath.Join(private, fmt.Sprintf("count%d", i)), claimedFile: claim + "/tracked"})
		swarm.Placements[task.ID] = sessions.SwarmPlacement{Checkout: wt, ImplementKind: "implementation", ReviewKind: "review"}
	}
	if err = f.authority.ConfigureSwarm(f.owner, swarm); err != nil {
		t.Fatal(err)
	}
	for _, upgrade := range []func(string) error{f.authority.UpgradeDispatchIntents, f.authority.UpgradeLifecycle, f.authority.UpgradeRecovery} {
		if err = upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := checkout.Observe(f.w.Root)
	if err != nil {
		t.Fatal(err)
	}
	preflight := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "preflight", Harness: "codex", Rationale: "explicit fixture model and manual phase contracts", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(30 * time.Minute), Tasks: map[string]sessions.PreflightTask{}}
	budget := sessions.BudgetPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "budget", CycleID: "cycle-one", Harness: "codex", Rationale: "owner-estimated fixture tokens; actual provider usage unavailable", CycleLimit: int64(100 * count), WindowLimit: int64(100 * count), RecoveryFloor: 10, Window: time.Hour, WindowOrigin: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Tasks: map[string]sessions.BudgetTask{}}
	if err = f.authority.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "p", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "owner-manual-native-host", CancellationEnabled: true}); err != nil {
		t.Fatal(err)
	}
	profileTasks := []any{}
	routes := map[string]sessions.ControllerTaskRoute{}
	ids := []string{}
	for i := range selected {
		task := &selected[i]
		id := task.task.ID
		ids = append(ids, id)
		runnerFile := filepath.Join(private, fmt.Sprintf("runner%d.json", i))
		checksFile := filepath.Join(private, fmt.Sprintf("checks%d.json", i))
		declarations := []checkDeclaration{{ID: "unit", Command: "printf ran >> '" + task.marker + "'", Deadline: "4s"}}
		if len(failingCheck) > 0 && failingCheck[0] {
			declarations[0].Command += "; exit 7"
		}
		if count == 2 {
			declarations = append(declarations, checkDeclaration{ID: "lint", Command: "printf lint >> '" + task.marker + "'", Deadline: "4s"})
		}
		verificationJSON(t, checksFile, checksConfig{SchemaVersion: 1, Checks: declarations})
		if out, e := verificationRun(t, f.w, verifierAdd, "--config", f.config, "--task", id, "--checks-file", checksFile, "--out", runnerFile, "--credential", filepath.Join(private, fmt.Sprintf("runner%d.token", i))); e != nil {
			t.Fatal(out, e)
		}
		var runner runnerProfile
		if err = privateJSON(runnerFile, &runner); err != nil {
			t.Fatal(err)
		}
		contract := filepath.Join(private, fmt.Sprintf("contract%d.json", i))
		verificationJSON(t, contract, application.NativePhaseContract{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", TaskID: id, Harness: "codex", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Models: []string{"model"}, ReviewOutputContract: "native-review-v1", IntegrationRoute: "owner-manual-local", IntegrationCheckout: f.w.Root, IntegrationBase: observed.Commit, IntegrationPolicyDigest: strings.Repeat("a", 64), ExternalStatus: "ready"})
		reviewerFile := filepath.Join(private, fmt.Sprintf("reviewer%d.json", i))
		reviewConfig := filepath.Join(private, fmt.Sprintf("review-config%d.json", i))
		if out, e := verificationRun(t, f.w, reviewerAdd, "--config", f.config, "--task", id, "--worker-session", task.worker.SessionID, "--runner-config", runnerFile, "--policy-revision", "review-policy", "--policy-out", filepath.Join(private, fmt.Sprintf("review-policy%d.json", i)), "--review-config", reviewConfig, "--connection", reviewerFile, "--credential", filepath.Join(private, fmt.Sprintf("reviewer%d.token", i))); e != nil {
			t.Fatal(out, e)
		}
		var reviewer native.Connection
		var rp application.NativeReviewerProfile
		if err = privateJSON(reviewerFile, &reviewer); err != nil {
			t.Fatal(err)
		}
		if err = privateJSON(reviewConfig, &rp); err != nil {
			t.Fatal(err)
		}
		preflight.Tasks[id] = sessions.PreflightTask{ImplementationRole: "implementation", ReviewRole: "review", RecoveryRole: "implementation", WorkerConnection: task.workerFile, RunnerProfile: runnerFile, ContractProfile: contract, ReviewerProfile: reviewConfig, RequireReview: true, RequireVerification: true, RequireIntegration: true, OwnerAuthorizedIntegration: true}
		phases := map[string]sessions.BudgetEstimate{}
		for phase, tokens := range map[string]int64{"implementation": 20, "review": 10, "correction": 5, "re-review": 5, "verification": 0, "integration": 10, "recovery": 5} {
			role := "implementation"
			if phase == "review" || phase == "re-review" {
				role = "review"
			}
			phases[phase] = sessions.BudgetEstimate{RoleID: role, Model: "model", Tokens: tokens, Provenance: "owner-estimate", ObservedAt: time.Now()}
		}
		budget.Tasks[id] = sessions.BudgetTask{Phases: phases, IntegrationOperationID: "integration-contract-pending"}
		bridgeFile := filepath.Join(private, fmt.Sprintf("bridge%d.json", i))
		if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return swarmScopedActorAdd(c, a, true, true) }, "--config", f.config, "--project", "p", "--connection", bridgeFile, "--credential", filepath.Join(private, fmt.Sprintf("bridge%d.token", i))); e != nil {
			t.Fatal(out, e)
		}
		var bridge native.Connection
		if err = privateJSON(bridgeFile, &bridge); err != nil {
			t.Fatal(err)
		}
		actor, e := f.authority.Authenticate(task.workerToken, task.worker.SessionID)
		if e != nil {
			t.Fatal(e)
		}
		profileRef := executor.ProfileRef{ID: fmt.Sprintf("selected-driver-profile%d", i), WorkerSession: task.worker.SessionID, ScopeDigest: "sha256:fixture-scope", ExpiresAt: actor.ExpiresAt}
		enrollment := executor.WorkerEnrollment{Binding: executor.Binding{Workspace: f.w.ID, Project: "p", Task: id, Host: "codex", Session: task.worker.SessionID, Instance: fmt.Sprintf("fixture-host-instance%d", i), CredentialID: profileRef.ID, ExpiresAt: actor.ExpiresAt}, Profile: profileRef, AttemptRequest: sessions.AttemptRequest{RequestID: fmt.Sprintf("stable-driver-attempt%d", i), TaskID: id, Checkout: task.checkout}}
		authenticated, e := executor.AuthenticateWorker(f.authority, task.workerToken, enrollment, time.Now())
		if e != nil {
			t.Fatal(e)
		}
		enrollment.Binding = authenticated.Claim()
		task.enrollment = enrollment
		template := executor.Dispatch{Version: 1, Binding: authenticated.Claim(), Profile: profileRef, Repository: observed.Repository, Checkout: task.checkout, Prompt: "Implement the isolated native driver fixture task"}
		template.PromptSpecDigest = executor.DispatchSpecDigest(template)
		task.template = template
		routes[id] = sessions.ControllerTaskRoute{BridgeSession: bridge.SessionID, WorkerSession: task.worker.SessionID, RunnerSession: runner.RunnerSessionID, ReviewerSession: reviewer.SessionID, VerificationGrantID: runner.Checks[0].GrantID, ReviewGrantID: rp.GrantID, CheckID: "unit", PromptSpecDigest: template.PromptSpecDigest}
		route := routes[id]
		for _, check := range runner.Checks {
			route.Checks = append(route.Checks, sessions.ControllerCheckRoute{CheckID: check.ID, GrantID: check.GrantID})
		}
		routes[id] = route
		profileTasks = append(profileTasks, map[string]any{"task_id": id, "worker_connection": task.workerFile, "bridge_connection": bridgeFile, "worker_enrollment": enrollment, "dispatch_template": template, "runner_profile": runnerFile, "reviewer_connection": reviewerFile, "reviewer_profile": reviewConfig})
	}

	controllerFile := filepath.Join(private, "controller.json")
	var conn native.Connection
	var controller *application.NativeControllerAccess
	initial := sessions.ControllerStatus{ID: "driver-fixture", CheckpointID: "driver-checkpoint", Sequence: 1}
	if version == 10 {
		resources := []string{"workspace:" + f.w.ID, "project:p"}
		for _, task := range selected {
			resources = append(resources, "task:"+task.task.ID)
		}
		actor, token, e := f.authority.Enroll(f.owner, sessions.Scope{Actions: []string{"read", "controller.read", "controller.step", "controller.stop"}, Resources: resources}, time.Now().Add(time.Hour))
		if e != nil {
			t.Fatal("explicit legacy controller reader enrollment", e)
		}
		credential := filepath.Join(private, "controller.token")
		if e = os.WriteFile(credential, []byte(token+"\n"), 0600); e != nil {
			t.Fatal(e)
		}
		conn = selected[0].worker
		conn.SessionID = actor.ID
		conn.CredentialFile = credential
		verificationJSON(t, controllerFile, conn)
		controller, err = application.OpenNativeController(controllerFile, capability)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		for _, upgrade := range []func(string) error{f.authority.UpgradePreflight, f.authority.UpgradeCheckpoints, f.authority.UpgradeBudgets} {
			if err = upgrade(f.owner); err != nil {
				t.Fatal(err)
			}
		}
		if err = f.authority.ConfigurePreflight(f.owner, preflight); err != nil {
			t.Fatal(err)
		}
		budgetFile := filepath.Join(private, "budget.json")
		verificationJSON(t, budgetFile, budget)
		if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, a []string) error { return budgetOwner(c, a, "configure") }, "--config", f.config, "--request", budgetFile, "--json"); e != nil {
			t.Fatal(out, e)
		}
		for _, upgrade := range []func(string) error{f.authority.UpgradeNativeAcceptance, f.authority.UpgradeNativePolicy, f.authority.UpgradeController} {
			if err = upgrade(f.owner); err != nil {
				t.Fatal(err)
			}
		}
		if err = f.authority.ConfigureCheckpoints(f.owner, sessions.CheckpointPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "checkpoint", Harness: "codex", TaskIDs: ids, MaxCycles: 5, MaxNoProgress: 5, ExpiresAt: time.Now().Add(30 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		owner, err := application.OpenNativeControllerOwner(f.config)
		if err != nil {
			t.Fatal(err)
		}
		policy := sessions.ControllerPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "controller", Harness: "codex", CapabilityDigest: swarm.CapabilityDigest, TaskIDs: ids, MaxWaves: 5, MaxConcurrency: count, MaxCorrectionRounds: 2, MaxNoProgress: 5, TransientRetries: 1, MaxElapsed: 10 * time.Minute, RetryBackoff: time.Millisecond, ExpiresAt: time.Now().Add(20 * time.Minute)}
		if err = owner.Configure(policy); err != nil {
			t.Fatal(err)
		}
		if out, e := verificationRun(t, f.w, controllerEnroll, "--config", f.config, "--project", "p", "--connection", controllerFile, "--credential", filepath.Join(private, "controller.token")); e != nil {
			t.Fatal(out, e)
		}
		if err = privateJSON(controllerFile, &conn); err != nil {
			t.Fatal(err)
		}
		controller, err = application.OpenNativeController(controllerFile, capability)
		if err != nil {
			t.Fatal(err)
		}
		initial, err = owner.Start(sessions.ControllerStartRequest{OperationKey: "driver-start", InvocationID: "driver-fixture", ProjectID: "p", PolicyRevision: "controller", ControllerSessionID: conn.SessionID, CheckpointID: "driver-checkpoint", Routes: routes})
		if err != nil {
			t.Fatal("actual driver fixture Start", err)
		}

	}
	profile := filepath.Join(private, "driver.json")
	verificationJSON(t, profile, map[string]any{"schema_version": 1, "workspace_id": f.w.ID, "installation_id": conn.InstallationID, "project_id": "p", "invocation_id": initial.ID, "checkpoint_id": initial.CheckpointID, "controller_connection": controllerFile, "capability_file": capability, "limits": map[string]any{"max_transitions": uint64(64), "max_effects": uint64(128), "max_work": uint64(2048), "max_no_progress": uint64(5), "max_elapsed": int64(10 * time.Minute), "operation_timeout": int64(30 * time.Second), "max_sequence": uint64(128)}, "tasks": profileTasks})
	host := &actualDriverHost{bindings: map[string]executor.Binding{}, results: map[string]executor.HostResult{}, observed: map[string]uint64{}, state: "running"}
	first := selected[0]
	f.marker = first.marker
	return actualDriverFixture{seed: f, private: private, profile: profile, workerCheckout: first.checkout, workerFile: first.workerFile, capability: capability, controller: controller, worker: first.worker, workerToken: first.workerToken, initial: initial, template: first.template, enrollment: first.enrollment, host: host, tasks: selected}
}

type actualDriverHost struct {
	sync.Mutex
	bindings                                                    map[string]executor.Binding
	results                                                     map[string]executor.HostResult
	cancelResults                                               map[string]string
	observed                                                    map[string]uint64
	state                                                       string
	dispatches, registers, lookups, observations, cancellations int
	loseResponse, lookupMiss                                    bool
	loseCancelResponse, cancelLookupMiss                        bool
	hook                                                        func(context.Context, string) error
}

func (h *actualDriverHost) callback(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.hook != nil {
		return h.hook(ctx, stage)
	}
	return nil
}

func (h *actualDriverHost) CapabilitiesContext(ctx context.Context) (executor.Capabilities, error) {
	if err := h.callback(ctx, "capabilities"); err != nil {
		return executor.Capabilities{}, err
	}
	yes := executor.Capability{State: executor.Supported}
	return executor.Capabilities{Version: 1, Host: "codex", Model: yes, Dispatch: yes, Lookup: yes, PrivateProfileHandoff: yes, Observe: yes, Cancel: yes}, nil
}
func (h *actualDriverHost) RegisterContext(ctx context.Context, r executor.Registration) error {
	if err := h.callback(ctx, "register"); err != nil {
		return err
	}
	h.Lock()
	defer h.Unlock()
	h.registers++
	h.bindings[r.Binding.Task] = r.Binding
	return nil
}
func (h *actualDriverHost) VerifyBindingContext(ctx context.Context, b executor.Binding) error {
	if err := h.callback(ctx, "verify_binding"); err != nil {
		return err
	}
	h.Lock()
	defer h.Unlock()
	if h.bindings[b.Task] != b {
		return executor.ErrDenied
	}
	return nil
}
func (h *actualDriverHost) DispatchContext(ctx context.Context, d executor.Dispatch) (executor.HostResult, error) {
	if err := h.callback(ctx, "dispatch"); err != nil {
		return executor.HostResult{}, err
	}
	h.Lock()
	defer h.Unlock()
	h.dispatches++
	if r, ok := h.results[d.OperationKey]; ok {
		return r, nil
	}
	r := executor.HostResult{Version: 1, OperationKey: d.OperationKey, DispatchID: d.DispatchID, HostExecutionID: "fixture-host-" + d.DispatchID, State: "accepted", Repository: d.Repository, Checkout: d.Checkout, Binding: d.Binding}
	h.results[d.OperationKey] = r
	if h.loseResponse {
		h.loseResponse = false
		return executor.HostResult{}, errors.New("fixture lost dispatch response after effect")
	}
	return r, nil
}
func (h *actualDriverHost) LookupContext(ctx context.Context, key string, b executor.Binding) (executor.HostResult, error) {
	if err := h.callback(ctx, "lookup"); err != nil {
		return executor.HostResult{}, err
	}
	h.Lock()
	defer h.Unlock()
	h.lookups++
	r, ok := h.results[key]
	if !ok || h.lookupMiss || r.Binding != b {
		return executor.HostResult{}, executor.ErrUncertain
	}
	return r, nil
}
func (h *actualDriverHost) ReadObservationContext(ctx context.Context, key string, b executor.Binding, after uint64) (executor.Observation, error) {
	if err := h.callback(ctx, "observe"); err != nil {
		return executor.Observation{}, err
	}
	h.Lock()
	defer h.Unlock()
	h.observations++
	r, ok := h.results[key]
	if !ok || r.Binding != b {
		return executor.Observation{}, executor.ErrUncertain
	}
	seq := h.observed[key]
	if seq <= after {
		seq = after + 1
	}
	h.observed[key] = seq
	return executor.Observation{Version: 1, OperationKey: key, Binding: b, Sequence: seq, State: h.state, SourceTime: time.Now(), EvidenceDigest: sessions.OutputDigest([]byte("independent fake host observation"))}, nil
}
func (h *actualDriverHost) CancelContext(ctx context.Context, r executor.CancellationRequest) (string, error) {
	if err := h.callback(ctx, "cancel"); err != nil {
		return "", err
	}
	h.Lock()
	defer h.Unlock()
	h.cancellations++
	if h.cancelResults == nil {
		h.cancelResults = map[string]string{}
	}
	h.cancelResults[r.OperationKey] = "acknowledged"
	if h.loseCancelResponse {
		h.loseCancelResponse = false
		return "", errors.New("fixture lost cancellation response after effect")
	}
	return "acknowledged", nil
}
func (h *actualDriverHost) LookupCancellationContext(ctx context.Context, r executor.CancellationRequest) (string, error) {
	if err := h.callback(ctx, "lookup_cancellation"); err != nil {
		return "", err
	}
	h.Lock()
	defer h.Unlock()
	if result, ok := h.cancelResults[r.OperationKey]; ok && !h.cancelLookupMiss {
		return result, nil
	}
	return "", executor.ErrUncertain
}

type actualDriverDecisions struct {
	outcome string
	calls   int
}

func (s *actualDriverDecisions) ReviewDecision(ctx context.Context, a sessions.ControllerAction) (application.NativeReviewRequest, error) {
	if err := ctx.Err(); err != nil {
		return application.NativeReviewRequest{}, err
	}
	s.calls++
	return application.NativeReviewRequest{OperationKey: a.OperationKey, AttemptID: a.AttemptID, LeaseID: a.LeaseID, RevisionID: a.RevisionID, ExpectedFence: a.Fence, Outcome: s.outcome, Rationale: "Independent fixture reviewer inspected the exact configured check evidence", EvidenceIDs: append([]string(nil), a.EvidenceIDs...)}, nil
}
func (f actualDriverFixture) run(t *testing.T, decisions application.NativeDriverReviewDecisionSource) (driver.Result, error) {
	return f.runMode(t, decisions, "drive", "")
}

func (f actualDriverFixture) runMode(t *testing.T, decisions application.NativeDriverReviewDecisionSource, mode, dispatch string) (driver.Result, error) {
	t.Helper()
	selected, err := application.OpenNativeControllerDriver(f.profile, f.host, decisions)
	if err != nil {
		t.Fatal("open actual production driver", err)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	return selected.Run(context.Background(), driver.Request{SchemaVersion: 1, ProjectID: "p", InvocationID: f.initial.ID, CheckpointID: f.initial.CheckpointID, ExpectedSequence: status.Sequence, Mode: mode, CancelDispatchID: dispatch})
}
func TestNativeDriverActualProductionPipelineAndReopen(t *testing.T) {
	f := setupActualDriver(t)
	decisions := &actualDriverDecisions{outcome: "approve"}
	result, err := f.run(t, decisions)
	if err != nil || result.Outcome != "awaiting_external" {
		t.Fatalf("actual initial dispatch/poll boundary: %+v %v", result, err)
	}
	if f.host.dispatches != 1 || f.host.registers != 1 || f.host.observations == 0 {
		t.Fatalf("actual context host was not dispatched/polled once: %+v", f.host)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Actions) != 1 {
		t.Fatal("missing real action", status)
	}
	action := status.Actions[0]
	intent, err := f.seed.authority.DispatchIntentStatus(f.workerToken, f.worker.SessionID, action.DispatchID)
	if err != nil || intent.Lease == nil {
		t.Fatal("missing actual worker acknowledgement", intent, err)
	}
	if err = os.WriteFile(filepath.Join(f.workerCheckout, filepath.FromSlash(f.tasks[0].claimedFile)), []byte("native driver worker change"), 0600); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, f.workerCheckout, "add", f.tasks[0].claimedFile)
	verificationGit(t, f.workerCheckout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "actual native driver worker revision")
	revision, err := f.seed.authority.PublishAttemptRevision(f.workerToken, f.worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "actual-driver-publication", AttemptID: f.enrollment.Binding.Attempt, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
	if err != nil {
		t.Fatal("actual driver publication", err)
	}
	f.host.state = "completed"
	result, err = f.run(t, decisions)
	if err != nil || result.Outcome != "owner_required" {
		t.Fatalf("actual check/review/owner boundary: %+v %v", result, err)
	}
	if decisions.calls != 1 {
		t.Fatalf("review decisions=%d, want1", decisions.calls)
	}
	count, err := os.ReadFile(f.seed.marker)
	if err != nil || string(count) != "ran" {
		t.Fatalf("configured check execution count: %q %v", count, err)
	}
	status, err = f.controller.Status("p", f.initial.ID)
	if err != nil || status.Phase != "integration" || status.Actions[0].RevisionID != revision.ID || status.Terminal {
		t.Fatal("owner gate lost exact producer source", status, err)
	}
	again, err := f.run(t, decisions)
	if err != nil || again.Outcome != "owner_required" {
		t.Fatal("reopen lost owner gate", again, err)
	}
	count, err = os.ReadFile(f.seed.marker)
	if err != nil || string(count) != "ran" || f.host.dispatches != 1 || decisions.calls != 1 {
		t.Fatal("reopen repeated a producer effect", string(count), f.host.dispatches, decisions.calls, err)
	}
	storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || len(storage.Holds) == 0 {
		t.Fatal("owner gate released uncertain protection", err)
	}
	raw, err := json.Marshal(result)
	if err != nil || bytes.Contains(raw, []byte(f.private)) || bytes.Contains(raw, []byte(f.template.Prompt)) || bytes.Contains(raw, []byte(f.workerToken)) {
		t.Fatal("public driver result disclosed private selection", string(raw), err)
	}
}

func TestNativeControllerDriveReturnsPolicyExitForTypedRefusal(t *testing.T) {
	f := setupActualDriver(t)
	var profile map[string]any
	if err := privateJSON(f.profile, &profile); err != nil {
		t.Fatal(err)
	}
	profile["invocation_id"] = "unknown-controller-invocation"
	verificationJSON(t, f.profile, profile)
	if _, err := application.OpenNativeControllerDriver(f.profile, nil, nil); err != nil {
		t.Fatal("fixture must reach Run with authenticated production profiles", err)
	}
	request := filepath.Join(f.private, "unknown-invocation-request.json")
	verificationJSON(t, request, driver.Request{SchemaVersion: 1, ProjectID: "p", InvocationID: "unknown-controller-invocation", CheckpointID: f.initial.CheckpointID, ExpectedSequence: f.initial.Sequence, Mode: "drive"})
	before, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil {
		t.Fatal(err)
	}
	out, err := verificationRun(t, f.seed.w, controllerDrive, "--profile", f.profile, "--request", request, "--json")
	var result driver.Result
	if decodeErr := json.Unmarshal([]byte(out), &result); decodeErr != nil || result.Outcome != "refused" || result.Reason != "authority_refused" {
		t.Fatalf("did not exercise the typed production refusal: result=%+v decode=%v error=%v output=%s", result, decodeErr, err, out)
	}
	if code := clikit.ExitCode(err); code != 3 {
		t.Fatalf("typed policy refusal exited %d, want final refusal 3: %v", code, err)
	}
	f.assertSelectedMain(t, request, 3, "refused", "authority_refused")
	after, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || !bytes.Equal(before.PrivateBytes, after.PrivateBytes) {
		t.Fatal("refused request changed authority", err)
	}
}

func (f actualDriverFixture) publishTask(t *testing.T, task actualDriverTask) sessions.AttemptRevision {
	t.Helper()
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	var action sessions.ControllerAction
	for _, candidate := range status.Actions {
		if candidate.TaskID == task.task.ID {
			action = candidate
		}
	}
	intent, err := f.seed.authority.DispatchIntentStatus(task.workerToken, task.worker.SessionID, action.DispatchID)
	if err != nil || intent.Lease == nil {
		t.Fatal("missing actual worker acknowledgement", intent, err)
	}
	if err = os.WriteFile(filepath.Join(task.checkout, filepath.FromSlash(task.claimedFile)), []byte("actual native driver worker revision"), 0600); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, task.checkout, "add", task.claimedFile)
	verificationGit(t, task.checkout, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "actual native driver worker revision")
	revision, err := f.seed.authority.PublishAttemptRevision(task.workerToken, task.worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "publication-" + task.task.ID, AttemptID: task.enrollment.Binding.Attempt, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
	if err != nil {
		t.Fatal("publish actual revision", err)
	}
	return revision
}

func TestNativeDriverActualSharedAuthorityTwoWorkersAndRequiredChecks(t *testing.T) {
	f := setupActualDriverTasks(t, 2)
	decisions := &actualDriverDecisions{outcome: "approve"}
	result, err := f.run(t, decisions)
	if err != nil || result.Outcome != "awaiting_external" || f.host.dispatches != 2 || f.host.registers != 2 {
		t.Fatalf("two real disjoint dispatches did not reach an external wait: %+v error=%v dispatches=%d registrations=%d", result, err, f.host.dispatches, f.host.registers)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || status.Terminal || len(status.Actions) != 2 {
		t.Fatal("running workers must remain nonterminal", status, err)
	}
	for _, task := range f.tasks {
		f.publishTask(t, task)
	}
	f.host.state = "completed"
	result, err = f.run(t, decisions)
	if err != nil || result.Outcome != "owner_required" || decisions.calls != 2 {
		t.Fatalf("required check set and independent review did not reach the owner boundary: %+v error=%v decisions=%d", result, err, decisions.calls)
	}
	status, err = f.controller.Status("p", f.initial.ID)
	if err != nil || status.Phase != "integration" || status.Terminal {
		t.Fatal(status, err)
	}

	counts := map[string][]byte{}
	for _, task := range f.tasks {
		count, err := os.ReadFile(task.marker)
		if err != nil || strings.Count(string(count), "ran") != 1 || strings.Count(string(count), "lint") != 1 {
			t.Fatalf("did not execute both required checks once for %s: %q %v", task.task.ID, count, err)
		}
		counts[task.task.ID] = count
	}

	again, err := f.run(t, decisions)
	if err != nil || again.Outcome != "owner_required" || f.host.dispatches != 2 || decisions.calls != 2 {
		t.Fatal("reopen duplicated a producer or lost the owner gate", again, err)
	}
	for _, task := range f.tasks {
		after, readErr := os.ReadFile(task.marker)
		if readErr != nil || !bytes.Equal(counts[task.task.ID], after) {
			t.Fatal("reopen reran a required check", task.task.ID, readErr)
		}
	}
}

func TestNativeDriverActualLostDispatchResponseReopensOriginalKey(t *testing.T) {
	f := setupActualDriver(t)
	f.host.loseResponse, f.host.lookupMiss = true, true
	result, err := f.run(t, nil)
	if err != nil || result.Outcome != "uncertain" || f.host.dispatches != 1 || len(f.host.results) != 1 {
		t.Fatalf("lost result must retain original uncertain effect: %+v %v dispatches=%d", result, err, f.host.dispatches)
	}
	var original string
	for key := range f.host.results {
		original = key
	}
	f.host.lookupMiss = false
	result, err = f.run(t, nil)
	if err != nil || result.Outcome != "awaiting_external" || f.host.dispatches != 1 || f.host.registers != 1 || f.host.lookups == 0 || len(f.host.results) != 1 || f.host.results[original].OperationKey != original {
		t.Fatalf("disk reopen failed original-key recovery or relaunched: %+v %v dispatches=%d registrations=%d", result, err, f.host.dispatches, f.host.registers)
	}
	storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || len(storage.Holds) == 0 {
		t.Fatal("recovery released retained protection", err)
	}
}

func TestNativeDriverActualSTOPDuringHostCallbacksPreventsDispatch(t *testing.T) {
	for _, stage := range []string{"register", "verify_binding"} {
		t.Run(stage, func(t *testing.T) {
			f := setupActualDriver(t)
			stopped := false
			f.host.hook = func(ctx context.Context, observed string) error {
				if observed != stage || stopped {
					return nil
				}
				status, err := f.controller.Status("p", f.initial.ID)
				if err != nil {
					return err
				}
				var connection native.Connection
				if err = privateJSON(filepath.Join(f.private, "controller.json"), &connection); err != nil {
					return err
				}
				token, err := native.ReadCredential(connection.CredentialFile)
				if err != nil {
					return err
				}
				_, err = f.seed.authority.StopController(token, connection.SessionID, sessions.ControllerStepRequest{OperationKey: "callback-stop-" + stage, ProjectID: "p", InvocationID: f.initial.ID, ExpectedSequence: status.Sequence})
				stopped = err == nil
				return err
			}
			result, err := f.run(t, nil)
			if err != nil || !stopped || f.host.dispatches != 0 || (result.Outcome != "refused" && result.Outcome != "stopped") {
				t.Fatalf("STOP during %s still permitted a host effect: %+v error=%v stopped=%v dispatches=%d", stage, result, err, stopped, f.host.dispatches)
			}
			status, err := f.controller.Status("p", f.initial.ID)
			if err != nil || !status.Terminal || status.HaltClass != "stopped" {
				t.Fatal("durable STOP was lost", status, err)
			}
			storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
			if err != nil || len(storage.Holds) == 0 {
				t.Fatal("STOP freed uncertain writer protection", err)
			}
		})
	}
}

func TestNativeDriverActualMissingBackendDoesNotReserve(t *testing.T) {
	f := setupActualDriver(t)
	before, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := application.OpenNativeControllerDriver(f.profile, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.Run(context.Background(), driver.Request{SchemaVersion: 1, ProjectID: "p", InvocationID: f.initial.ID, CheckpointID: f.initial.CheckpointID, ExpectedSequence: f.initial.Sequence, Mode: "drive"})
	if err != nil || result.Outcome != "unsupported" {
		t.Fatalf("missing backend must return precise unsupported result: %+v %v", result, err)
	}
	after, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || !bytes.Equal(before.PrivateBytes, after.PrivateBytes) {
		t.Fatal("missing host reserved work or mutated authority", err)
	}
}

func TestNativeControllerDriveActualSchemaTenIsUnsupportedWithoutMutation(t *testing.T) {
	f := setupActualDriverVersion(t, 1, 10)
	before, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || before.AuthorityVersion != 10 {
		t.Fatal("fixture must use genuinely constructed schema10", before.AuthorityVersion, err)
	}
	request := filepath.Join(f.private, "legacy-drive-request.json")
	verificationJSON(t, request, driver.Request{SchemaVersion: 1, ProjectID: "p", InvocationID: f.initial.ID, CheckpointID: f.initial.CheckpointID, ExpectedSequence: f.initial.Sequence, Mode: "drive"})
	out, err := verificationRun(t, f.seed.w, controllerDrive, "--profile", f.profile, "--request", request, "--json")
	var result driver.Result
	if clikit.ExitCode(err) != 0 || json.Unmarshal([]byte(out), &result) != nil || result.Outcome != "unsupported" || result.Reason != "controller_schema_unsupported" {
		t.Fatalf("schema10 was not explicitly unsupported: %s %v", out, err)
	}
	f.assertSelectedMain(t, request, 0, "unsupported", "controller_schema_unsupported")
	after, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || after.AuthorityVersion != 10 || !bytes.Equal(before.PrivateBytes, after.PrivateBytes) {
		t.Fatal("unsupported driver migrated or mutated schema10", err)
	}
	if f.host.dispatches != 0 || f.host.registers != 0 || f.host.observations != 0 {
		t.Fatal("unsupported schema called the host")
	}
}

// Final qualification selects the exact frozen candidate binary explicitly.
// Ordinary package tests still exercise the handler; this extra invocation
// proves actual Main dispatch without importing one feature slice into another.
func (f actualDriverFixture) assertSelectedMain(t *testing.T, request string, code int, outcome, reason string) {
	t.Helper()
	binary := os.Getenv("DACLI_CONTROLLER_DRIVER_BINARY")
	if binary == "" {
		t.Log("exact candidate binary not selected; handler evidence only")
		return
	}
	info, err := os.Lstat(binary)
	if err != nil || !filepath.IsAbs(binary) || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatal("selected candidate must be an absolute executable regular file", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "native", "controller", "drive", "--profile", f.profile, "--request", request, "--json")
	command.Dir = f.seed.w.Root
	command.Env = store.VerificationEnvironment()
	stdout, err := command.Output()
	actual := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal("selected Main process did not complete", err)
		}
		actual = exit.ExitCode()
	}
	var result driver.Result
	if actual != code || json.Unmarshal(stdout, &result) != nil || result.Outcome != outcome || result.Reason != reason {
		t.Fatalf("actual Main result mismatch: exit=%d result=%s", actual, stdout)
	}
}

func TestNativeDriverActualCancellationLostResponseRetainsWriter(t *testing.T) {
	f := setupActualDriver(t)
	initial, err := f.run(t, nil)
	if err != nil || initial.Outcome != "awaiting_external" {
		t.Fatal("dispatch before cancellation", initial, err)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || len(status.Actions) != 1 {
		t.Fatal(status, err)
	}
	dispatch := status.Actions[0].DispatchID
	before, err := f.seed.authority.LifecycleStatus(f.workerToken, f.worker.SessionID, dispatch)
	if err != nil || before.State != "running" {
		t.Fatal("cancellation fixture must start from independent running evidence", before, err)
	}
	f.host.loseCancelResponse, f.host.cancelLookupMiss = true, true
	result, err := f.runMode(t, nil, "cancel", dispatch)
	if err != nil || f.host.cancellations != 1 || result.Outcome != "uncertain" {
		t.Fatalf("lost cancel outcome must remain uncertain: %+v %v cancellations=%d", result, err, f.host.cancellations)
	}
	f.host.cancelLookupMiss = false
	result, err = f.runMode(t, nil, "cancel", dispatch)
	if err != nil || result.Outcome != "cancelled" || f.host.cancellations != 1 || f.host.dispatches != 1 {
		t.Fatalf("reopen repeated cancel/dispatch or lost original acknowledgement: %+v %v cancels=%d dispatches=%d", result, err, f.host.cancellations, f.host.dispatches)
	}
	after, err := f.seed.authority.LifecycleStatus(f.workerToken, f.worker.SessionID, dispatch)
	beforeObservations, beforeErr := json.Marshal(before.Observations)
	afterObservations, afterErr := json.Marshal(after.Observations)
	if err != nil || beforeErr != nil || afterErr != nil || after.Binding != before.Binding || after.State != before.State || !bytes.Equal(beforeObservations, afterObservations) || after.Cancellation == nil || after.Cancellation.State != "acknowledged" {
		t.Fatal("cancellation acknowledgement fabricated terminal observation or changed execution identity", before, after, err)
	}
	for _, observation := range after.Observations {
		if observation.HostSlotTerminal != nil || terminalHostStateForFixture(observation.State) {
			t.Fatal("cancellation acknowledgement acquired terminal assurance", observation)
		}
	}
	storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || len(storage.Holds) == 0 {
		t.Fatal("cancellation acknowledgement released writer protection", err)
	}
	for _, original := range result.OriginalOperations {
		if original.Kind == "dispatch" && original.State != "completed" {
			t.Fatal("original dispatch correlation was lost", original)
		}
	}
}

func terminalHostStateForFixture(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func TestNativeDriverActualProfileAndWorkerDriftDuringHostCallbacks(t *testing.T) {
	for _, stage := range []string{"register", "verify_binding"} {
		for _, mutation := range []string{"profile", "worker_revocation"} {
			t.Run(stage+"/"+mutation, func(t *testing.T) {
				f := setupActualDriver(t)
				changed := false
				f.host.hook = func(ctx context.Context, observed string) error {
					if observed != stage || changed {
						return nil
					}
					changed = true
					if mutation == "worker_revocation" {
						return f.seed.authority.Revoke(f.seed.owner, f.worker.SessionID)
					}
					raw, err := os.ReadFile(f.profile)
					if err != nil {
						return err
					}
					return os.WriteFile(f.profile, append(raw, '\n'), 0600)
				}
				result, err := f.run(t, nil)
				if err != nil || !changed || f.host.dispatches != 0 || result.Outcome != "refused" {
					t.Fatalf("%s during %s permitted dispatch: %+v %v changed=%v dispatches=%d", mutation, stage, result, err, changed, f.host.dispatches)
				}
				storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
				if err != nil || len(storage.Holds) == 0 {
					t.Fatal("drift released uncertain writer", err)
				}
			})
		}
	}
}

func TestNativeDriverActualWaitingDoesNotExhaustController(t *testing.T) {
	f := setupActualDriver(t)
	first, err := f.run(t, nil)
	if err != nil || first.Outcome != "awaiting_external" {
		t.Fatal(first, err)
	}
	before, err := f.controller.Status("p", f.initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		result, err := f.run(t, nil)
		if err != nil || result.Outcome != "awaiting_external" || f.host.dispatches != 1 {
			t.Fatal("reopen churned a waiting controller", result, err)
		}
	}
	after, err := f.controller.Status("p", f.initial.ID)
	if err != nil || after.Terminal || before.Sequence != after.Sequence || before.NoProgress != after.NoProgress {
		t.Fatal("waiting advanced bookkeeping instead of external producer truth", before, after, err)
	}
}

func TestNativeDriverActualIndependentRejectionRequestsCorrection(t *testing.T) {
	f := setupActualDriver(t)
	result, err := f.run(t, nil)
	if err != nil || result.Outcome != "awaiting_external" {
		t.Fatal(result, err)
	}
	f.publishTask(t, f.tasks[0])
	f.host.state = "completed"
	decisions := &actualDriverDecisions{outcome: "request-changes"}
	result, err = f.run(t, decisions)
	if err != nil || result.Outcome != "awaiting_external" || decisions.calls != 1 || f.host.dispatches != 1 {
		t.Fatalf("independent rejection did not reach the original worker's correction gate: %+v %v decisions=%d", result, err, decisions.calls)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || status.Phase != "correction" || status.Corrections != 1 || status.Terminal || status.Actions[0].ActorID != f.worker.SessionID {
		t.Fatal("review rejection was approved, lost or assigned to a replacement worker", status, err)
	}
	current, err := store.FindTask(f.seed.w, f.seed.task.ID)
	if err != nil || current.Status == "done" {
		t.Fatal("independent rejection accepted the task", err)
	}
}

func TestNativeDriverActualTerminalObservationSurvivesConcurrentSTOP(t *testing.T) {
	f := setupActualDriver(t)
	stopped := false
	f.host.hook = func(ctx context.Context, stage string) error {
		if stage != "observe" || stopped {
			return nil
		}
		status, err := f.controller.Status("p", f.initial.ID)
		if err != nil {
			return err
		}
		var connection native.Connection
		if err = privateJSON(filepath.Join(f.private, "controller.json"), &connection); err != nil {
			return err
		}
		token, err := native.ReadCredential(connection.CredentialFile)
		if err != nil {
			return err
		}
		_, err = f.seed.authority.StopController(token, connection.SessionID, sessions.ControllerStepRequest{OperationKey: "stop-during-terminal-observation", ProjectID: "p", InvocationID: f.initial.ID, ExpectedSequence: status.Sequence})
		if err == nil {
			stopped = true
			f.host.state = "completed"
		}
		return err
	}
	result, err := f.run(t, nil)
	if err != nil || !stopped || result.Outcome != "stopped" || f.host.dispatches != 1 || f.host.observations != 1 {
		t.Fatalf("authenticated terminal observation did not survive concurrent STOP: %+v %v stopped=%v observations=%d", result, err, stopped, f.host.observations)
	}
	for _, hostResult := range f.host.results {
		lifecycle, err := f.seed.authority.LifecycleStatus(f.workerToken, f.worker.SessionID, hostResult.DispatchID)
		if err != nil || lifecycle.State != "completed" || len(lifecycle.Observations) != 1 || lifecycle.Observations[0].State != "completed" {
			t.Fatal("terminal result was polled but not ingested by the authenticated bridge", lifecycle, err)
		}
	}
	storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || len(storage.Holds) == 0 {
		t.Fatal("terminal observation implicitly released protection", err)
	}
}

func TestNativeDriverActualRetainedIntentWithoutHostBindingNeverLaunches(t *testing.T) {
	f := setupActualDriver(t)
	status := f.initial
	for _, key := range []string{"manual-reservation", "manual-intent"} {
		var err error
		status, err = f.controller.Step(sessions.ControllerStepRequest{OperationKey: key, ProjectID: "p", InvocationID: f.initial.ID, ExpectedSequence: status.Sequence})
		if err != nil {
			t.Fatal("manual authority transition", key, err)
		}
	}
	if len(status.Actions) != 1 || status.Actions[0].Kind != "host_dispatch" {
		t.Fatal("fixture must retain an actual committed intent without an SDK permit", status)
	}
	original := status.Actions[0].OperationKey
	for i := 0; i < 2; i++ {
		result, err := f.run(t, nil)
		// This host cannot verify an execution binding before registration.
		// Recovery must stop uncertain at that proof rather than register or
		// dispatch merely to make its historical lookup available.
		if err != nil || result.Outcome != "uncertain" || f.host.registers != 0 || f.host.dispatches != 0 {
			t.Fatalf("reopen manufactured authority for a retained intent: %+v %v registers=%d dispatches=%d lookups=%d", result, err, f.host.registers, f.host.dispatches, f.host.lookups)
		}
		found := false
		for _, operation := range result.OriginalOperations {
			if operation.Kind == "dispatch" && operation.OperationKey == original {
				found = true
			}
		}
		if !found {
			t.Fatal("reopen substituted the original dispatch key", result)
		}
	}
}

func TestNativeDriverActualPersistedReviewReceiptCanBeLostAndReopened(t *testing.T) {
	f := setupActualDriver(t)
	result, err := f.run(t, nil)
	if err != nil || result.Outcome != "awaiting_external" {
		t.Fatal(result, err)
	}
	revision := f.publishTask(t, f.tasks[0])
	f.host.state = "completed"
	result, err = f.run(t, nil)
	if err != nil || result.Outcome != "awaiting_external" || result.Reason != "independent_review_decision_required" {
		t.Fatal("missing independent decision must be an explicit wait", result, err)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || len(status.Actions) != 1 || status.Actions[0].Kind != "independent_review" {
		t.Fatal(status, err)
	}
	action := status.Actions[0]
	profile, err := application.OpenNativeReviewProfile(filepath.Join(f.private, "reviewer0.json"), filepath.Join(f.private, "review-config0.json"))
	if err != nil {
		t.Fatal(err)
	}
	access, err := profile.Authenticate()
	if err != nil {
		t.Fatal(err)
	}
	// Perform the real separately authenticated producer, then discard the
	// handoff to the driver. This models receipt loss after durable persistence;
	// it does not claim an OS or provider restart.
	review, err := access.Propose(application.NativeReviewRequest{OperationKey: action.OperationKey, AttemptID: action.AttemptID, LeaseID: action.LeaseID, RevisionID: action.RevisionID, ExpectedFence: action.Fence, Outcome: "approve", Rationale: "Actual independent fixture review before receipt loss", EvidenceIDs: action.EvidenceIDs})
	if err != nil || review.IssuerSession == f.worker.SessionID || review.Request.RevisionID != revision.ID || review.Request.ExpectedFence != revision.Fence || review.Request.OperationKey != action.OperationKey {
		t.Fatal("persisted independent receipt lost its exact source", review, err)
	}
	decisions := &actualDriverDecisions{outcome: "approve"}
	result, err = f.run(t, decisions)
	if err != nil || result.Outcome != "owner_required" || decisions.calls != 0 || f.host.dispatches != 1 {
		t.Fatal("reopen repeated the independent producer", result, err, decisions.calls)
	}
	status, err = f.controller.Status("p", f.initial.ID)
	if err != nil || status.Actions[0].ReviewID != review.ID || status.Actions[0].RevisionID != revision.ID || status.Terminal {
		t.Fatal("reopen substituted the persisted review or accepted owner work", status, err)
	}
	count, err := os.ReadFile(f.tasks[0].marker)
	if err != nil || string(count) != "ran" {
		t.Fatal("reopen reran the actual configured verification", string(count), err)
	}
}

func (f actualDriverFixture) verificationAction(t *testing.T) sessions.ControllerAction {
	t.Helper()
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || len(status.Actions) != 1 {
		t.Fatal(status, err)
	}
	bridge, err := application.OpenNativeLifecycle(filepath.Join(f.private, "bridge0.json"))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := bridge.Status(status.Actions[0].DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	previous := lifecycle.Observations[len(lifecycle.Observations)-1]
	hostSequence := previous.HostSequence + 1
	key, err := driver.OperationKey("p", f.initial.ID, f.initial.CheckpointID, hostSequence, "observe", status.Actions[0].OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Observe(sessions.ObserveLifecycleRequest{OperationKey: key, Binding: lifecycle.Binding, ExpectedSequence: previous.Sequence, HostSequence: hostSequence, State: "completed", Source: "owner-manual-native-host", SourceTime: time.Now(), EvidenceDigest: sessions.OutputDigest([]byte("independent fake-host terminal report"))}); err != nil {
		t.Fatal(err)
	}
	f.host.state = "completed"
	for i := 0; i < 4; i++ {
		if len(status.Actions) == 1 && status.Actions[0].Kind == "verification" {
			return status.Actions[0]
		}
		status, err = f.controller.Step(sessions.ControllerStepRequest{OperationKey: fmt.Sprintf("manual-verification-stage-%d", status.Sequence), ProjectID: "p", InvocationID: f.initial.ID, ExpectedSequence: status.Sequence})
		if err != nil || status.Terminal {
			t.Fatal("bounded manual progression to existing verification producer", status, err)
		}
	}
	t.Fatal("manual authority did not emit the expected verification action", status)
	return sessions.ControllerAction{}
}

func TestNativeDriverActualVerificationProducerRecoveryStates(t *testing.T) {
	for _, state := range []string{"completed", "pending", "interrupted", "pending_cancel"} {
		t.Run(state, func(t *testing.T) {
			f := setupActualDriver(t)
			result, err := f.run(t, nil)
			if err != nil || result.Outcome != "awaiting_external" {
				t.Fatal(result, err)
			}
			revision := f.publishTask(t, f.tasks[0])
			action := f.verificationAction(t)
			request := action.Verification
			if len(action.Verifications) == 1 {
				request = action.Verifications[0]
			}
			var runner application.NativeRunnerProfile
			runnerFile := filepath.Join(f.private, "runner0.json")
			if err = privateJSON(runnerFile, &runner); err != nil {
				t.Fatal(err)
			}
			selected, err := application.OpenNativeVerificationProfile(f.workerFile, runnerFile)
			if err != nil {
				t.Fatal(err)
			}
			access, err := selected.Authenticate()
			if err != nil {
				t.Fatal(err)
			}
			nativeRequest := application.NativeVerificationRequest{OperationKey: request.OperationKey, WorkerSession: request.WorkerSession, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence}
			if state == "completed" {
				evidence, err := access.Run(context.Background(), nativeRequest)
				if err != nil || evidence.IssuerSession != runner.RunnerSessionID || evidence.IssuerSession == f.worker.SessionID || evidence.Revision.ID != revision.ID || evidence.Revision.Fence != revision.Fence || evidence.GrantID != request.GrantID {
					t.Fatal("actual verification producer returned a different source", evidence, err)
				}
			} else {
				token, err := native.ReadCredential(runner.CredentialFile)
				if err != nil {
					t.Fatal(err)
				}
				checks, err := application.NativeProfileCheckDescriptors(runner, true)
				if err != nil || len(checks) != 1 {
					t.Fatal(checks, err)
				}
				digest := sessions.CheckDigest(checks[0].Descriptor)
				if _, prior, err := f.seed.authority.BeginVerification(f.workerToken, token, runner.RunnerSessionID, request, digest); err != nil || prior != nil {
					t.Fatal("reserve actual verification before simulated producer loss", err)
				}
				if state == "interrupted" {
					if _, err = f.seed.authority.InterruptVerification(f.workerToken, token, runner.RunnerSessionID, request, digest, "cancelled_before_start"); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Drop the producer handoff and reconstruct the application from disk.
			decisions := &actualDriverDecisions{outcome: "approve"}

			if state == "pending_cancel" {
				result, err = f.runMode(t, nil, "cancel", action.DispatchID)
				if err != nil || result.Outcome != "cancelled" || f.host.cancellations != 1 || decisions.calls != 0 {
					t.Fatal("pending verification could not withdraw its exact retained writer", result, err)
				}
			} else {
				result, err = f.run(t, decisions)
			}
			if err != nil {
				t.Fatal(result, err)
			}
			if state == "completed" {
				count, err := os.ReadFile(f.tasks[0].marker)
				if err != nil || string(count) != "ran" || result.Outcome != "owner_required" || decisions.calls != 1 {
					t.Fatal("completed producer was repeated or not reconciled", result, string(count), err)
				}
			} else {
				if _, err := os.Stat(f.tasks[0].marker); !os.IsNotExist(err) || decisions.calls != 0 || (result.Outcome != "uncertain" && result.Outcome != "awaiting_external" && !(state == "pending_cancel" && result.Outcome == "cancelled")) {
					t.Fatal("unknown/interrupted producer was rerun or approved", result, err)
				}
			}

			if state == "pending_cancel" {
				storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
				if err != nil || len(storage.Holds) == 0 {
					t.Fatal("withdrawal released pending verification protection", err)
				}
				var durable struct {
					Operations map[string]struct {
						Pending                bool
						Evidence               *sessions.Evidence
						CheckpointVerification *sessions.CheckpointVerificationBinding
					}
				}
				if err = json.Unmarshal(storage.PrivateBytes, &durable); err != nil {
					t.Fatal(err)
				}
				operation, ok := durable.Operations[request.OperationKey]
				if !ok || !operation.Pending || operation.Evidence != nil || operation.CheckpointVerification == nil || operation.CheckpointVerification.Request.OperationKey != request.OperationKey || operation.CheckpointVerification.Request.RevisionID != request.RevisionID || operation.CheckpointVerification.Request.ExpectedFence != request.ExpectedFence {
					t.Fatal("withdrawal fabricated completion or replaced pending verification", operation)
				}
			} else {
				lookup, err := access.Lookup(nativeRequest)
				if err != nil || string(lookup.State) != state {
					t.Fatal("reopen replaced the original verification key or state", lookup, err)
				}
			}

		})
	}
}

func TestNativeDriverActualContextCancellationDuringCallbacks(t *testing.T) {
	for _, stage := range []string{"register", "verify_binding"} {
		t.Run(stage, func(t *testing.T) {
			f := setupActualDriver(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			f.host.hook = func(_ context.Context, observed string) error {
				if observed == stage {
					called = true
					cancel()
				}
				return nil
			}
			selected, err := application.OpenNativeControllerDriver(f.profile, f.host, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := selected.Run(ctx, driver.Request{SchemaVersion: 1, ProjectID: "p", InvocationID: f.initial.ID, CheckpointID: f.initial.CheckpointID, ExpectedSequence: f.initial.Sequence, Mode: "drive"})
			if err != nil || !called || f.host.dispatches != 0 || (result.Outcome != "uncertain" && result.Outcome != "cancelled" && result.Outcome != "deadline") {
				t.Fatalf("context ended inside %s but work continued: %+v %v called=%v dispatches=%d", stage, result, err, called, f.host.dispatches)
			}
			storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
			if err != nil || len(storage.Holds) == 0 {
				t.Fatal("cancelled callback released its uncertain writer", err)
			}
		})
	}
}

func TestNativeDriverActualWithdrawalBeforeHostCancelNeverRepeatsEffect(t *testing.T) {
	f := setupActualDriver(t)
	initial, err := f.run(t, nil)
	if err != nil || initial.Outcome != "awaiting_external" {
		t.Fatal(initial, err)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || len(status.Actions) != 1 {
		t.Fatal(status, err)
	}
	action := status.Actions[0]
	lifecycle, err := f.seed.authority.LifecycleStatus(f.workerToken, f.worker.SessionID, action.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := driver.OperationKey("p", f.initial.ID, f.initial.CheckpointID, 0, "cancel", action.OperationKey)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := application.OpenNativeLifecycle(filepath.Join(f.private, "controller.json"))
	if err != nil {
		t.Fatal(err)
	}
	withdrawal, err := controller.Cancel(sessions.CancelLifecycleRequest{OperationKey: key, Binding: lifecycle.Binding})
	if err != nil || !withdrawal.HostActionAuthorized || withdrawal.Cancellation.State != "requested" {
		t.Fatal("actual durable withdrawal before simulated caller loss", withdrawal, err)
	}
	// Lose the first-use host permission before calling CancelContext. A lookup
	// miss on reopen must retain uncertainty instead of manufacturing that call.
	result, err := f.runMode(t, nil, "cancel", action.DispatchID)
	if err != nil || result.Outcome != "uncertain" || f.host.cancellations != 0 || f.host.dispatches != 1 {
		t.Fatal("reopen repeated cancellation without first-use authority", result, err)
	}
	storage, err := f.seed.authority.ObserveStorage(f.seed.owner)
	if err != nil || len(storage.Holds) == 0 {
		t.Fatal("lost first-use cancellation released its writer", err)
	}
}

func TestNativeDriverActualFailedConfiguredCheckRequestsCorrection(t *testing.T) {
	f := setupActualDriverVersion(t, 1, 16, true)
	result, err := f.run(t, nil)
	if err != nil || result.Outcome != "awaiting_external" {
		t.Fatal(result, err)
	}
	f.publishTask(t, f.tasks[0])
	f.host.state = "completed"
	decisions := &actualDriverDecisions{outcome: "approve"}
	result, err = f.run(t, decisions)
	if err != nil || result.Outcome != "awaiting_external" || decisions.calls != 0 || f.host.dispatches != 1 {
		t.Fatal("failed command was approved, relaunched or could not request correction", result, err)
	}
	status, err := f.controller.Status("p", f.initial.ID)
	if err != nil || status.Terminal || status.Phase != "correction" || status.Corrections != 1 || status.Actions[0].ActorID != f.worker.SessionID {
		t.Fatal("actual failure did not reach the original worker", status, err)
	}
	request := status.Actions[0].Verification
	if len(status.Actions[0].Verifications) == 1 {
		request = status.Actions[0].Verifications[0]
	}
	profile, err := application.OpenNativeVerificationProfile(f.workerFile, filepath.Join(f.private, "runner0.json"))
	if err != nil {
		t.Fatal(err)
	}
	access, err := profile.Authenticate()
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := access.Lookup(application.NativeVerificationRequest{OperationKey: request.OperationKey, AttemptID: request.AttemptID, LeaseID: request.LeaseID, RevisionID: request.RevisionID, CheckID: request.CheckID, ExpectedFence: request.ExpectedFence})
	if err != nil || lookup.Evidence == nil || lookup.Evidence.ExitCode != 7 || lookup.Evidence.Revision.ID != request.RevisionID || lookup.Evidence.Revision.Fence != request.ExpectedFence {
		t.Fatal("fixture did not exercise the actual failing command/evidence", lookup, err)
	}
}

// Command qualification594 creates a private, disposable owner fixture for the
// core/594 real-host qualification. It never starts host children.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

type ownerConfig struct {
	SchemaVersion       int    `json:"schema_version"`
	Workspace           string `json:"workspace"`
	Authority           string `json:"authority"`
	InstallationID      string `json:"installation_id"`
	OwnerCredentialFile string `json:"owner_credential_file"`
}
type manifest struct {
	Schema           int               `json:"schema"`
	Workspace        string            `json:"workspace"`
	Authority        string            `json:"authority"`
	OwnerConfig      string            `json:"owner_config"`
	Capability       string            `json:"capability_file"`
	Project          string            `json:"project"`
	Invocation       string            `json:"invocation"`
	Tasks            map[string]string `json:"tasks"`
	Checkouts        map[string]string `json:"checkouts"`
	Connections      map[string]string `json:"connections"`
	Profiles         map[string]string `json:"profiles"`
	Policies         map[string]string `json:"policies"`
	Sessions         map[string]string `json:"sessions"`
	Operations       map[string]string `json:"operations"`
	CreatedAt        time.Time         `json:"created_at"`
	ProbeSHA256      string            `json:"probe_sha256"`
	CapabilitySHA256 string            `json:"capability_sha256"`
}

func main() {
	if len(os.Args) < 2 {
		die("usage: qualification594 setup [--root DIR --probe FILE]")
	}
	switch os.Args[1] {
	case "setup":
		fs := flag.NewFlagSet("setup", flag.ExitOnError)
		root := fs.String("root", "/private/tmp/dacli-critical-path-20261022/core594-live/fixture", "private fixture root")
		probe := fs.String("probe", "/private/tmp/dacli-critical-path-20261022/core594-live/capability-probe.json", "current owner-observed host probe")
		_ = fs.Parse(os.Args[2:])
		if err := setup(*root, *probe); err != nil {
			die("setup: %v", err)
		}
	case "prepare":
		if len(os.Args) != 4 {
			die("usage: qualification594 prepare MANIFEST TASK")
		}
		if err := prepareIntegration(os.Args[2], os.Args[3]); err != nil {
			die("prepare: %v", err)
		}
	case "release":
		if len(os.Args) != 5 {
			die("usage: qualification594 release MANIFEST TASK OBSERVATION")
		}
		if err := releaseIntegrated(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			die("release: %v", err)
		}
	case "finish":
		if len(os.Args) != 6 {
			die("usage: qualification594 finish MANIFEST TASK CANDIDATE CONTROLLER_STATUS")
		}
		if err := finishIntegration(os.Args[2], os.Args[3], os.Args[4], os.Args[5]); err != nil {
			die("finish: %v", err)
		}
	case "budget":
		if len(os.Args) != 3 {
			die("usage: qualification594 budget MANIFEST")
		}
		if err := configureBudget(os.Args[2]); err != nil {
			die("budget: %v", err)
		}
	case "controller-start":
		if len(os.Args) != 3 {
			die("usage: qualification594 controller-start MANIFEST")
		}
		if err := controllerStart(os.Args[2]); err != nil {
			die("controller-start: %v", err)
		}
	case "controller-status":
		if len(os.Args) != 3 {
			die("usage: qualification594 controller-status MANIFEST")
		}
		if err := controllerStatus(os.Args[2]); err != nil {
			die("controller-status: %v", err)
		}
	default:
		die("unknown operation %q", os.Args[1])
	}
}
func setup(root, probePath string) error {
	root, _ = filepath.Abs(root)
	if _, e := os.Stat(filepath.Join(root, "manifest.json")); e == nil {
		return fmt.Errorf("manifest exists; refusing to replace authority")
	}
	probe, e := native.ReadPrivate(probePath, 65536)
	if e != nil {
		return fmt.Errorf("read current private host probe: %w", e)
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return e
	}
	if e = os.Chmod(root, 0700); e != nil {
		return e
	}
	wr := filepath.Join(root, "workspace")
	w, e := workspace.Init(wr, "DACLI core594 native Luna qualification")
	if e != nil {
		return e
	}
	project := "luna594"
	if _, e = store.CreateProject(w, "a-root", "Native Luna qualification", project, "Qualify independent native workers, trusted checks, review and owner integration", "implementation", model.LandingPolicy{Mode: model.LandingLocal, Base: "main"}); e != nil {
		return e
	}
	tasks := map[string]*store.Task{}
	for _, x := range []struct {
		k, title, claim string
		deps            []string
	}{{"a", "Implement alpha label normalization", "alpha", nil}, {"b", "Implement beta status counts", "beta", nil}} {
		t, err := store.CreateTask(w, "a-root", project, x.title, store.TaskOpts{Accept: []string{"the scoped function has the required behavior", "owner trusted check passes and independent review approves"}, Estimate: "2,3,5", Claims: []string{x.claim}, DependsOn: x.deps})
		if err != nil {
			return err
		}
		tasks[x.k] = t
	}
	c, e := store.CreateTask(w, "a-root", project, "Build combined qualification report", store.TaskOpts{Accept: []string{"report combines normalized label and all status counts", "owner trusted check passes and independent review approves"}, Estimate: "3,5,8", Claims: []string{"combined"}, DependsOn: []string{tasks["a"].ID, tasks["b"].ID}})
	if e != nil {
		return e
	}
	tasks["c"] = c
	roles := []team.Role{{Name: "implementation", Kind: "implementer", Runtime: "codex", Model: "gpt-6-luna", Grant: "rw", Scope: []string{"alpha/**", "beta/**", "combined/**"}, WIP: 3, MaxPoints: 13, Prompt: "Implement only the assigned claim. Commit the change and report its revision."}, {Name: "review", Kind: "reviewer", Runtime: "codex", Model: "gpt-6-luna", Grant: "ro", Scope: []string{"alpha/**", "beta/**", "combined/**"}, WIP: 3, MaxPoints: 13, Prompt: "Review the exact revision against its criteria independently."}}
	for _, r := range roles {
		if e = store.CreateRole(w, "a-root", r); e != nil {
			return e
		}
	}
	if e = os.WriteFile(filepath.Join(wr, ".gitignore"), []byte(".dacli\n__pycache__/\n"), 0600); e != nil {
		return e
	}
	for _, d := range []string{"alpha", "beta", "combined", "checks"} {
		if e = os.MkdirAll(filepath.Join(wr, d), 0700); e != nil {
			return e
		}
	}
	seed := map[string]string{"alpha/README.md": "# Alpha\n", "beta/README.md": "# Beta\n", "combined/README.md": "# Combined\n", "checks/check_a.py": "from alpha.labels import normalize_label\nassert normalize_label(' Hello  World ') == 'hello-world'\nassert normalize_label('  \\t ') == ''\n", "checks/check_b.py": "from beta.statuses import count_statuses\nassert count_statuses(['pending','done','pending']) == {'pending':2,'done':1,'unknown':0}\nassert count_statuses([]) == {'pending':0,'done':0,'unknown':0}\ntry: count_statuses(['bad'])\nexcept ValueError: pass\nelse: raise AssertionError('unexpected status accepted')\n", "checks/check_c.py": "from combined.report import summarize\nassert summarize(' Hello  World ', ['pending','done']) == {'label':'hello-world','counts':{'pending':1,'done':1,'unknown':0}}\n"}
	for p, s := range seed {
		if e = os.WriteFile(filepath.Join(wr, p), []byte(s), 0600); e != nil {
			return e
		}
	}
	if e = git(wr, "init", "-b", "main"); e != nil {
		return e
	}
	if e = git(wr, "add", "-A"); e != nil {
		return e
	}
	if e = git(wr, "-c", "user.name=Qualification owner", "-c", "user.email=owner@example.invalid", "commit", "-m", "qualification base"); e != nil {
		return e
	}
	private := filepath.Join(root, "private")
	if e = os.MkdirAll(private, 0700); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(root, "markers"), 0700); e != nil {
		return e
	}
	authority := filepath.Join(private, "authority.json")
	ownerCred := filepath.Join(private, "owner.token")
	ownerFile := filepath.Join(private, "owner.json")
	installation := "core594-live"
	admission, e := application.NewLeaseAdmission(w)
	if e != nil {
		return e
	}
	a, e := sessions.Open(authority, sessions.Binding{Workspace: w.ID, Installation: installation}, admission)
	if e != nil {
		return e
	}
	owner, e := a.Bootstrap()
	if e != nil {
		return e
	}
	if e = write(ownerCred, []byte(owner+"\n")); e != nil {
		return e
	}
	if e = save(ownerFile, ownerConfig{1, wr, authority, installation, ownerCred}); e != nil {
		return e
	}
	for _, upgrade := range []func(string) error{a.UpgradeSwarms, a.UpgradeDispatchIntents, a.UpgradeLifecycle, a.UpgradeRecovery, a.UpgradePreflight, a.UpgradeCheckpoints, a.UpgradeBudgets, a.UpgradeNativeAcceptance, a.UpgradeNativePolicy, a.UpgradeController, a.UpgradeNativeModelRouting, a.UpgradeNativeIntegration} {
		if e = upgrade(owner); e != nil {
			return fmt.Errorf("upgrade owner authority: %w", e)
		}
	}
	now := time.Now().UTC()
	probeHash := sum(probe)
	capabilityFile := filepath.Join(private, "capability.json")
	capability := application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: w.ID, ProjectID: project, Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: now, ExpiresAt: now.Add(45 * time.Minute), Codex: &application.CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probePath, ProbeSHA256: probeHash, NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}}
	if e = save(capabilityFile, capability); e != nil {
		return e
	}
	capHash := sum(read(capabilityFile))
	checks := map[string]string{"a": "PYTHONPATH=. python3 checks/check_a.py", "b": "PYTHONPATH=. python3 checks/check_b.py", "c": "PYTHONPATH=. python3 checks/check_c.py"}
	checkouts := map[string]string{}
	for _, k := range []string{"a", "b", "c"} {
		p := filepath.Join(root, "checkouts", k)
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if e = git(wr, "worktree", "add", "-b", "qualification-"+k, p, "main"); e != nil {
			return e
		}
		if e = os.Symlink(filepath.Join(wr, workspace.Dir), filepath.Join(p, workspace.Dir)); e != nil {
			return e
		}
		checkouts[k] = p
	}
	obs, e := checkout.Observe(wr)
	if e != nil {
		return e
	}
	for _, t := range tasks {
		if e = a.BindTask(owner, sessions.TaskBinding{TaskID: t.ID, ProjectID: project, Repository: obs.Repository, AdmissionEnabled: true}); e != nil {
			return e
		}
	}
	m := manifest{Schema: 1, Workspace: wr, Authority: authority, OwnerConfig: ownerFile, Capability: capabilityFile, Project: project, Invocation: "luna594-invocation", Tasks: map[string]string{}, Checkouts: checkouts, Connections: map[string]string{}, Profiles: map[string]string{}, Policies: map[string]string{}, Sessions: map[string]string{}, Operations: map[string]string{}, CreatedAt: now, ProbeSHA256: probeHash, CapabilitySHA256: capHash}
	for k, t := range tasks {
		m.Tasks[k] = t.ID
	}
	routes := map[string]sessions.ControllerTaskRoute{}
	preTasks := map[string]sessions.PreflightTask{}
	for _, k := range []string{"a", "b", "c"} {
		t := tasks[k]
		path := map[string]string{"a": "alpha", "b": "beta", "c": "combined"}[k]
		workerConn, workerTok, worker, e := enroll(a, owner, w, authority, installation, private, "worker-"+k, sessions.Scope{Actions: []string{"read", "attempt.register", "lease.acquire", "lease.renew", "lease.release", "attempt.publish", "evidence.submit", "verification.request"}, Resources: []string{"workspace:" + w.ID, "task:" + t.ID, "checkout:" + checkouts[k], "path:" + path}})
		if e != nil {
			return e
		}
		_ = workerTok
		m.Connections[k] = workerConn
		m.Connections["worker_"+k] = workerConn
		m.Sessions["worker_"+k] = worker.ID
		cmd := checks[k] + " && printf x >> " + quote(filepath.Join(root, "markers", k+".count"))
		checkID := "trusted-" + k
		desc := sessions.CheckDescriptor{ID: checkID, Command: cmd, Deadline: 30 * time.Second}
		runner, runnerTok, runnerSession, grant, e := runnerProfile(a, owner, w, authority, installation, private, k, t, obs.Repository, desc)
		if e != nil {
			return e
		}
		_ = runnerTok
		m.Profiles["runner-"+k] = runner
		m.Profiles["runner_"+k] = runner
		m.Sessions["runner_"+k] = runnerSession.ID
		m.Sessions["verification_grant_"+k] = grant
		reviewerConn, reviewerTok, reviewer, reviewGrant, reviewPath, e := reviewerProfile(a, owner, w, authority, installation, private, k, t, obs.Repository, worker.ID, desc)
		if e != nil {
			return e
		}
		_ = reviewerTok
		m.Connections["review-"+k] = reviewerConn
		m.Connections["reviewer_"+k] = reviewerConn
		m.Sessions["reviewer_"+k] = reviewer.ID
		m.Sessions["review_grant_"+k] = reviewGrant
		m.Policies["review-"+k] = reviewPath
		m.Policies["review_"+k] = reviewPath
		contractPath := filepath.Join(private, "contract-"+k+".json")
		contract := application.NativePhaseContract{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: project, TaskID: t.ID, Harness: "codex", ObservedAt: now, ExpiresAt: now.Add(45 * time.Minute), Models: []string{"gpt-6-luna"}, ReviewOutputContract: "native-review-v1", IntegrationRoute: "owner-manual-local", IntegrationCheckout: wr, IntegrationBase: obs.Commit, IntegrationPolicyDigest: sum([]byte("native-integration-policy")), ExternalStatus: "ready", TokenControl: "unavailable", BudgetControl: "unavailable"}
		if e = save(contractPath, contract); e != nil {
			return e
		}
		m.Profiles["contract-"+k] = contractPath
		m.Profiles["contract_"+k] = contractPath
		intPath := filepath.Join(private, "integration-"+k+".json")
		ip := sessions.NativeIntegrationPolicy{SchemaVersion: 1, ProjectID: project, TaskID: t.ID, Revision: "integration-" + k, Repository: obs.Repository, TargetCheckout: wr, TargetRef: "refs/heads/main", ExpectedCommit: obs.Commit, ExpectedTree: obs.Tree, NativePolicyRevision: "owner-policy", Allow: true, ExpiresAt: now.Add(45 * time.Minute)}
		if e = save(intPath, ip); e != nil {
			return e
		}
		m.Policies["integration-"+k] = intPath
		m.Policies["integration_"+k] = intPath
		preTasks[t.ID] = sessions.PreflightTask{ImplementationRole: "implementation", ReviewRole: "review", RecoveryRole: "implementation", WorkerConnection: workerConn, RunnerProfile: runner, ContractProfile: contractPath, ReviewerProfile: reviewPath, RequireReview: true, RequireVerification: true, RequireIntegration: true, OwnerAuthorizedIntegration: true}
		routes[t.ID] = sessions.ControllerTaskRoute{WorkerSession: worker.ID, RunnerSession: runnerSession.ID, ReviewerSession: reviewer.ID, VerificationGrantID: grant, ReviewGrantID: reviewGrant, CheckID: checkID, PromptSpecDigest: sessions.OutputDigest([]byte("594:" + k + ":gpt-6-luna"))}
	}

	swarm := sessions.SwarmPolicy{ProjectID: project, Revision: "swarm-594", Harness: "codex", CapabilityDigest: "sha256:" + capHash, Width: 2, WIP: 3, TTL: 45 * time.Minute, Roles: []sessions.SwarmRole{{ID: "implementation", Kind: "implementation", Harness: "codex", Capacity: 3}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 3}}, Placements: map[string]sessions.SwarmPlacement{}}
	for _, k := range []string{"a", "b", "c"} {
		swarm.Placements[tasks[k].ID] = sessions.SwarmPlacement{Checkout: checkouts[k], ImplementKind: "implementation", ReviewKind: "review"}
	}
	if e = a.ConfigureSwarmWithBindings(owner, swarm, nil); e != nil {
		return fmt.Errorf("configure swarm: %w", e)
	}

	pre := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: project, Revision: "preflight-594", Harness: "codex", Rationale: "owner-observed Codex host; gpt-6-luna; manually delivered private child profiles", AcceptManualAssurance: true, ExpiresAt: now.Add(45 * time.Minute), Tasks: preTasks}
	if e = a.ConfigurePreflight(owner, pre); e != nil {
		return fmt.Errorf("configure preflight: %w", e)
	}

	// These are bounded owner estimates for admission only. Native provider usage
	// is not observable through the host path and is never represented as actual.
	budget := sessions.BudgetPolicy{SchemaVersion: 1, ProjectID: project, Revision: "budget-594", CycleID: "cycle-594", Harness: "codex", Rationale: "owner-estimated qualification bounds; actual provider usage is unavailable", CycleLimit: 1200, WindowLimit: 1200, RecoveryFloor: 60, Window: time.Hour, WindowOrigin: now.Add(-time.Minute), ExpiresAt: now.Add(45 * time.Minute), Tasks: map[string]sessions.BudgetTask{}}
	for _, k := range []string{"a", "b", "c"} {
		phases := map[string]sessions.BudgetEstimate{}
		for phase, tokens := range map[string]int64{"implementation": 120, "review": 80, "re-review": 40, "correction": 60, "verification": 0, "integration": 40, "recovery": 40} {
			role := "implementation"
			if phase == "review" || phase == "re-review" {
				role = "review"
			}
			phases[phase] = sessions.BudgetEstimate{RoleID: role, Model: "gpt-6-luna", Tokens: tokens, Provenance: "owner-estimate", ObservedAt: now}
		}
		budget.Tasks[tasks[k].ID] = sessions.BudgetTask{Phases: phases, IntegrationOperationID: "integration-" + k, AcceptanceOperationID: "accept-" + k}
	}
	if e = a.ConfigureBudget(owner, budget); e != nil {
		return fmt.Errorf("configure owner-estimated budget: %w", e)
	}
	if e = a.ConfigureLifecycle(owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: project, Revision: "lifecycle-594", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		return e
	}
	if e = a.ConfigureCheckpoints(owner, sessions.CheckpointPolicy{SchemaVersion: 1, ProjectID: project, Revision: "checkpoint-594", Harness: "codex", TaskIDs: []string{tasks["a"].ID, tasks["b"].ID, tasks["c"].ID}, MaxCycles: 12, MaxNoProgress: 3, ExpiresAt: now.Add(45 * time.Minute)}); e != nil {
		return e
	}
	cp := sessions.ControllerPolicy{SchemaVersion: 1, ProjectID: project, Revision: "controller-594", Harness: "codex", CapabilityDigest: "sha256:" + capHash, TaskIDs: []string{tasks["a"].ID, tasks["b"].ID, tasks["c"].ID}, MaxWaves: 4, MaxConcurrency: 2, MaxCorrectionRounds: 2, MaxNoProgress: 3, TransientRetries: 1, MaxElapsed: 40 * time.Minute, RetryBackoff: 3 * time.Second, ExpiresAt: now.Add(45 * time.Minute)}
	if e = a.ConfigureController(owner, cp); e != nil {
		return e
	}
	bridgeConn, _, bridge, e := enroll(a, owner, w, authority, installation, private, "bridge", sessions.Scope{Actions: []string{"read", "dispatch.correlate", "dispatch.observe"}, Resources: []string{"workspace:" + w.ID, "project:" + project, "task:" + tasks["a"].ID, "task:" + tasks["b"].ID, "task:" + tasks["c"].ID, "path:alpha", "path:beta", "path:combined"}})
	if e != nil {
		return e
	}
	m.Connections["bridge"] = bridgeConn
	m.Sessions["bridge"] = bridge.ID
	scope, e := a.ControllerEnrollmentScope(owner, project)
	if e != nil {
		return e
	}
	controllerConn, _, controller, e := enroll(a, owner, w, authority, installation, private, "controller", scope)
	if e != nil {
		return e
	}
	m.Connections["controller"] = controllerConn
	m.Sessions["controller"] = controller.ID
	for taskID, r := range routes {
		r.BridgeSession = bridge.ID
		routes[taskID] = r
	}
	ctrlProfile := application.NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: project, SessionID: controller.ID, Role: "controller", CapabilityFile: capabilityFile, CapabilitySHA256: capHash, PolicyRevision: cp.Revision, Routes: routes}
	p := filepath.Join(private, "orchestration-controller.json")
	if e = save(p, ctrlProfile); e != nil {
		return e
	}
	m.Profiles["controller"] = p
	bridgeProfile := application.NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: project, SessionID: bridge.ID, Role: "bridge", CapabilityFile: capabilityFile, CapabilitySHA256: capHash}
	p = filepath.Join(private, "orchestration-bridge.json")
	if e = save(p, bridgeProfile); e != nil {
		return e
	}
	m.Profiles["bridge"] = p
	for _, k := range []string{"a", "b", "c"} {
		t := tasks[k]
		var cn native.Connection
		if e = native.Decode(read(m.Connections["worker_"+k]), &cn); e != nil {
			return e
		}
		wp := application.NativeOrchestrationConfig{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: project, SessionID: cn.SessionID, Role: "worker", CapabilityFile: capabilityFile, CapabilitySHA256: capHash}
		p = filepath.Join(private, "orchestration-worker-"+k+".json")
		if e = save(p, wp); e != nil {
			return e
		}
		m.Profiles["orchestration_worker_"+k] = p
		_ = t
	}
	routing := sessions.NativeModelRoutingPolicy{SchemaVersion: 1, ProjectID: project, Revision: "routing-594", Harness: "codex", ExpiresAt: now.Add(45 * time.Minute), Tasks: map[string]sessions.NativeModelRoutingTask{}}
	for _, k := range []string{"a", "b", "c"} {
		t := tasks[k]
		routing.Tasks[t.ID] = sessions.NativeModelRoutingTask{MaxCorrectionRounds: 2, Candidates: []sessions.NativeModelRoutingCandidate{{DifficultyTier: 2, Route: routes[t.ID], RoleID: "implementation", Model: "gpt-6-luna", Preflight: preTasks[t.ID], ContextNeeded: 0, Available: true, ObservedAt: now, ExpiresAt: now.Add(40 * time.Minute)}}}
	}
	if e = a.ConfigureNativeModelRouting(owner, routing); e != nil {
		return fmt.Errorf("configure explicit Luna route: %w", e)
	}
	m.Policies["swarm"] = "swarm-594"
	m.Policies["preflight"] = "preflight-594"
	m.Policies["controller"] = "controller-594"
	m.Policies["model_routing"] = "routing-594"
	m.Policies["lifecycle"] = "lifecycle-594"
	m.Policies["checkpoint"] = "checkpoint-594"
	m.Policies["budget"] = "budget-594"
	manifestPath := filepath.Join(root, "manifest.json")
	if e = save(manifestPath, m); e != nil {
		return e
	}
	// Open the public profile and plan once to prove the generated independent
	// profiles and owner policies are usable before real children are dispatched.
	if _, e = application.OpenNativeOrchestrationProfile(controllerConn, m.Profiles["controller"]); e != nil {
		return fmt.Errorf("controller profile validation: %w", e)
	}
	fmt.Printf("setup complete: %s\n", manifestPath)
	return nil
}
func enroll(a *sessions.Authority, owner string, w *workspace.Workspace, authority, installation, private, name string, scope sessions.Scope) (string, string, sessions.Session, error) {
	s, t, e := a.Enroll(owner, scope, time.Now().UTC().Add(45*time.Minute))
	if e != nil {
		return "", "", sessions.Session{}, e
	}
	cred := filepath.Join(private, name+".token")
	conn := filepath.Join(private, name+".json")
	if e = write(cred, []byte(t+"\n")); e != nil {
		return "", "", sessions.Session{}, e
	}
	if e = save(conn, native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: authority, WorkspaceID: w.ID, InstallationID: installation, SessionID: s.ID, CredentialFile: cred}); e != nil {
		return "", "", sessions.Session{}, e
	}
	return conn, t, s, nil
}
func runnerProfile(a *sessions.Authority, owner string, w *workspace.Workspace, authority, installation, private, key string, t *store.Task, repo string, d sessions.CheckDescriptor) (string, string, sessions.Session, string, error) {
	s, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"verification.run"}, Resources: []string{"task:" + t.ID}}, time.Now().UTC().Add(45*time.Minute))
	if e != nil {
		return "", "", sessions.Session{}, "", e
	}
	g, e := a.GrantVerification(owner, s.ID, t.ID, repo, d, time.Now().UTC().Add(40*time.Minute))
	if e != nil {
		return "", "", sessions.Session{}, "", e
	}
	cred := filepath.Join(private, "runner-"+key+".token")
	if e = write(cred, []byte(token+"\n")); e != nil {
		return "", "", sessions.Session{}, "", e
	}
	p := application.NativeRunnerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authority, InstallationID: installation, TaskID: t.ID, Repository: repo, RunnerSessionID: s.ID, CredentialFile: cred, Checks: []application.ConfiguredCheck{{ID: d.ID, Command: d.Command, Deadline: d.Deadline.String(), GrantID: g.ID}}}
	path := filepath.Join(private, "runner-"+key+".json")
	if e = save(path, p); e != nil {
		return "", "", sessions.Session{}, "", e
	}
	return path, token, s, g.ID, nil
}
func reviewerProfile(a *sessions.Authority, owner string, w *workspace.Workspace, authority, installation, private, key string, t *store.Task, repo, worker string, d sessions.CheckDescriptor) (string, string, sessions.Session, string, string, error) {
	s, token, e := a.Enroll(owner, sessions.Scope{Actions: []string{"read", "review.propose", "review.evaluate"}, Resources: []string{"workspace:" + w.ID, "task:" + t.ID}}, time.Now().UTC().Add(45*time.Minute))
	if e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	policy := application.NativeReviewPolicy{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: "luna594", TaskID: t.ID, Revision: "review-" + key, Checks: []sessions.RequiredCheck{{ID: d.ID, CommandDigest: sessions.CheckDigest(d)}}}
	policyPath := filepath.Join(private, "review-policy-"+key+".json")
	if e = save(policyPath, policy); e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	reviewAuthority, _, e := application.OpenNativeReviewAuthority(w.Root, w.ID, authority, installation, policyPath)
	if e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	g, e := reviewAuthority.GrantReview(owner, sessions.ReviewGrantRequest{ReviewerSession: s.ID, WorkerSession: worker, TaskID: t.ID, Repository: repo, ExpiresAt: time.Now().UTC().Add(40 * time.Minute)})
	if e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	cred := filepath.Join(private, "reviewer-"+key+".token")
	if e = write(cred, []byte(token+"\n")); e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	conn := filepath.Join(private, "reviewer-"+key+".json")
	if e = save(conn, native.Connection{SchemaVersion: 1, Workspace: w.Root, Authority: authority, WorkspaceID: w.ID, InstallationID: installation, SessionID: s.ID, CredentialFile: cred}); e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	profile := application.NativeReviewerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authority, InstallationID: installation, ProjectID: "luna594", TaskID: t.ID, Repository: repo, ReviewerSessionID: s.ID, WorkerSessionID: worker, GrantID: g.ID, CredentialFile: cred, PolicyFile: policyPath}
	profilePath := filepath.Join(private, "reviewer-profile-"+key+".json")
	if e = save(profilePath, profile); e != nil {
		return "", "", sessions.Session{}, "", "", e
	}
	return conn, token, s, g.ID, profilePath, nil
}
func write(path string, b []byte) error {
	if e := os.WriteFile(path, b, 0600); e != nil {
		return e
	}
	return os.Chmod(path, 0600)
}
func save(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return write(path, b)
}
func read(p string) []byte { b, _ := os.ReadFile(p); return b }
func sum(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func git(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	b, e := c.CombinedOutput()
	if e != nil {
		return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), e, strings.TrimSpace(string(b)))
	}
	return nil
}
func quote(s string) string  { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }

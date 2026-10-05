package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Real CLI/private profiles and configured command; host correlation is a
// disposable manual declaration, not an actual coding model or host launch.
func TestNativeControllerCLIActualCommandIndependentReviewAndIntegrationCheckpoint(t *testing.T) {
	for _, correction := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "actual-failure-correction"}[correction], func(t *testing.T) { controllerCLIActual(t, correction) })
	}
}
func controllerCLIActual(t *testing.T, correction bool) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "command-count")
	command := ""
	if correction {
		command = "printf ran >> '" + marker + "'; test -f src/ready"
	}
	f := setupVerification(t, command, true)
	if correction {
		f.marker = marker
	}
	if _, e := f.authority.ReleaseLease(f.workerToken, f.worker.SessionID, sessions.ReleaseLeaseRequest{OperationKey: "release-initial", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence}); e != nil {
		t.Fatal(e)
	}
	if e := store.SetEstimate(f.w, f.task, "5,8,11"); e != nil {
		t.Fatal(e)
	}
	f.task.Doc.Front.SetList("claims", []string{"src"})
	f.task.Doc.SetSection("Acceptance", "- [ ] fixture command evidence")
	if e := store.SaveTask(f.task); e != nil {
		t.Fatal(e)
	}
	for _, role := range []team.Role{{Name: "implementation", Kind: "implementer", Runtime: "codex", Model: "model", Grant: "rw", MaxPoints: 13}, {Name: "review", Kind: "reviewer", Runtime: "codex", Model: "model", Grant: "ro", MaxPoints: 13}} {
		if e := store.CreateRole(f.w, "owner", role); e != nil {
			t.Fatal(e)
		}
	}
	verificationGit(t, f.w.Root, "add", ".")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "budget fixture roles and scope")
	private := t.TempDir()
	workerFile := filepath.Join(private, "worker.json")
	if out, e := verificationRun(t, f.w, enroll, "--config", f.config, "--task", f.task.ID, "--connection", workerFile, "--credential", filepath.Join(private, "worker.token"), "--path", "src", "--checkout", f.w.Root, "--verification"); e != nil {
		t.Fatal(out, e)
	}
	var worker native.Connection
	if e := privateJSON(workerFile, &worker); e != nil {
		t.Fatal(e)
	}
	wt, e := native.ReadCredential(worker.CredentialFile)
	if e != nil {
		t.Fatal(e)
	}
	probe := dispatchProbeFixture(t)
	probeFile := filepath.Join(private, "probe.json")
	if e = os.WriteFile(probeFile, probe, 0600); e != nil {
		t.Fatal(e)
	}
	probeSHA := sha256.Sum256(probe)
	capability := filepath.Join(private, "capability.json")
	verificationJSON(t, capability, application.ManualSwarmCapability{SchemaVersion: 2, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Codex: &application.CodexDispatchCapability{SchemaVersion: 1, ProbeFile: probeFile, ProbeSHA256: hex.EncodeToString(probeSHA[:]), NativeHostAttestation: "owner-observed-native-collaboration", ChildProfileChannel: "owner-manual-separate-process"}})
	raw, e := os.ReadFile(capability)
	if e != nil {
		t.Fatal(e)
	}
	capSHA := sha256.Sum256(raw)
	if e = f.authority.UpgradeSwarms(f.owner); e != nil {
		t.Fatal(e)
	}
	swarm := sessions.SwarmPolicy{ProjectID: "p", Revision: "swarm", Harness: "codex", CapabilityDigest: "sha256:" + hex.EncodeToString(capSHA[:]), Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implementation", Kind: "implementation", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{f.task.ID: {Checkout: f.w.Root, ImplementKind: "implementation", ReviewKind: "review"}}}
	if e = f.authority.ConfigureSwarm(f.owner, swarm); e != nil {
		t.Fatal(e)
	}
	for _, upgrade := range []func(string) error{f.authority.UpgradeDispatchIntents, f.authority.UpgradeLifecycle, f.authority.UpgradeRecovery, f.authority.UpgradePreflight, f.authority.UpgradeCheckpoints, f.authority.UpgradeBudgets} {
		if e = upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	observed, e := checkout.Observe(f.w.Root)
	if e != nil {
		t.Fatal(e)
	}
	contract := filepath.Join(private, "contract.json")
	verificationJSON(t, contract, application.NativePhaseContract{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", TaskID: f.task.ID, Harness: "codex", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Models: []string{"model"}, ReviewOutputContract: "native-review-v1", IntegrationRoute: "owner-manual-local", IntegrationCheckout: f.w.Root, IntegrationBase: observed.Commit, IntegrationPolicyDigest: strings.Repeat("a", 64), ExternalStatus: "ready"})
	reviewerFile := filepath.Join(private, "reviewer.json")
	reviewConfig := filepath.Join(private, "review-config.json")
	if out, e := verificationRun(t, f.w, reviewerAdd, "--config", f.config, "--task", f.task.ID, "--worker-session", worker.SessionID, "--runner-config", f.runnerProfile, "--policy-revision", "review-policy", "--policy-out", filepath.Join(private, "review-policy.json"), "--review-config", reviewConfig, "--connection", reviewerFile, "--credential", filepath.Join(private, "reviewer.token")); e != nil {
		t.Fatal(out, e)
	}
	var reviewer native.Connection
	var reviewProfile application.NativeReviewerProfile
	if e = privateJSON(reviewerFile, &reviewer); e != nil {
		t.Fatal(e)
	}
	if e = privateJSON(reviewConfig, &reviewProfile); e != nil {
		t.Fatal(e)
	}
	preflight := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "preflight", Harness: "codex", Rationale: "explicit fixture model and manual phase contracts", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(30 * time.Minute), Tasks: map[string]sessions.PreflightTask{f.task.ID: {ImplementationRole: "implementation", ReviewRole: "review", RecoveryRole: "implementation", WorkerConnection: workerFile, RunnerProfile: f.runnerProfile, ContractProfile: contract, ReviewerProfile: reviewConfig, RequireReview: true, RequireVerification: true, RequireIntegration: true, OwnerAuthorizedIntegration: true}}}
	if e = f.authority.ConfigurePreflight(f.owner, preflight); e != nil {
		t.Fatal(e)
	}
	policy := sessions.BudgetPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "budget", CycleID: "cycle-one", Harness: "codex", Rationale: "owner-estimated fixture tokens; actual provider usage unavailable", CycleLimit: 100, WindowLimit: 100, RecoveryFloor: 10, Window: time.Hour, WindowOrigin: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), Tasks: map[string]sessions.BudgetTask{f.task.ID: {Phases: map[string]sessions.BudgetEstimate{}, IntegrationOperationID: "integration-contract-pending"}}}
	for phase, tokens := range map[string]int64{"implementation": 20, "review": 10, "correction": 5, "re-review": 5, "verification": 0, "integration": 10, "recovery": 5} {
		role := "implementation"
		if phase == "review" || phase == "re-review" {
			role = "review"
		}
		policy.Tasks[f.task.ID].Phases[phase] = sessions.BudgetEstimate{RoleID: role, Model: "model", Tokens: tokens, Provenance: "owner-estimate", ObservedAt: time.Now()}
	}
	policyFile := filepath.Join(private, "policy.json")
	verificationJSON(t, policyFile, policy)
	if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return budgetOwner(c, args, "configure") }, "--config", f.config, "--request", policyFile, "--json"); e != nil {
		t.Fatal(out, e)
	}

	for _, up := range []func(string) error{f.authority.UpgradeNativeAcceptance, f.authority.UpgradeNativePolicy, f.authority.UpgradeController} {
		if e = up(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	cp := sessions.CheckpointPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "checkpoint", Harness: "codex", TaskIDs: []string{f.task.ID}, MaxCycles: 5, MaxNoProgress: 5, ExpiresAt: time.Now().Add(30 * time.Minute)}
	if e = f.authority.ConfigureCheckpoints(f.owner, cp); e != nil {
		t.Fatal(e)
	}
	if e = f.authority.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "p", Revision: "life", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	bridgeFile := filepath.Join(private, "bridge.json")
	if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return swarmScopedActorAdd(c, args, true, true) }, "--config", f.config, "--project", "p", "--connection", bridgeFile, "--credential", filepath.Join(private, "bridge.token")); e != nil {
		t.Fatal(out, e)
	}
	var bridgeConnection native.Connection
	if e = privateJSON(bridgeFile, &bridgeConnection); e != nil {
		t.Fatal(e)
	}
	owner, e := application.OpenNativeControllerOwner(f.config)
	if e != nil {
		t.Fatal(e)
	}
	controllerPolicy := sessions.ControllerPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "controller", Harness: "codex", CapabilityDigest: swarm.CapabilityDigest, TaskIDs: []string{f.task.ID}, MaxWaves: 5, MaxConcurrency: 1, MaxCorrectionRounds: 2, MaxNoProgress: 5, TransientRetries: 1, MaxElapsed: 10 * time.Minute, RetryBackoff: time.Millisecond, ExpiresAt: time.Now().Add(20 * time.Minute)}
	if e = owner.Configure(controllerPolicy); e != nil {
		t.Fatal(e)
	}
	faultConnection := filepath.Join(private, "fault-controller.json")
	faultCredential := filepath.Join(private, "fault-controller.token")
	writes := 0
	faultEnroll := func(ctx *clikit.Ctx, args []string) error {
		return controllerEnrollFiles(ctx, args, func(file *os.File, data []byte) error {
			writes++
			if writes == 2 {
				_ = file.Close()
			}
			return writePrivate(file, data)
		})
	}
	faultOut, faultErr := verificationRun(t, f.w, faultEnroll, "--config", f.config, "--project", "p", "--connection", faultConnection, "--credential", faultCredential)
	if clikit.ExitCode(faultErr) != 3 || !strings.Contains(faultOut, "enrollment") || !strings.Contains(faultOut, "retained") || !strings.Contains(faultOut, faultConnection) || !strings.Contains(faultOut, faultCredential) {
		t.Fatal("post-issuance file failure concealed retained enrollment", faultOut, faultErr)
	}
	for _, path := range []string{faultConnection, faultCredential} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("retained private artifact removed", err)
		}
	}
	controllerFile := filepath.Join(private, "controller.json")
	if out, e := verificationRun(t, f.w, controllerEnroll, "--config", f.config, "--project", "p", "--connection", controllerFile, "--credential", filepath.Join(private, "controller.token")); e != nil {
		t.Fatal(out, e)
	}
	var controllerConnection native.Connection
	if e = privateJSON(controllerFile, &controllerConnection); e != nil {
		t.Fatal(e)
	}
	controller, e := application.OpenNativeController(controllerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	route := sessions.ControllerTaskRoute{BridgeSession: bridgeConnection.SessionID, WorkerSession: worker.SessionID, RunnerSession: f.runner.RunnerSessionID, ReviewerSession: reviewer.SessionID, VerificationGrantID: f.runner.Checks[0].GrantID, ReviewGrantID: reviewProfile.GrantID, CheckID: "unit", PromptSpecDigest: sessions.OutputDigest([]byte("fixture"))}
	startRequest := sessions.ControllerStartRequest{OperationKey: "start", InvocationID: "controller", ProjectID: "p", PolicyRevision: "controller", ControllerSessionID: controllerConnection.SessionID, CheckpointID: "checkpoint", Routes: map[string]sessions.ControllerTaskRoute{f.task.ID: route}}
	startFile := filepath.Join(private, "start-request.json")
	startResult := filepath.Join(private, "start-result.json")
	verificationJSON(t, startFile, startRequest)
	startCmd := exec.Command(os.Args[0], "-test.run=^TestNativeControllerCommittedTransitionCrashHelper$")
	startCmd.Env = append(os.Environ(), "DACLI_CONTROLLER_CRASH_CONNECTION="+controllerFile, "DACLI_CONTROLLER_CRASH_OWNER="+f.config, "DACLI_CONTROLLER_CRASH_REQUEST="+startFile, "DACLI_CONTROLLER_CRASH_RESULT="+startResult)
	startRaw, startErr := startCmd.CombinedOutput()
	var startExit *exec.ExitError
	if !errors.As(startErr, &startExit) || startExit.ExitCode() != 17 {
		t.Fatal("owner Start process did not crash after commit", string(startRaw), startErr)
	}
	var st sessions.ControllerStatus
	if e = privateJSON(startResult, &st); e != nil {
		t.Fatal(e)
	}
	beforeStart, _ := os.ReadFile(worker.Authority)
	startReplay, e := owner.Start(startRequest)
	afterStart, _ := os.ReadFile(worker.Authority)
	if e != nil || !startReplay.Historical || startReplay.Deadline != st.Deadline || !bytes.Equal(beforeStart, afterStart) {
		t.Fatal("Start crash replay reset invocation", startReplay, e)
	}

	step := func(key string) {
		t.Helper()
		request := sessions.ControllerStepRequest{OperationKey: key, ProjectID: "p", InvocationID: st.ID, ExpectedSequence: st.Sequence}
		requestFile := filepath.Join(private, "crash-request-"+key+".json")
		resultFile := filepath.Join(private, "crash-result-"+key+".json")
		verificationJSON(t, requestFile, request)
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeControllerCommittedTransitionCrashHelper$")
		cmd.Env = append(os.Environ(), "DACLI_CONTROLLER_CRASH_CONNECTION="+controllerFile, "DACLI_CONTROLLER_CRASH_CAPABILITY="+capability, "DACLI_CONTROLLER_CRASH_REQUEST="+requestFile, "DACLI_CONTROLLER_CRASH_RESULT="+resultFile)
		if key == "dispatch" {
			cmd.Env = append(cmd.Env, "DACLI_CONTROLLER_CRASH_DROP_RESULT=1")
		}
		var raw []byte
		var err error
		if key == "reserve" {
			otherRequest := request
			otherRequest.OperationKey = "competing-reserve"
			otherFile := filepath.Join(private, "competing-request.json")
			otherResult := filepath.Join(private, "competing-result.json")
			verificationJSON(t, otherFile, otherRequest)
			other := exec.Command(os.Args[0], "-test.run=^TestNativeControllerCommittedTransitionCrashHelper$")
			other.Env = append(os.Environ(), "DACLI_CONTROLLER_CRASH_CONNECTION="+controllerFile, "DACLI_CONTROLLER_CRASH_CAPABILITY="+capability, "DACLI_CONTROLLER_CRASH_REQUEST="+otherFile, "DACLI_CONTROLLER_CRASH_RESULT="+otherResult)
			var firstLog, secondLog bytes.Buffer
			cmd.Stdout = &firstLog
			cmd.Stderr = &firstLog
			other.Stdout = &secondLog
			other.Stderr = &secondLog
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			if e := other.Start(); e != nil {
				t.Fatal(e)
			}
			firstErr := cmd.Wait()
			secondErr := other.Wait()
			code := func(e error) int {
				var x *exec.ExitError
				if errors.As(e, &x) {
					return x.ExitCode()
				}
				return 0
			}
			// A bounded lock wait is storage1, not policy3. Preserve it and
			// retry once only after the winning process has terminated and its
			// committed wave is observed. The new process must prove stale CAS3.
			retryStorage := func(original *exec.Cmd, log string, err error) error {
				if code(err) != 1 || !strings.Contains(log, sessions.ErrStorage.Error()) {
					return err
				}
				current, e := controller.Status("p", st.ID)
				if e != nil || current.Sequence != st.Sequence+1 || current.Waves != 1 {
					t.Fatal("contended winner not durably observed before changed-condition retry", current, e)
				}
				t.Log("storage1 lock contention retained; winner terminal and wave committed before one fresh stale-CAS process")
				fresh := exec.Command(original.Path, original.Args[1:]...)
				fresh.Env = original.Env
				_, e = fresh.CombinedOutput()
				return e
			}
			if code(secondErr) == 17 {
				firstErr = retryStorage(cmd, firstLog.String(), firstErr)
			}
			if code(firstErr) == 17 {
				secondErr = retryStorage(other, secondLog.String(), secondErr)
			}
			if !((code(firstErr) == 17 && code(secondErr) == 3) || (code(firstErr) == 3 && code(secondErr) == 17)) {
				t.Fatal("two controller processes did not yield one committed wave and one CAS refusal", firstErr, secondErr, firstLog.String(), secondLog.String())
			}
			if code(firstErr) == 3 {
				request = otherRequest
				resultFile = otherResult
				err = secondErr
			} else {
				err = firstErr
			}
		} else {
			raw, err = cmd.CombinedOutput()
		}
		var exit *exec.ExitError
		wantedExit := 17
		if key == "dispatch" {
			wantedExit = 19
		}
		if !errors.As(err, &exit) || exit.ExitCode() != wantedExit {
			t.Fatal("controller process did not reach committed transition crash", key, string(raw), err)
		}
		var next sessions.ControllerStatus
		if key == "dispatch" {
			var e error
			next, e = controller.Status("p", st.ID)
			if e != nil {
				t.Fatal(e)
			}
			if next.HostActionAuthorized || len(next.Actions) != 1 || next.Actions[0].IntentID == "" {
				t.Fatal("lost dispatch response manufactured replacement permit", next)
			}
		} else if err := privateJSON(resultFile, &next); err != nil {
			t.Fatal(err)
		}
		controller, err = application.OpenNativeController(controllerFile, capability)
		if err != nil {
			t.Fatal(err)
		}
		current, err := controller.Status("p", st.ID)
		if err != nil || current.Sequence != next.Sequence || current.Waves != next.Waves || !reflect.DeepEqual(current.Actions, next.Actions) {
			t.Fatal("restart lost committed transition", key, current, err)
		}
		before, err := os.ReadFile(worker.Authority)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := controller.Step(request)
		after, _ := os.ReadFile(worker.Authority)
		if err != nil || !replay.Historical || replay.HostActionAuthorized || replay.Sequence != next.Sequence || replay.Waves != next.Waves || !bytes.Equal(before, after) {
			t.Fatal("committed crash replay duplicated transition or permit", key, replay, err)
		}
		st = next
		t.Log("committed process stage", key, st.Phase, st.Corrections)
	}
	step("reserve")
	if st.Waves != 1 || len(st.Actions) != 1 {
		b, _ := json.Marshal(st.Checkpoint)
		t.Fatal("actual controller did not reserve nonzero wave", string(b))
	}
	step("dispatch")
	if st.HostActionAuthorized || st.Actions[0].IntentID == "" {
		t.Fatal("restart reissued lost host permit or lost original intent")
	}
	// The response was lost before any external launcher action. A separate
	// fixture bridge now reconciles that original opaque host correlation;
	// this test never claims that a model or host process was launched.
	action := st.Actions[0]
	at, e := f.authority.RegisterAttempt(wt, worker.SessionID, sessions.AttemptRequest{RequestID: "controller-worker", TaskID: f.task.ID, Checkout: f.w.Root})
	if e != nil {
		t.Fatal(e)
	}
	bridge, e := application.OpenNativeSwarm(bridgeFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	correlated, e := bridge.CorrelateDispatch(sessions.CorrelateDispatchIntent{OperationKey: "correlate", DispatchID: action.DispatchID, IntentID: action.IntentID, ExpectedSequence: 1, HostAgentID: "fixture-host", State: "host_correlated", ExpectedFence: action.ReservationFence})
	if e != nil {
		t.Fatal(e)
	}
	workerAccess, e := application.OpenNativeSwarm(workerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	intent, e := workerAccess.AcknowledgeDispatch(sessions.AcknowledgeDispatchIntent{OperationKey: "ack", DispatchID: action.DispatchID, IntentID: action.IntentID, AttemptID: at.ID, HostAgentID: correlated.HostAgentID, ExpectedSequence: correlated.Sequence, ExpectedFence: action.ReservationFence})
	if e != nil {
		t.Fatal(e)
	}
	bt, e := native.ReadCredential(bridgeConnection.CredentialFile)
	if e != nil {
		t.Fatal(e)
	}
	binding := sessions.LifecycleBinding{DispatchID: intent.DispatchID, IntentID: intent.ID, BridgeSessionID: route.BridgeSession, WorkerSessionID: worker.SessionID, TaskID: f.task.ID, Checkout: intent.Reservation.Reservation.Checkout, BaseCommit: intent.Reservation.BaseCommit, HostAgentID: intent.HostAgentID, AttemptID: at.ID, LeaseID: intent.Lease.ID, ReservationFence: intent.Reservation.Reservation.Fence, LeaseFence: intent.Lease.Fence}
	if _, e = f.authority.ObserveLifecycle(bt, route.BridgeSession, sessions.ObserveLifecycleRequest{OperationKey: "completed", Binding: binding, ExpectedSequence: 0, HostSequence: 1, State: "completed", Source: "owner-manual-native-host", SourceTime: time.Now(), EvidenceDigest: sessions.OutputDigest([]byte("cooperative report"))}); e != nil {
		t.Fatal(e)
	}
	revision, e := f.authority.PublishAttemptRevision(wt, worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "published", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	step("verification-descriptor")
	if st.Phase != "verification" {
		t.Fatal("completed producer did not progress", st)
	}
	v := st.Actions[0].Verification
	verificationJSON(t, f.requestFile, verificationRequest{1, v.OperationKey, v.AttemptID, v.LeaseID, v.RevisionID, v.CheckID, v.ExpectedFence})
	out, e := verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
	if e != nil {
		t.Fatal(out, e)
	}
	var result struct {
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatal(e)
	}
	// Reopen after actual execution, before controller bookkeeping. Same producer
	// key recovers its immutable evidence and cannot execute the command again.
	controller, e = application.OpenNativeController(controllerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	again, e := verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
	if e != nil || again != out {
		t.Fatal("command replay differed", again, e)
	}
	count, e := os.ReadFile(f.marker)
	if e != nil || string(count) != "ran" {
		t.Fatal("actual configured command duplicated", string(count), e)
	}
	if correction {
		if result.Evidence.ExitCode != 1 {
			t.Fatal("fixture did not run actual failing command", result.Evidence)
		}
		step("correction")
		if st.Corrections != 1 || st.Actions[0].Kind != "correction" {
			t.Fatal("actual failed command did not issue one correction", st)
		}
		if e = os.WriteFile(filepath.Join(f.w.Root, "src", "ready"), []byte("corrected"), 0600); e != nil {
			t.Fatal(e)
		}
		verificationGit(t, f.w.Root, "add", "src/ready")
		verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "actual correction")
		revision, e = f.authority.PublishAttemptRevision(wt, worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "corrected-revision", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence, ExpectedRevision: revision.Sequence})
		if e != nil {
			t.Fatal("corrected revision publication", e)
		}
		step("corrected-verification")
		v = st.Actions[0].Verification
		verificationJSON(t, f.requestFile, verificationRequest{1, v.OperationKey, v.AttemptID, v.LeaseID, v.RevisionID, v.CheckID, v.ExpectedFence})
		out, e = verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
		if e != nil {
			t.Fatal(out, e)
		}
		if e = json.Unmarshal([]byte(out), &result); e != nil || result.Evidence.ExitCode != 0 {
			t.Fatal("corrected actual command did not produce successful evidence", result.Evidence, e)
		}
		again, e = verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
		if e != nil || again != out {
			t.Fatal("corrected command replay duplicated execution", e)
		}
		count, e = os.ReadFile(f.marker)
		if e != nil || string(count) != "ranran" {
			t.Fatal("actual correction executed more than once per revision", string(count), e)
		}
		var phaseContract application.NativePhaseContract
		if e = privateJSON(contract, &phaseContract); e != nil {
			t.Fatal(e)
		}
		phaseContract.IntegrationBase = revision.Observation.Commit
		verificationJSON(t, contract, phaseContract)
	}
	step("review-descriptor")
	if st.Phase != "review" || st.Actions[0].ActorID != reviewer.SessionID {
		t.Fatal("independent reviewer not selected", st)
	}
	selected, e := application.OpenNativeReviewProfile(reviewerFile, reviewConfig)
	if e != nil {
		t.Fatal(e)
	}
	reviewerAccess, e := selected.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	review, e := reviewerAccess.Propose(application.NativeReviewRequest{OperationKey: st.Actions[0].OperationKey, AttemptID: at.ID, LeaseID: intent.Lease.ID, RevisionID: revision.ID, ExpectedFence: intent.Lease.Fence, Outcome: "approve", Rationale: "actual independent fixture review", EvidenceIDs: []string{result.Evidence.ID}})
	if e != nil {
		t.Fatal(e)
	}
	step("integration-checkpoint")
	if st.Phase != "integration" || st.HaltClass != "pending_local_integration" || st.Actions[0].ReviewID != review.ID || st.Actions[0].EvidenceID != result.Evidence.ID || st.Checkpoint == nil || st.Checkpoint.Phase != "integration" || st.Terminal {
		t.Fatal("reviewed exact integration checkpoint falsely completed or lost producer refs", st)
	}
	if _, e = f.authority.RenewLease(wt, worker.SessionID, sessions.RenewLeaseRequest{OperationKey: "protection-retained", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence, ExpiresAt: intent.Lease.ExpiresAt}); e != nil {
		t.Fatal("controller released retained lease", e)
	}
	controllerPolicy.Revision = "changed-policy"
	if e = owner.Configure(controllerPolicy); e != nil {
		t.Fatal(e)
	}
	refusalRequest := filepath.Join(private, "refusal.json")
	verificationJSON(t, refusalRequest, sessions.ControllerStepRequest{OperationKey: "stable-refusal", ProjectID: "p", InvocationID: st.ID, ExpectedSequence: st.Sequence})
	handler := func(ctx *clikit.Ctx, args []string) error { return controllerCommand(ctx, args, "step") }
	haltResult := filepath.Join(private, "halt-result.json")
	haltCmd := exec.Command(os.Args[0], "-test.run=^TestNativeControllerCommittedTransitionCrashHelper$")
	haltCmd.Env = append(os.Environ(), "DACLI_CONTROLLER_CRASH_CONNECTION="+controllerFile, "DACLI_CONTROLLER_CRASH_CAPABILITY="+capability, "DACLI_CONTROLLER_CRASH_REQUEST="+refusalRequest, "DACLI_CONTROLLER_CRASH_RESULT="+haltResult, "DACLI_CONTROLLER_CRASH_RETAIN_REFUSAL=1")
	haltRaw, haltErr := haltCmd.CombinedOutput()
	var haltExit *exec.ExitError
	if !errors.As(haltErr, &haltExit) || haltExit.ExitCode() != 17 {
		t.Fatal("refusal halt process did not crash after durable recording", string(haltRaw), haltErr)
	}
	var halted sessions.ControllerStatus
	if e = privateJSON(haltResult, &halted); e != nil || !halted.Terminal || halted.HaltClass != "policy_refused" {
		t.Fatal("durable refusal halt missing after process crash", halted, e)
	}
	firstOut, firstErr := verificationRun(t, f.w, handler, "--connection", controllerFile, "--capability-file", capability, "--request", refusalRequest, "--json")
	if clikit.ExitCode(firstErr) != 3 || !strings.Contains(firstOut, "policy_refused") {
		t.Fatal("first policy refusal lost exit3", firstOut, firstErr)
	}
	before, _ := os.ReadFile(worker.Authority)
	replayOut, replayErr := verificationRun(t, f.w, handler, "--connection", controllerFile, "--capability-file", capability, "--request", refusalRequest, "--json")
	after, _ := os.ReadFile(worker.Authority)
	if clikit.ExitCode(replayErr) != 3 || !strings.Contains(replayOut, "Historical") || !bytes.Equal(before, after) {
		t.Fatal("immutable CLI refusal replay changed exit3 or authority", replayOut, replayErr)
	}
}

// The child exits immediately after the actual production transition commits.
// Its parent reopens and consumes the same immutable request before any next action.
func TestNativeControllerCommittedTransitionCrashHelper(t *testing.T) {
	connection := os.Getenv("DACLI_CONTROLLER_CRASH_CONNECTION")
	if connection == "" {
		t.Skip("only an isolated fixture subprocess")
	}
	var out sessions.ControllerStatus
	var e error
	if ownerFile := os.Getenv("DACLI_CONTROLLER_CRASH_OWNER"); ownerFile != "" {
		owner, err := application.OpenNativeControllerOwner(ownerFile)
		if err != nil {
			t.Fatal(err)
		}
		var request sessions.ControllerStartRequest
		if err = privateJSON(os.Getenv("DACLI_CONTROLLER_CRASH_REQUEST"), &request); err != nil {
			t.Fatal(err)
		}
		out, e = owner.Start(request)
	} else {
		controller, err := application.OpenNativeController(connection, os.Getenv("DACLI_CONTROLLER_CRASH_CAPABILITY"))
		if err != nil {
			t.Fatal(err)
		}
		var request sessions.ControllerStepRequest
		if err = privateJSON(os.Getenv("DACLI_CONTROLLER_CRASH_REQUEST"), &request); err != nil {
			t.Fatal(err)
		}
		out, e = controller.Step(request)
	}
	if errors.Is(e, sessions.ErrDenied) && !(out.ID != "" && os.Getenv("DACLI_CONTROLLER_CRASH_RETAIN_REFUSAL") == "1") {
		os.Exit(3)
	}
	if e != nil && !errors.Is(e, sessions.ErrDenied) {
		t.Fatal(e, out.HaltClass)
	}

	if os.Getenv("DACLI_CONTROLLER_CRASH_DROP_RESULT") == "1" {
		os.Exit(19)
	}
	data, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("DACLI_CONTROLLER_CRASH_RESULT"), data, 0600); e != nil {
		t.Fatal(e)
	}
	os.Exit(17)
}

package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/mcp/native"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestNativeBudgetOwnerCLIUpgradeCurrentPrivateWorkerAndGenuine12Refusal(t *testing.T) {
	f := setupVerification(t, "", true)
	for _, upgrade := range []func(string) error{f.authority.UpgradeSwarms, f.authority.UpgradeDispatchIntents, f.authority.UpgradeLifecycle, f.authority.UpgradeRecovery, f.authority.UpgradePreflight, f.authority.UpgradeCheckpoints} {
		if e := upgrade(f.owner); e != nil {
			t.Fatal(e)
		}
	}
	before, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	out, e := verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return budgetOwner(c, args, "upgrade") }, "--config", f.config)
	if e != nil || !strings.Contains(out, "schema13") {
		t.Fatalf("actual owner upgrade13: %v %s", e, out)
	}
	after, e := os.ReadFile(f.worker.Authority)
	if e != nil {
		t.Fatal(e)
	}
	var old, current map[string]json.RawMessage
	_ = json.Unmarshal(before, &old)
	_ = json.Unmarshal(after, &current)
	for key, value := range old {
		if key == "Version" || strings.HasPrefix(key, "Budget") {
			continue
		}
		if !bytes.Equal(value, current[key]) {
			t.Fatalf("schema13 migration changed prior history %s", key)
		}
	}
	server, e := native.Open(f.connection)
	if e != nil {
		t.Fatal(e)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture"}}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	identity := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"identity","arguments":{"schema_version":1}}}` + "\n"
	var reply bytes.Buffer
	if e = server.Serve(strings.NewReader(init+identity), &reply); e != nil || !strings.Contains(reply.String(), f.worker.SessionID) || strings.Contains(reply.String(), `"isError":true`) {
		t.Fatalf("current private worker unusable13: %v %s", e, reply.String())
	}
	path := os.Getenv("DACLI_SCHEMA12_BINARY")
	if path == "" {
		t.Skip("explicit genuine12 backup not selected")
	}
	binary, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(binary)
	if hex.EncodeToString(sum[:]) != "d2116ba9ab274368c648300fbf4f8eaaf39a83e6f9dc4611a07a9d7376503b05" {
		t.Fatal("selected binary is not pinned genuine12")
	}
	command := exec.Command(path, "native", "serve", "--connection", f.connection)
	command.Stdin = strings.NewReader(init + identity)
	raw, e := command.CombinedOutput()
	if e == nil || !strings.Contains(string(raw), "refused") {
		t.Fatalf("genuine12 accepted13: %v %s", e, raw)
	}
	unchanged, e := os.ReadFile(f.worker.Authority)
	if e != nil || !bytes.Equal(after, unchanged) {
		t.Fatal("genuine12 refusal mutated13", e)
	}
}

// Real command/private profile/producer-consumer contract fixture. Host IDs and
// diagnostic capability are fixture declarations, not an actual model launch.
func TestNativeBudgetCLIProducerProjectionActualVerificationReviewAndCrashSettlement(t *testing.T) {
	f := setupVerification(t, "", true)
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
	verificationJSON(t, contract, application.NativePhaseContract{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", TaskID: f.task.ID, Harness: "codex", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(30 * time.Minute), Models: []string{"model"}, IntegrationRoute: "owner-manual-local", IntegrationCheckout: f.w.Root, IntegrationBase: observed.Commit, IntegrationPolicyDigest: strings.Repeat("a", 64), ExternalStatus: "ready"})
	preflight := sessions.PreflightPolicy{SchemaVersion: 1, ProjectID: "p", Revision: "preflight", Harness: "codex", Rationale: "explicit fixture model and manual phase contracts", AcceptManualAssurance: true, ExpiresAt: time.Now().Add(30 * time.Minute), Tasks: map[string]sessions.PreflightTask{f.task.ID: {ImplementationRole: "implementation", ReviewRole: "review", RecoveryRole: "implementation", WorkerConnection: workerFile, RunnerProfile: f.runnerProfile, ContractProfile: contract, RequireVerification: true, RequireIntegration: true, OwnerAuthorizedIntegration: true}}}
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
	controllerFile := filepath.Join(private, "controller.json")
	if out, e := verificationRun(t, f.w, budgetControllerAdd, "--config", f.config, "--project", "p", "--connection", controllerFile, "--credential", filepath.Join(private, "controller.token")); e != nil {
		t.Fatal(out, e)
	}
	controller, e := application.OpenNativeSwarm(controllerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := controller.Plan("p")
	if e != nil {
		t.Fatal(e)
	}
	wave, e := controller.Reserve(sessions.ReserveWaveRequest{OperationKey: "wave", Plan: plan, ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil || len(wave.Dispatches) != 1 {
		t.Fatalf("actual CLI fixture budget wave: %+v %v", wave, e)
	}
	d := wave.Dispatches[0]
	at, e := f.authority.RegisterAttempt(wt, worker.SessionID, sessions.AttemptRequest{RequestID: "budget-worker", TaskID: f.task.ID, Checkout: f.w.Root})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.authority.ConfigureLifecycle(f.owner, sessions.LifecyclePolicy{SchemaVersion: 1, ProjectID: "p", Revision: "lifecycle", Source: "owner-manual-native-host", Freshness: time.Minute, CancellationEnabled: true, CancellationChannel: "unsupported"}); e != nil {
		t.Fatal(e)
	}
	bridgeFile := filepath.Join(private, "bridge.json")
	if out, e := verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return swarmScopedActorAdd(c, args, true, true) }, "--config", f.config, "--project", "p", "--connection", bridgeFile, "--credential", filepath.Join(private, "bridge.token")); e != nil {
		t.Fatal(out, e)
	}
	bridge, e := application.OpenNativeSwarm(bridgeFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	result, e := controller.RequestDispatch(sessions.RequestDispatchIntent{OperationKey: "intent", DispatchID: d.ID, BridgeSessionID: bridgeSession(t, bridgeFile), WorkerSessionID: worker.SessionID, PromptSpecDigest: sessions.OutputDigest([]byte("fixture")), ExpectedFence: d.Reservation.Fence})
	if e != nil {
		t.Fatal(e)
	}
	correlated, e := bridge.CorrelateDispatch(sessions.CorrelateDispatchIntent{OperationKey: "correlate", DispatchID: d.ID, IntentID: result.Intent.ID, ExpectedSequence: result.Intent.Sequence, HostAgentID: "fixture-host", State: "host_correlated", ExpectedFence: d.Reservation.Fence})
	if e != nil {
		t.Fatal(e)
	}
	workerAccess, e := application.OpenNativeSwarm(workerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	intent, e := workerAccess.AcknowledgeDispatch(sessions.AcknowledgeDispatchIntent{OperationKey: "ack", DispatchID: d.ID, IntentID: correlated.ID, AttemptID: at.ID, HostAgentID: correlated.HostAgentID, ExpectedSequence: correlated.Sequence, ExpectedFence: d.Reservation.Fence})
	if e != nil {
		t.Fatal(e)
	}
	revision, e := f.authority.PublishAttemptRevision(wt, worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "budget-revision", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence})
	if e != nil {
		t.Fatal(e)
	}
	verifyRequest := verificationRequest{1, "budget-command", at.ID, intent.Lease.ID, revision.ID, "unit", intent.Lease.Fence}
	verificationJSON(t, f.requestFile, verifyRequest)
	out, e := verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
	if e != nil {
		t.Fatal(out, e)
	}
	// Read-only reopening occurs after real configured command output was durable,
	// before its accounting settlement. Replay may recover but cannot rerun.
	again, e := verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
	if e != nil || again != out {
		t.Fatal("command crash replay differs", again, e)
	}
	count, e := os.ReadFile(f.marker)
	if e != nil || string(count) != "ran" {
		t.Fatal("actual command did not execute exactly once", string(count), e)
	}
	requestFile := filepath.Join(private, "budget-request.json")
	verificationJSON(t, requestFile, struct{ ProjectID string }{"p"})
	statusJSON, e := verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return budgetCommand(c, args, "status") }, "--connection", controllerFile, "--capability-file", capability, "--request", requestFile, "--json")
	if e != nil {
		t.Fatal(e, statusJSON)
	}
	var status sessions.BudgetStatus
	if e = json.Unmarshal([]byte(statusJSON), &status); e != nil {
		t.Fatal(e)
	}
	producer := ""
	for _, allocation := range status.Allocations {
		if allocation.DispatchID != d.ID {
			continue
		}
		for _, candidate := range allocation.AvailableProducers {
			if candidate.Phase == "verification" {
				producer = candidate.ID
			}
		}
	}
	if producer != verifyRequest.OperationKey {
		t.Fatalf("public status cannot supply actual command producer: %s", statusJSON)
	}
	settle := sessions.BudgetSettleRequest{OperationKey: "settle-command", DispatchID: d.ID, Phase: "verification", ProducerID: producer, ExpectedFence: d.Reservation.Fence}
	verificationJSON(t, requestFile, settle)
	settleCLI := func() (string, error) {
		return verificationRun(t, f.w, func(c *clikit.Ctx, args []string) error { return budgetCommand(c, args, "settle") }, "--connection", controllerFile, "--capability-file", capability, "--request", requestFile, "--json")
	}
	settled, e := settleCLI()
	if e != nil {
		t.Fatal(e, settled)
	}
	replay, e := settleCLI()
	if e != nil || settled != replay {
		t.Fatalf("actual CLI settlement replay differs: %s %s %v", settled, replay, e)
	}
	var allocation sessions.BudgetAllocation
	if e = json.Unmarshal([]byte(settled), &allocation); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, phase := range allocation.Phases {
		if phase.Phase == "verification" {
			found = phase.State == "charged" && phase.ProducerID == producer && phase.ProviderUsage == nil
		}
	}
	if !found {
		t.Fatalf("actual command settlement lost provenance: %s", settled)
	}
	reviewerFile := filepath.Join(private, "reviewer.json")
	reviewConfig := filepath.Join(private, "review-config.json")
	if out, e := verificationRun(t, f.w, reviewerAdd, "--config", f.config, "--task", f.task.ID, "--worker-session", worker.SessionID, "--runner-config", f.runnerProfile, "--policy-revision", "review-policy", "--policy-out", filepath.Join(private, "review-policy.json"), "--review-config", reviewConfig, "--connection", reviewerFile, "--credential", filepath.Join(private, "reviewer.token")); e != nil {
		t.Fatal(out, e)
	}
	selectedReviewer, e := application.OpenNativeReviewProfile(reviewerFile, reviewConfig)
	if e != nil {
		t.Fatal(e)
	}
	reviewAccess, e := selectedReviewer.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	var verificationOutcome struct {
		Evidence sessions.Evidence `json:"evidence"`
	}
	if e = json.Unmarshal([]byte(out), &verificationOutcome); e != nil {
		t.Fatal(e)
	}
	rr := application.NativeReviewRequest{OperationKey: "review-one", AttemptID: at.ID, LeaseID: intent.Lease.ID, RevisionID: revision.ID, ExpectedFence: intent.Lease.Fence, Outcome: "request-changes", Rationale: "fixture requires an actual correction revision", EvidenceIDs: []string{verificationOutcome.Evidence.ID}}
	review, e := reviewAccess.Propose(rr)
	if e != nil {
		t.Fatal(e)
	}
	settleFromPublic := func(phase, expected, key string) {
		t.Helper()
		current, e := controller.BudgetStatus("p")
		if e != nil {
			t.Fatal(e)
		}
		id := ""
		for _, allocation := range current.Allocations {
			if allocation.DispatchID == d.ID {
				for _, candidate := range allocation.AvailableProducers {
					if candidate.Phase == phase && candidate.ID == expected {
						id = candidate.ID
					}
				}
			}
		}
		if id == "" {
			t.Fatalf("public projection missing %s producer %s", phase, expected)
		}
		settle.OperationKey = key
		settle.Phase = phase
		settle.ProducerID = id
		verificationJSON(t, requestFile, settle)
		if reply, e := settleCLI(); e != nil {
			t.Fatal(reply, e)
		}
	}
	settleFromPublic("review", review.ID, "settle-review")
	if e = os.WriteFile(filepath.Join(f.w.Root, "src", "tracked"), []byte("actual fixture correction"), 0600); e != nil {
		t.Fatal(e)
	}
	verificationGit(t, f.w.Root, "add", "src/tracked")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "actual fixture correction")
	corrected, e := f.authority.PublishAttemptRevision(wt, worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "correction-revision", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence, ExpectedRevision: revision.Sequence})
	if e != nil {
		t.Fatal(e)
	}
	settleFromPublic("correction", corrected.ID, "settle-correction")
	verifyRequest.OperationKey = "correction-command"
	verifyRequest.RevisionID = corrected.ID
	verificationJSON(t, f.requestFile, verifyRequest)
	correctionOutput, e := verificationRun(t, f.w, verify, "--connection", workerFile, "--runner-config", f.runnerProfile, "--request", f.requestFile)
	if e != nil {
		t.Fatal(correctionOutput, e)
	}
	if e = json.Unmarshal([]byte(correctionOutput), &verificationOutcome); e != nil {
		t.Fatal(e)
	}
	rr.OperationKey = "re-review"
	rr.RevisionID = corrected.ID
	rr.Outcome = "approve"
	rr.EvidenceIDs = []string{verificationOutcome.Evidence.ID}
	selectedReviewer, e = application.OpenNativeReviewProfile(reviewerFile, reviewConfig)
	if e != nil {
		t.Fatal(e)
	}
	reviewAccess, e = selectedReviewer.Authenticate()
	if e != nil {
		t.Fatal(e)
	}
	reviewed, e := reviewAccess.Propose(rr)
	if e != nil {
		t.Fatal(e)
	}
	settleFromPublic("re-review", reviewed.ID, "settle-re-review")
	third, e := f.authority.PublishAttemptRevision(wt, worker.SessionID, sessions.PublishRevisionRequest{OperationKey: "third-publication", AttemptID: at.ID, LeaseID: intent.Lease.ID, ExpectedFence: intent.Lease.Fence, ExpectedRevision: corrected.Sequence})
	if e != nil {
		t.Fatal(e)
	}
	current, e := controller.BudgetStatus("p")
	if e != nil {
		t.Fatal(e)
	}
	originalCorrectionSeen := false
	for _, record := range current.Allocations {
		if record.DispatchID == d.ID {
			for _, candidate := range record.AvailableProducers {
				if candidate.Phase == "correction" {
					if candidate.ID == third.ID {
						t.Fatal("charged phase advertises another producer that settlement refuses")
					}
					if candidate.ID == corrected.ID {
						originalCorrectionSeen = true
					}
				}
			}
		}
	}
	if !originalCorrectionSeen {
		t.Fatal("charged phase lost original actionable producer")
	}
	// Reopen supported private controller and replay original receipt AFTER later
	// phases changed current ledger. Exact old operation must remain immutable.
	controller, e = application.OpenNativeSwarm(controllerFile, capability)
	if e != nil {
		t.Fatal(e)
	}
	originalSettle := sessions.BudgetSettleRequest{OperationKey: "settle-command", DispatchID: d.ID, Phase: "verification", ProducerID: producer, ExpectedFence: d.Reservation.Fence}
	verificationJSON(t, requestFile, originalSettle)
	historical, e := settleCLI()
	if e != nil || historical != settled {
		t.Fatalf("later phase/restart changed historical settlement receipt: %v %s", e, historical)
	}
	count, e = os.ReadFile(f.marker)
	if e != nil || string(count) != "ranran" {
		t.Fatal("correction command count differs", string(count), e)
	}
	settle.OperationKey = "settle-contract"
	settle.Phase = "integration"
	settle.ProducerID = "integration-contract-pending"
	verificationJSON(t, requestFile, settle)
	pending, e := settleCLI()
	if e != nil {
		t.Fatal(e, pending)
	}
	if !strings.Contains(pending, "unknown-retained") {
		t.Fatalf("pending integration contract became success: %s", pending)
	}
	if reflect.DeepEqual(allocation, sessions.BudgetAllocation{}) {
		t.Fatal("empty outcome")
	}
}
func bridgeSession(t *testing.T, path string) string {
	t.Helper()
	var c native.Connection
	if e := privateJSON(path, &c); e != nil {
		t.Fatal(e)
	}
	return c.SessionID
}

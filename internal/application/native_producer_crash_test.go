package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
)

const nativeProducerCrashEnv = "DACLI_TEST_NATIVE_PRODUCER_CRASH_REQUEST"

type nativeProducerCrashRequest struct {
	Kind, Stage, OwnerConfig, ReviewPolicy, IntegrationPolicy, OperationKey string
	Acceptance                                                              sessions.NativeAcceptanceInput
	Integration                                                             sessions.NativeIntegrationInput
}

// This helper is a separate OS process so os.Exit models a lost producer, not
// an error return that the in-process application can accidentally finalize.
func TestNativeProducerCrashHelper(t *testing.T) {
	path := os.Getenv(nativeProducerCrashEnv)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var request nativeProducerCrashRequest
	if json.Unmarshal(raw, &request) != nil {
		t.Fatal("invalid native producer crash fixture")
	}
	switch request.Kind {
	case "acceptance":
		owner, err := OpenNativeAcceptanceOwner(request.OwnerConfig, request.ReviewPolicy)
		if err != nil {
			t.Fatal(err)
		}
		switch request.Stage {
		case "before-effect":
			owner.testBeforeCanonicalClose = func() { os.Exit(71) }
		case "after-effect":
			owner.testAfterCanonicalClose = func() { os.Exit(72) }
		case "after-save-before-move":
			owner.testAfterTaskSave = func() { os.Exit(76) }
		case "after-finalization":
		default:
			t.Fatalf("unknown acceptance crash stage %q", request.Stage)
		}
		result, err := owner.Apply(request.Acceptance, "")
		if err != nil {
			t.Fatal(err)
		}
		if request.Stage == "after-finalization" {
			if !result.Applied || result.State != "closed" {
				t.Fatalf("acceptance did not return a durably closed result: %+v", result)
			}
			os.Exit(77)
		}
	case "integration":
		owner, err := OpenNativeIntegrationOwner(request.OwnerConfig, request.ReviewPolicy, request.IntegrationPolicy)
		if err != nil {
			t.Fatal(err)
		}
		switch request.Stage {
		case "before-effect":
			owner.testBeforeGitEffect = func() { os.Exit(73) }
		case "after-ref-cas":
			owner.testAfterRefCAS = func() { os.Exit(74) }
		case "after-effect":
			owner.testAfterGitEffect = func() { os.Exit(75) }
		case "after-finalization":
		default:
			t.Fatalf("unknown integration crash stage %q", request.Stage)
		}
		result, err := owner.Apply(context.Background(), request.Integration)
		if err != nil {
			t.Fatalf("actual native integration producer returned before crash boundary: %v; candidate=%+v policy=%+v", err, request.Integration.Candidate, owner.policy)
		}
		if request.Stage == "after-finalization" && result.Record.State != "integrated" {
			t.Fatalf("integration did not return a durably integrated result: %+v %v", result, err)
		}
		if request.Stage == "after-finalization" {
			os.Exit(78)
		}
	default:
		t.Fatalf("unknown producer %q", request.Kind)
	}
	t.Fatal("requested process-death boundary was not reached")
}

func TestNativeAcceptanceProducerCrashRestartAtCanonicalClose(t *testing.T) {
	for _, stage := range []string{"before-effect", "after-save-before-move", "after-effect", "after-finalization"} {
		t.Run(stage, func(t *testing.T) {
			useCanonicalTestTempParent(t)
			f := acceptanceSetup(t)
			before, err := os.ReadFile(f.task.Path)
			if err != nil {
				t.Fatal(err)
			}
			runNativeProducerCrashChild(t, nativeProducerCrashRequest{
				Kind: "acceptance", Stage: stage, OwnerConfig: f.access.owner.path,
				ReviewPolicy: f.access.policyPath, Acceptance: f.input,
			})
			pending := readNativeAcceptanceRecord(t, f.access.owner.config.Authority, f.input.OperationKey)
			if stage == "after-finalization" {
				if pending.State != "closed" || pending.CanonicalReceiptDigest == "" {
					t.Fatalf("post-finalization child did not leave a closed receipt: %+v", pending)
				}
				assertNoNativeAuthorityLock(t, f.access.owner.path)
				fresh, openErr := OpenNativeAcceptanceOwner(f.access.owner.path, f.access.policyPath)
				if openErr != nil {
					t.Fatalf("fresh owner could not reopen finalized authority: %v", openErr)
				}
				donePath := filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path))
				committed, readErr := os.ReadFile(donePath)
				task, taskErr := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
				if readErr != nil || taskErr != nil || task.Status != model.StatusDone {
					t.Fatalf("post-finalization task is not durably done: task=%+v read=%v taskErr=%v", task, readErr, taskErr)
				}
				receipt, present := task.Doc.Front.Get("native_acceptance.receipt")
				if !present || receipt != pending.CanonicalReceiptDigest {
					t.Fatalf("post-finalization task receipt differs from authority: marker=%q record=%q present=%v", receipt, pending.CanonicalReceiptDigest, present)
				}
				result, replayErr := fresh.Apply(f.input, "")
				afterReplay, afterErr := os.ReadFile(donePath)
				if replayErr != nil || !result.Duplicate || string(afterReplay) != string(committed) {
					t.Fatalf("fresh post-finalization replay changed canonical task: %+v err=%v read=%v", result, replayErr, afterErr)
				}
				return
			}
			if pending.State != "closing" {
				t.Fatalf("producer death did not preserve a pending close: %+v", pending)
			}
			switch stage {
			case "before-effect":
				after, readErr := os.ReadFile(f.task.Path)
				if readErr != nil || string(after) != string(before) {
					t.Fatal("pre-effect crash changed canonical task", readErr)
				}
			case "after-save-before-move":
				saved, readErr := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
				if readErr != nil || saved.Status != f.task.Status || saved.Path != f.task.Path {
					t.Fatalf("save-before-move crash did not leave the task in its source status: %+v %v", saved, readErr)
				}
				receipt, present := saved.Doc.Front.Get("native_acceptance.receipt")
				digest, digestErr := store.ReadNativeAcceptanceOutcomeDigest(f.w, f.task.ID, "p")
				if !present || receipt != pending.CanonicalReceiptDigest || digestErr != nil || digest != pending.CanonicalOutcomeDigest {
					t.Fatalf("durable saved outcome does not match pending authority receipt: receipt=%q digest=%q pending=%+v err=%v", receipt, digest, pending, digestErr)
				}
				donePath := filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path))
				if _, statErr := os.Stat(donePath); !os.IsNotExist(statErr) {
					t.Fatalf("save-before-move crash unexpectedly placed task in done: %v", statErr)
				}
			case "after-effect":
				task, readErr := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
				if readErr != nil || task.Status != model.StatusDone || !store.LogHasStamp(task, "completed by native-owner") {
					t.Fatalf("post-effect crash did not leave the exact canonical close: %+v %v", task, readErr)
				}
			}

			if _, err = OpenNativeAcceptanceOwner(f.access.owner.path, f.access.policyPath); err == nil {
				t.Fatal("dead transaction lock must fail closed before owner reconciliation")
			}
			removeReapedProducerLock(t, f.access.owner.path)
			fresh, err := OpenNativeAcceptanceOwner(f.access.owner.path, f.access.policyPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := fresh.Apply(f.input, "")
			if err != nil || !result.Applied {
				t.Fatalf("fresh producer failed to reconcile exact operation: %+v %v", result, err)
			}
			closed := readNativeAcceptanceRecord(t, f.access.owner.config.Authority, f.input.OperationKey)
			if closed.State != "closed" || closed.CanonicalReceiptDigest == "" {
				t.Fatalf("canonical receipt not durably finalized: %+v", closed)
			}
			committed, err := os.ReadFile(filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path)))
			if err != nil || strings.Count(string(committed), "native_acceptance.receipt") != 1 || strings.Count(string(committed), "completed by native-owner") != 1 {
				t.Fatalf("canonical outcome is missing or duplicated: %v", err)
			}
			closedTask, taskErr := store.NativeAcceptanceTask(f.w, f.task.ID, "p")
			if taskErr != nil {
				t.Fatalf("cannot reload canonical closed task: %v", taskErr)
			}
			receipt, markerPresent := closedTask.Doc.Front.Get("native_acceptance.receipt")
			if !markerPresent || receipt != closed.CanonicalReceiptDigest {
				t.Fatalf("canonical task receipt does not match exact authority receipt: marker=%q record=%q present=%v", receipt, closed.CanonicalReceiptDigest, markerPresent)
			}
			result, err = fresh.Apply(f.input, "")
			afterReplay, readErr := os.ReadFile(filepath.Join(f.w.TasksDir("p", model.StatusDone), filepath.Base(f.task.Path)))
			if err != nil || readErr != nil || !result.Duplicate || string(afterReplay) != string(committed) {
				t.Fatalf("fresh replay repeated canonical side effect: %+v %v %v", result, err, readErr)
			}
		})
	}
}

func TestNativeIntegrationProducerCrashRestartAtGitEffects(t *testing.T) {
	for _, stage := range []string{"before-effect", "after-ref-cas", "after-effect", "after-finalization"} {
		t.Run(stage, func(t *testing.T) {
			useCanonicalTestTempParent(t)
			f, owner, input, expected := nativeIntegrationProducerFixture(t)
			_, ownerCredential, _, authErr := owner.authenticate()
			if authErr != nil || owner.selectionCurrent(ownerCredential) != nil {
				t.Fatalf("fresh owner selections are not current: %v", authErr)
			}
			if observation, observeErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(owner.policy, input.Candidate)); observeErr != nil || observation.Commit != expected || !observation.Clean {
				t.Fatalf("target preflight mismatch: %+v %v", observation, observeErr)
			}
			runNativeProducerCrashChild(t, nativeProducerCrashRequest{
				Kind: "integration", Stage: stage, OwnerConfig: f.access.owner.path,
				ReviewPolicy: f.access.policyPath, IntegrationPolicy: owner.policyPath,
				Integration: input,
			})

			if stage == "after-finalization" {
				assertNoNativeAuthorityLock(t, f.access.owner.path)
				fresh, openErr := OpenNativeIntegrationOwner(f.access.owner.path, f.access.policyPath, owner.policyPath)
				if openErr != nil {
					t.Fatalf("fresh owner could not reopen finalized integration authority: %v", openErr)
				}
				record, readErr := fresh.Inspect(input.OperationKey)
				if readErr != nil || record.State != "integrated" || record.ReceiptDigest == "" || record.Outcome.Commit != input.Candidate.Commit || record.Outcome.Tree != input.Candidate.Tree {
					t.Fatalf("post-finalization integration receipt is not durable and exact: %+v err=%v", record, readErr)
				}
				before, observeErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
				if observeErr != nil || before.Commit != input.Candidate.Commit || before.Tree != input.Candidate.Tree || !before.Clean {
					t.Fatalf("post-finalization target differs from durable outcome: %+v err=%v", before, observeErr)
				}
				path := filepath.Join(fresh.policy.TargetCheckout, "src")
				bytesBefore, fileErr := os.ReadFile(path)
				if fileErr != nil {
					t.Fatal(fileErr)
				}
				replayed, replayErr := fresh.Reconcile(context.Background(), input)
				after, afterErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
				bytesAfter, afterFileErr := os.ReadFile(path)
				if replayErr != nil || !replayed.Duplicate || afterErr != nil || after.Commit != before.Commit || after.Tree != before.Tree || after.Clean != before.Clean || afterFileErr != nil || string(bytesAfter) != string(bytesBefore) {
					t.Fatalf("fresh post-finalization replay changed Git state: result=%+v replay=%v before=%+v after=%+v fileErr=%v/%v", replayed, replayErr, before, after, fileErr, afterFileErr)
				}
				return
			}
			if _, err := OpenNativeIntegrationOwner(f.access.owner.path, f.access.policyPath, owner.policyPath); err == nil {
				t.Fatal("dead transaction lock must fail closed before owner reconciliation")
			}
			removeReapedProducerLock(t, f.access.owner.path)
			fresh, err := OpenNativeIntegrationOwner(f.access.owner.path, f.access.policyPath, owner.policyPath)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
			if err != nil {
				t.Fatal(err)
			}
			switch stage {
			case "before-effect":
				if observed.Commit != expected || observed.Tree != fresh.policy.ExpectedTree || !observed.Clean {
					t.Fatalf("pre-effect crash changed target: %+v", observed)
				}
			case "after-ref-cas":
				if observed.Commit != input.Candidate.Commit || observed.Clean {
					t.Fatalf("ref-CAS crash must retain the partial/dirty worktree: %+v", observed)
				}
			case "after-effect":
				if observed.Commit != input.Candidate.Commit || observed.Tree != input.Candidate.Tree || !observed.Clean {
					t.Fatalf("post-effect crash did not leave exact integrated tree: %+v", observed)
				}
			}

			result, reconcileErr := fresh.Reconcile(context.Background(), input)
			record, readErr := fresh.Inspect(input.OperationKey)
			if stage == "after-effect" {
				if reconcileErr != nil || result.Record.State != "integrated" || readErr != nil || record.State != "integrated" || record.Outcome.Commit != input.Candidate.Commit || record.Outcome.Tree != input.Candidate.Tree {
					t.Fatalf("fresh process failed exact Git readback reconciliation: %+v %+v %v %v", result, record, reconcileErr, readErr)
				}
			} else {
				if !errors.Is(reconcileErr, sessions.ErrReconcile) || readErr != nil || record.State != "pending" {
					t.Fatalf("unknown or partial effect must stay pending: %+v %+v %v %v", result, record, reconcileErr, readErr)
				}
				beforeRetry, observeErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
				if observeErr != nil {
					t.Fatal(observeErr)
				}
				if _, applyErr := fresh.Apply(context.Background(), input); !errors.Is(applyErr, sessions.ErrReconcile) {
					t.Fatalf("pending operation was allowed to rerun Git effect: %v", applyErr)
				}
				afterRetry, observeErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
				if observeErr != nil || beforeRetry.Commit != afterRetry.Commit || beforeRetry.Tree != afterRetry.Tree || beforeRetry.Clean != afterRetry.Clean {
					t.Fatalf("pending replay changed actual Git state: before=%+v after=%+v err=%v", beforeRetry, afterRetry, observeErr)
				}
			}
			if stage == "after-effect" {
				replayed, err := fresh.Reconcile(context.Background(), input)
				afterReplay, observeErr := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(fresh.policy, input.Candidate))
				if err != nil || !replayed.Duplicate || observeErr != nil || afterReplay.Commit != input.Candidate.Commit || afterReplay.Tree != input.Candidate.Tree || !afterReplay.Clean {
					t.Fatalf("integrated replay repeated or lost effect: %+v %+v %v %v", replayed, afterReplay, err, observeErr)
				}
			}
		})
	}
}

func nativeIntegrationProducerFixture(t *testing.T) (acceptanceFixture, *NativeIntegrationOwner, sessions.NativeIntegrationInput, string) {
	t.Helper()
	f := acceptanceSetup(t)
	base := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", f.input.Commit+"^")
	baseTree := nativeAcceptanceGitOutput(t, f.w.Root, "rev-parse", base+"^{tree}")
	observed, err := checkout.Observe(f.w.Root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(observed.Repository)
	if err != nil {
		t.Fatal(err)
	}
	nativeGit(t, f.w.Root, "update-ref", "refs/heads/main", base)
	nativeGit(t, f.w.Root, "reset", "--hard", base)
	targetRoot := filepath.Join(t.TempDir(), "integration-target")
	nativeGit(t, f.w.Root, "branch", "integration-target", base)
	nativeGit(t, f.w.Root, "worktree", "add", targetRoot, "integration-target")
	canonicalCheckout, err := filepath.EvalSymlinks(targetRoot)
	if err != nil {
		t.Fatal(err)
	}
	nativeGit(t, f.w.Root, "config", "core.logAllRefUpdates", "true")
	for _, upgrade := range []func(string) error{f.a.UpgradeNativePolicy, f.a.UpgradeController, f.a.UpgradeNativeModelRouting, f.a.UpgradeNativeIntegration} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	nativePolicy := sessions.NativePolicy{SchemaVersion: 1, ProjectID: "p", TaskID: f.task.ID, Revision: "native-integration-proof", Allow: true, ExpiresAt: time.Now().Add(time.Hour)}
	if err := f.a.ConfigureNativePolicy(f.owner, nativePolicy); err != nil {
		t.Fatal(err)
	}
	intPolicyPath := filepath.Join(filepath.Dir(f.access.owner.path), "integration-policy.json")
	intPolicy := sessions.NativeIntegrationPolicy{SchemaVersion: 1, ProjectID: "p", TaskID: f.task.ID, Revision: "integration-proof", Repository: canonicalRepository, TargetCheckout: canonicalCheckout, TargetRef: "refs/heads/integration-target", ExpectedCommit: base, ExpectedTree: baseTree, NativePolicyRevision: nativePolicy.Revision, Allow: true, ExpiresAt: time.Now().Add(time.Hour)}
	acceptanceJSON(t, intPolicyPath, intPolicy)
	owner, err := OpenNativeIntegrationOwner(f.access.owner.path, f.access.policyPath, intPolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Configure(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Produce a new exact revision, trusted check and independent review only
	// after integration policy configuration, as required by the admission.
	nativeGit(t, f.w.Root, "checkout", "-b", "worker")
	if err := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("finished\nafter-policy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, f.w.Root, "add", "src")
	nativeGit(t, f.w.Root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "post-policy worker revision")
	revision, err := f.a.PublishAttemptRevision(f.workerToken, f.worker.ID, sessions.PublishRevisionRequest{OperationKey: "integration-revision", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewNativeVerificationRunner(f.a, f.runnerToken, f.runnerID, []NativeCheck{{Descriptor: f.check, GrantID: f.verificationGrantID}})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := runner.Run(context.Background(), f.workerToken, NativeVerificationRequest{OperationKey: "integration-check", AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: revision.ID, CheckID: f.check.ID, ExpectedFence: f.lease.Fence, WorkerSession: f.worker.ID})
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.a.ProposeReview(f.reviewerToken, f.reviewer.ID, sessions.ReviewRequest{OperationKey: "integration-review", GrantID: f.grant.ID, AttemptID: f.at.ID, LeaseID: f.lease.ID, RevisionID: revision.ID, ExpectedFence: f.lease.Fence, Outcome: "approve", Rationale: "post-policy exact revision and check", EvidenceIDs: []string{evidence.ID}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := f.a.ReadNativeAcceptanceCandidate(f.reviewerToken, f.reviewer.ID, review.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Evidence) != 1 {
		t.Fatalf("expected one exact trusted producer, got %d", len(candidate.Evidence))
	}
	input := candidate.Input
	input.OperationKey = "integration-crash-proof"
	return f, owner, sessions.NativeIntegrationInput{OperationKey: input.OperationKey, PolicyRevision: intPolicy.Revision, Candidate: input}, base
}

func runNativeProducerCrashChild(t *testing.T, request nativeProducerCrashRequest) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestNativeProducerCrashHelper$")
	command.Env = append(store.VerificationEnvironment(), nativeProducerCrashEnv+"="+path)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	want := map[string]int{"acceptance/before-effect": 71, "acceptance/after-effect": 72, "acceptance/after-save-before-move": 76, "acceptance/after-finalization": 77, "integration/before-effect": 73, "integration/after-ref-cas": 74, "integration/after-effect": 75, "integration/after-finalization": 78}[request.Kind+"/"+request.Stage]
	if !errors.As(err, &exit) || exit.ExitCode() != want {
		t.Fatalf("producer did not die at requested actual side effect (want exit %d): err=%v output=%s", want, err, output)
	}
}

func useCanonicalTestTempParent(t *testing.T) {
	t.Helper()
	parent, err := os.MkdirTemp(os.TempDir(), "dacli-native-producer-")
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		_ = os.RemoveAll(parent)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(canonical) })
	t.Setenv("TMPDIR", canonical)
}

func assertNoNativeAuthorityLock(t *testing.T, ownerConfigPath string) {
	t.Helper()
	raw, err := os.ReadFile(ownerConfigPath)
	var config recoveryOwnerConfig
	if err != nil || json.Unmarshal(raw, &config) != nil {
		t.Fatalf("cannot inspect synthetic owner selection: %v", err)
	}
	if _, err := os.Lstat(config.Authority + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("finalized producer left an authority transaction lock: %v", err)
	}
}

func readNativeAcceptanceRecord(t *testing.T, authorityPath, operationKey string) sessions.NativeAcceptanceRecord {
	t.Helper()
	var state struct {
		NativeAcceptanceOperations map[string]sessions.NativeAcceptanceRecord
	}
	raw, err := os.ReadFile(authorityPath)
	if err != nil || json.Unmarshal(raw, &state) != nil {
		t.Fatalf("cannot read synthetic acceptance state: %v", err)
	}
	record, ok := state.NativeAcceptanceOperations[operationKey]
	if !ok {
		t.Fatalf("native acceptance operation %q is absent", operationKey)
	}
	return record
}

func removeReapedProducerLock(t *testing.T, ownerConfigPath string) {
	t.Helper()
	raw, err := os.ReadFile(ownerConfigPath)
	var config recoveryOwnerConfig
	if err != nil || json.Unmarshal(raw, &config) != nil {
		t.Fatalf("cannot inspect synthetic owner selection: %v", err)
	}
	lock := config.Authority + ".lock"
	info, err := os.Lstat(lock)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("expected exact regular stale lock after confirmed child exit: %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatalf("fixture owner could not reconcile reaped child lock: %v", err)
	}
}

package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/procmon"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func TestNativeStorageOwnerSnapshotPreservesSchema16AndRefusesWorker(t *testing.T) {
	f := controllerSetup(t)
	before, e := os.ReadFile(f.a.path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.a.ObserveStorage(f.token); !errors.Is(e, ErrDenied) {
		t.Fatalf("worker read owner storage snapshot: %v", e)
	}
	out, e := f.a.ObserveStorage(f.owner)
	if e != nil {
		t.Fatal(e)
	}
	if out.AuthorityVersion != 16 || !bytes.Equal(out.PrivateBytes, before) || out.Collections["Sessions"].Count < 2 || out.Collections["ControllerPolicies"].Count != 1 {
		t.Fatal("storage lost schema16 histories or identity counts")
	}
	public, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	for _, private := range []string{f.owner, f.token, hash(f.owner), "OwnerHash", "CredentialHash", "PrivateBytes", "exit 1"} {
		if strings.Contains(string(public), private) {
			t.Fatal("storage public report exposed private authority")
		}
	}
	after, e := os.ReadFile(f.a.path)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("storage observation mutated source authority")
	}
}
func TestNativeStorageHoldsCoverBudgetPolicyAndAcceptanceUnknown(t *testing.T) {
	f := controllerSetup(t)
	if e := f.a.transaction(false, true, func(s *state) error {
		s.BudgetAllocations["budget-unknown"] = BudgetAllocation{TaskID: "task-a", Phases: []BudgetPhase{{State: "unknown-retained"}}}
		s.NativePolicyConsumptions["decision-unknown"] = NativePolicyConsumption{DecisionID: "decision-unknown", OperationKey: "accept-unknown"}
		s.NativeAcceptanceOperations["accept-closing"] = NativeAcceptanceRecord{State: "closing", Candidate: NativeAcceptanceCandidate{Input: NativeAcceptanceInput{TaskID: "task-c"}}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.a.path)
	out, e := f.a.ObserveStorage(f.owner)
	if e != nil {
		t.Fatal(e)
	}
	wanted := map[string]bool{"native budget liability retained": false, "native policy consumption outcome unresolved": false, "native acceptance operation unresolved": false}
	for _, h := range out.Holds {
		if _, ok := wanted[h.Reason]; ok {
			wanted[h.Reason] = true
		}
	}
	for reason, found := range wanted {
		if !found {
			t.Fatalf("native storage dropped unresolved hold: %s", reason)
		}
	}
	after, _ := os.ReadFile(f.a.path)
	if !bytes.Equal(before, after) {
		t.Fatal("hold inventory mutated source authority")
	}
}

func nativeStorageLatestFixture(t *testing.T) controllerFixture {
	t.Helper()
	f := controllerSetup(t)
	for _, upgrade := range []func(string) error{f.a.UpgradeNativeModelRouting, f.a.UpgradeNativeIntegration, f.a.UpgradeNativeExternal} {
		if err := upgrade(f.owner); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestNativeStorageHoldsUnresolvedNativeIntegrationAndExternalOperations(t *testing.T) {
	for _, kind := range []string{"integration", "external"} {
		for _, caseState := range []string{"pending", "unknown", "invalid-terminal", "missing-task", "mismatched-task", "mismatched-operation", "completed"} {
			t.Run(kind+"/"+caseState, func(t *testing.T) {
				f := nativeStorageLatestFixture(t)
				if err := f.a.transaction(false, true, func(s *state) error {
					binding := s.Tasks["task-a"]
					switch kind {
					case "integration":
						input := NativeIntegrationInput{OperationKey: "integrate-test", PolicyRevision: "integration-policy", Candidate: NativeAcceptanceInput{OperationKey: "integrate-test", ProjectID: binding.ProjectID, TaskID: "task-a", Repository: binding.Repository, Commit: "target", Tree: "target-tree"}}
						policy := NativeIntegrationPolicy{TaskID: "task-a", ProjectID: binding.ProjectID, Revision: input.PolicyRevision, Repository: input.Candidate.Repository, ExpectedCommit: "base", ExpectedTree: "base-tree"}
						rec := NativeIntegrationRecord{OperationKey: input.OperationKey, InputDigest: identity("native-integration-input", "", input), State: "pending", Input: input, Policy: policy, Before: NativeIntegrationObservation{Commit: policy.ExpectedCommit, Tree: policy.ExpectedTree, Clean: true, ObservedAt: time.Now().UTC()}}
						switch caseState {
						case "unknown":
							rec.State = "unknown"
						case "invalid-terminal":
							rec.State = "integrated"
							rec.Outcome = NativeIntegrationObservation{Commit: input.Candidate.Commit, Tree: input.Candidate.Tree, Clean: true, ObservedAt: time.Now().UTC()}
							rec.CompletedAt = time.Now().UTC()
							rec.ReceiptDigest = "invalid"
						case "missing-task":
							rec.Input.Candidate.TaskID = ""
						case "mismatched-task":
							rec.Policy.TaskID = "task-b"
						case "mismatched-operation":
							rec.OperationKey = "other-operation"
						case "completed":
							rec.State = "integrated"
							rec.Outcome = NativeIntegrationObservation{Commit: input.Candidate.Commit, Tree: input.Candidate.Tree, Clean: true, ObservedAt: time.Now().UTC()}
							rec.CompletedAt = time.Now().UTC()
							rec.ReceiptDigest = identity("native-integration-receipt", "", rec)
						}
						s.NativeIntegrationOperations[input.OperationKey] = rec
					case "external":
						input := NativeExternalInput{OperationKey: "external-test", PolicyRevision: "external-policy", Action: "push", Candidate: NativeAcceptanceInput{OperationKey: "external-test", ProjectID: binding.ProjectID, TaskID: "task-a", Repository: binding.Repository, Commit: "target", Tree: "target-tree"}}
						policy := NativeExternalPolicy{ProjectID: binding.ProjectID, TaskID: "task-a", Revision: input.PolicyRevision, Repository: input.Candidate.Repository, GitHubRepository: "owner/repo", SourceBranch: "branch", TargetBranch: "main", Commit: input.Candidate.Commit, Tree: input.Candidate.Tree, Generation: 1, SliceGeneration: 1}
						before := NativeExternalObservation{Generation: 1, SliceGeneration: 1, Branch: "branch", Commit: "target", Tree: "target-tree", Trunk: "base", RemoteHead: "base", Clean: true, APIComplete: true, ObservedAt: time.Now().UTC()}
						rec := NativeExternalRecord{OperationKey: input.OperationKey, InputDigest: identity("native-external-input", "", input), State: "pending", Input: input, Policy: policy, Before: before}
						switch caseState {
						case "unknown":
							rec.State = "unknown"
						case "invalid-terminal":
							rec.State = "observed"
							rec.Outcome = NativeExternalObservation{Generation: 1, SliceGeneration: 1, Branch: "branch", Commit: "target", Tree: "target-tree", Trunk: "base", RemoteHead: "wrong", Clean: true, APIComplete: true, ObservedAt: time.Now().UTC()}
						case "missing-task":
							rec.Input.Candidate.TaskID = ""
						case "mismatched-task":
							rec.Policy.TaskID = "task-b"
						case "mismatched-operation":
							rec.OperationKey = "other-operation"
						case "completed":
							rec.State = "observed"
							rec.Outcome = NativeExternalObservation{Generation: 1, SliceGeneration: 1, Branch: "branch", Commit: "target", Tree: "target-tree", Trunk: "base", RemoteHead: "target", Clean: true, APIComplete: true, ObservedAt: time.Now().UTC()}
						}
						s.NativeExternalOperations[input.OperationKey] = sealExternal(rec)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(f.a.path)
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.a.ObserveStorage(f.owner)
				if err != nil {
					t.Fatal(err)
				}
				wantHold := caseState != "completed"
				wantTask := "task-a"
				if caseState == "missing-task" || caseState == "mismatched-task" {
					wantTask = ""
				}
				found := 0
				for _, hold := range out.Holds {
					if hold.Reason == "native "+kind+" outcome unresolved" {
						if hold.TaskID != wantTask {
							t.Fatalf("%s %s hold task=%q, want exact %q", kind, caseState, hold.TaskID, wantTask)
						}
						found++
					}
				}
				if (found == 1) != wantHold {
					t.Fatalf("%s %s hold count=%d, want exactly one=%v: %+v", kind, caseState, found, wantHold, out.Holds)
				}
				after, err := os.ReadFile(f.a.path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("native operation retention observation mutated authority", err)
				}
			})
		}
	}
}

func TestObserveStorageOperationHoldProtectsGeneratedArtifact(t *testing.T) {
	for _, kind := range []string{"integration", "external"} {
		t.Run(kind, func(t *testing.T) {
			w, err := workspace.Init(t.TempDir(), "retention")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.CreateProject(w, "a-root", "Core", "core", "retention fixture", ""); err != nil {
				t.Fatal(err)
			}
			task, err := store.CreateTask(w, "a-root", "core", "completed fixture", store.TaskOpts{Accept: []string{"landed"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.MoveTask(w, task, model.StatusDone); err != nil {
				t.Fatal(err)
			}
			runID := "01M14RETENTION000000000001"
			runDir := w.RunDir(runID)
			if err = os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err = procmon.WriteRecord(filepath.Join(runDir, "proc.txt"), procmon.Record{RunID: runID, Task: task.ID, Child: "fixture-worker", Outcome: "completed"}); err != nil {
				t.Fatal(err)
			}
			artifact := filepath.Join(runDir, "capture.tmp")
			if err = os.WriteFile(artifact, []byte("generated"), 0o600); err != nil {
				t.Fatal(err)
			}
			artifactBefore, err := os.ReadFile(artifact)
			if err != nil {
				t.Fatal(err)
			}

			f := nativeStorageLatestFixture(t)
			baselineSnapshot, err := f.a.ObserveStorage(f.owner)
			if err != nil || len(baselineSnapshot.Holds) != 0 {
				t.Fatalf("fixture has pre-existing holds that could mask the new hold: %+v %v", baselineSnapshot.Holds, err)
			}
			baseline, err := store.PlanNativeRetention(w, "core", store.NativeRetentionAuthority{Complete: true, Digest: baselineSnapshot.Digest})
			if err != nil {
				t.Fatal(err)
			}
			baselineRemovable := false
			for _, item := range baseline.Items {
				if item.Path == artifact {
					baselineRemovable = item.Removable
				}
			}
			if !baselineRemovable {
				t.Fatal("retention fixture generated artifact was not otherwise removable")
			}

			if err = f.a.transaction(false, true, func(s *state) error {
				s.Tasks[task.ID] = TaskBinding{TaskID: task.ID, ProjectID: "project", Repository: w.Root}
				if kind == "integration" {
					input := NativeIntegrationInput{OperationKey: "retention-pending", PolicyRevision: "retention-policy", Candidate: NativeAcceptanceInput{OperationKey: "retention-pending", ProjectID: "project", TaskID: task.ID, Repository: w.Root}}
					record := NativeIntegrationRecord{OperationKey: input.OperationKey, InputDigest: identity("native-integration-input", "", input), State: "pending", Input: input, Policy: NativeIntegrationPolicy{TaskID: task.ID, ProjectID: "project", Revision: input.PolicyRevision, Repository: w.Root}}
					s.NativeIntegrationOperations[input.OperationKey] = record
				} else {
					input := NativeExternalInput{OperationKey: "retention-pending", PolicyRevision: "retention-policy", Action: "push", Candidate: NativeAcceptanceInput{OperationKey: "retention-pending", ProjectID: "project", TaskID: task.ID, Repository: w.Root, Commit: "commit", Tree: "tree"}}
					policy := NativeExternalPolicy{ProjectID: "project", TaskID: task.ID, Revision: input.PolicyRevision, Repository: w.Root, GitHubRepository: "owner/repo", SourceBranch: "branch", TargetBranch: "main", Commit: input.Candidate.Commit, Tree: input.Candidate.Tree, Generation: 1, SliceGeneration: 1}
					before := NativeExternalObservation{Generation: 1, SliceGeneration: 1, Branch: "branch", Commit: "commit", Tree: "tree", Trunk: "base", RemoteHead: "base", Clean: true, APIComplete: true, ObservedAt: time.Now().UTC()}
					record := NativeExternalRecord{OperationKey: input.OperationKey, InputDigest: identity("native-external-input", "", input), State: "pending", Input: input, Policy: policy, Before: before}
					s.NativeExternalOperations[input.OperationKey] = sealExternal(record)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			authorityBefore, err := os.ReadFile(f.a.path)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := f.a.ObserveStorage(f.owner)
			if err != nil {
				t.Fatal(err)
			}
			authorityAfter, err := os.ReadFile(f.a.path)
			if err != nil || !bytes.Equal(authorityBefore, authorityAfter) {
				t.Fatal("composed retention observation mutated authority", err)
			}
			expectedReason := "native " + kind + " outcome unresolved"
			foundHold := false
			for _, hold := range observed.Holds {
				if hold.Reason == expectedReason && hold.TaskID == task.ID {
					foundHold = true
				}
				if hold.Reason == expectedReason && hold.TaskID == "" {
					t.Fatal("valid task binding unexpectedly became global")
				}
			}
			if !foundHold {
				t.Fatalf("actual authority observation omitted task-scoped hold %q: %+v", expectedReason, observed.Holds)
			}
			retention := store.NativeRetentionAuthority{Complete: true, Digest: observed.Digest}
			for _, hold := range observed.Holds {
				retention.Holds = append(retention.Holds, store.NativeRetentionHold{TaskID: hold.TaskID, Reason: hold.Reason})
			}
			plan, err := store.PlanNativeRetention(w, "core", retention)
			if err != nil {
				t.Fatal(err)
			}
			retained := false
			for _, item := range plan.Items {
				if item.Path == artifact {
					retained = !item.Removable && strings.Contains(strings.Join(item.Reasons, "\n"), expectedReason)
				}
			}
			if !retained {
				t.Fatal("generated artifact was not preserved by the actual authority hold")
			}
			artifactAfter, err := os.ReadFile(artifact)
			if err != nil || !bytes.Equal(artifactBefore, artifactAfter) {
				t.Fatal("dry-run retention changed generated artifact", err)
			}
		})
	}
}

func TestNativeStoragePolicyClosedOutcomeRequiresExactProducerBinding(t *testing.T) {
	for _, change := range []string{"exact", "unknown", "wrong-outcome", "wrong-decision", "wrong-operation", "missing-acceptance", "wrong-acceptance-key", "wrong-acceptance-input-digest"} {
		t.Run(change, func(t *testing.T) {
			f := controllerSetup(t)
			if e := f.a.transaction(false, true, func(s *state) error {
				input := NativeAcceptanceInput{OperationKey: "accept-exact", TaskID: "task-a", ProjectID: "project"}
				digest := strings.Repeat("e", 64)
				s.NativePolicyDecisions["decision-exact"] = NativePolicyDecision{ID: "decision-exact", Input: input}
				s.NativePolicyConsumptions["decision-exact"] = NativePolicyConsumption{DecisionID: "decision-exact", OperationKey: "accept-exact", CanonicalOutcomeDigest: digest}
				out := NativePolicyOutcome{OperationKey: "accept-exact", DecisionID: "decision-exact", State: "closed", ExpectedOutcomeDigest: digest, ObservedOutcomeDigest: digest}
				switch change {
				case "unknown":
					out.State = "unknown"
				case "wrong-outcome":
					out.ObservedOutcomeDigest = strings.Repeat("f", 64)
				case "wrong-decision":
					out.DecisionID = "other"
				case "wrong-operation":
					out.OperationKey = "other"
				}
				s.NativePolicyOutcomes["accept-exact"] = out
				if change != "missing-acceptance" {
					candidate := input
					candidate.OperationKey = "" // Real acceptance stores the unkeyed candidate (core639).
					acceptance := NativeAcceptanceRecord{OperationKey: "accept-exact", InputDigest: identity("native-acceptance-operation", "", input), State: "closed", CanonicalOutcomeDigest: digest, Candidate: NativeAcceptanceCandidate{Input: candidate}}
					if change == "wrong-acceptance-key" {
						acceptance.OperationKey = "other"
					}
					if change == "wrong-acceptance-input-digest" {
						acceptance.InputDigest = "wrong"
					}
					s.NativeAcceptanceOperations["accept-exact"] = acceptance
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			out, e := f.a.ObserveStorage(f.owner)
			if e != nil {
				t.Fatal(e)
			}
			held := false
			for _, h := range out.Holds {
				if h.Reason == "native policy consumption outcome unresolved" {
					held = true
				}
			}
			if held != (change != "exact") {
				t.Fatalf("policy retention resolution disagrees with exact producer outcome: %s held=%v", change, held)
			}
		})
	}
}

func TestNativeAcceptanceRecordMatchesExactOperationWithoutMutatingCandidate(t *testing.T) {
	for _, change := range []string{"exact-unkeyed", "exact-keyed", "record-key", "input-key", "candidate-key", "input-digest", "missing-digest", "candidate-revision", "candidate-fence", "candidate-policy"} {
		t.Run(change, func(t *testing.T) {
			input := NativeAcceptanceInput{SchemaVersion: 1, WorkspaceID: "workspace", InstallationID: "installation", OperationKey: "accept-exact", ProjectID: "project", TaskID: "task", ReviewID: "review", AttemptID: "attempt", LeaseID: "lease", RevisionID: "revision", ExpectedFence: 7, PolicyRevision: "policy"}
			candidate := input
			candidate.OperationKey = ""
			record := NativeAcceptanceRecord{OperationKey: input.OperationKey, InputDigest: identity("native-acceptance-operation", "", input), Candidate: NativeAcceptanceCandidate{Input: candidate}}
			switch change {
			case "exact-keyed":
				record.Candidate.Input.OperationKey = input.OperationKey
			case "record-key":
				record.OperationKey = "other"
			case "input-key":
				input.OperationKey = "other"
			case "candidate-key":
				record.Candidate.Input.OperationKey = "other"
			case "input-digest":
				record.InputDigest = identity("native-acceptance-operation", "", NativeAcceptanceInput{OperationKey: input.OperationKey})
			case "missing-digest":
				record.InputDigest = ""
			case "candidate-revision":
				record.Candidate.Input.RevisionID = "other"
			case "candidate-fence":
				record.Candidate.Input.ExpectedFence++
			case "candidate-policy":
				record.Candidate.Input.PolicyRevision = "other"
			}
			before := record
			want := change == "exact-unkeyed" || change == "exact-keyed"
			if got := acceptanceRecordMatchesInput(record, input); got != want {
				t.Fatalf("exact acceptance record binding %s = %v, want %v", change, got, want)
			}
			if record.Candidate.Input != before.Candidate.Input || record.OperationKey != before.OperationKey || record.InputDigest != before.InputDigest {
				t.Fatal("receipt comparison rewrote stored candidate or operation binding")
			}
		})
	}
}

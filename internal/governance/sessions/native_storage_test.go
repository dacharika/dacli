package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
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

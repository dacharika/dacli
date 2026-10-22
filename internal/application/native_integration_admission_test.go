package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// These tests exercise the public owner-selected producer with a real Git target
// and the actual session authority fixture. Refusals must not change the target.
func TestNativeIntegrationOwnerAdmissionRefusals(t *testing.T) {
	for _, kind := range []string{
		"worker-owner-selection", "wrong-policy", "wrong-project", "wrong-task",
		"dirty-target", "dirty-reviewed-checkout", "released-lease",
		"revoked-independent-review", "changed-pinned-trunk",
	} {
		t.Run(kind, func(t *testing.T) {
			f, owner, input, expected := nativeIntegrationProducerFixture(t)
			target := integrationTarget(owner.policy, input.Candidate)
			initial, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
			if err != nil || initial.Commit != expected || !initial.Clean {
				t.Fatalf("positive-control target was not at selected clean pin: %+v %v", initial, err)
			}
			if _, err := owner.InspectPolicy(); err != nil {
				t.Fatalf("positive-control selected integration policy unavailable: %v", err)
			}
			attempt := input
			var workerOwnerConfig string
			switch kind {
			case "worker-owner-selection":
				// A worker credential cannot open the owner-only consumer profile.
				workerConfig := f.access.owner.config
				workerCredential := filepath.Join(t.TempDir(), "worker.token")
				if err := os.WriteFile(workerCredential, []byte(f.workerToken), 0600); err != nil {
					t.Fatal(err)
				}
				workerConfig.OwnerCredentialFile = workerCredential
				workerOwnerConfig = filepath.Join(t.TempDir(), "worker-owner.json")
				acceptanceJSON(t, workerOwnerConfig, workerConfig)
			case "wrong-policy":
				attempt.PolicyRevision = "different-policy"
			case "wrong-project":
				attempt.Candidate.ProjectID = "foreign-project"
			case "wrong-task":
				attempt.Candidate.TaskID = "foreign-task"
			case "dirty-target":
				if err := os.WriteFile(filepath.Join(owner.policy.TargetCheckout, "src"), []byte("dirty target"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dirty-reviewed-checkout":
				// This is the worker's reviewed checkout, not the separate selected
				// integration target. Stale review/currentness must refuse without
				// changing either tree.
				if err := os.WriteFile(filepath.Join(f.w.Root, "src"), []byte("worker-only dirt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "released-lease":
				if _, err := f.a.ReleaseLease(f.workerToken, f.worker.ID, sessions.ReleaseLeaseRequest{OperationKey: "integration-release", AttemptID: f.at.ID, LeaseID: f.lease.ID, ExpectedFence: f.lease.Fence}); err != nil {
					t.Fatal(err)
				}
			case "revoked-independent-review":
				if err := f.a.RevokeReview(f.owner, f.grant.ID); err != nil {
					t.Fatal(err)
				}
			case "changed-pinned-trunk":
				// Advance the exact configured target ref to a real new commit,
				// while leaving the target checkout clean and on that ref.
				nativeGit(t, owner.policy.TargetCheckout, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "changed pinned trunk")
			}
			before, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
			if err != nil {
				t.Fatalf("could not snapshot target immediately before refusal: %v", err)
			}
			if kind == "dirty-target" && before.Clean {
				t.Fatalf("dirty-target case did not establish a dirty target: %+v", before)
			}
			if kind == "dirty-reviewed-checkout" && !before.Clean {
				t.Fatalf("dirty-worker case must keep selected target clean: %+v", before)
			}
			if kind == "changed-pinned-trunk" && (!before.Clean || before.Commit == expected) {
				t.Fatalf("changed-trunk case did not establish clean target advanced from pin: before=%+v expected=%s", before, expected)
			}
			targetPath := filepath.Join(owner.policy.TargetCheckout, "src")
			targetBytesBefore, err := os.ReadFile(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			candidateBefore := snapshotNativeIntegrationCheckout(t, f.w.Root)
			var refused error
			if kind == "worker-owner-selection" {
				_, refused = OpenNativeIntegrationOwner(workerOwnerConfig, f.access.policyPath, owner.policyPath)
			} else {
				_, refused = owner.Apply(context.Background(), attempt)
			}
			if !errors.Is(refused, sessions.ErrDenied) {
				t.Fatalf("invalid admission %q must fail closed as a policy denial: %v", kind, refused)
			}
			after, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
			if err != nil || !sameIntegrationGitState(before, after) {
				t.Fatalf("refusal %q changed actual target: before=%+v after=%+v err=%v", kind, before, after, err)
			}
			targetBytesAfter, err := os.ReadFile(targetPath)
			if err != nil || !reflect.DeepEqual(targetBytesBefore, targetBytesAfter) {
				t.Fatalf("refusal %q changed target worktree bytes: before=%q after=%q err=%v", kind, targetBytesBefore, targetBytesAfter, err)
			}
			candidateAfter := snapshotNativeIntegrationCheckout(t, f.w.Root)
			if !sameNativeIntegrationCheckout(candidateBefore, candidateAfter) {
				t.Fatalf("refusal %q changed reviewed candidate checkout: before=%+v after=%+v", kind, candidateBefore, candidateAfter)
			}
			if err = assertNoNativeIntegrationOperation(t, owner, f.access.owner.config.Authority, input.OperationKey); err != nil {
				t.Fatalf("refusal %q manufactured a durable operation: %v", kind, err)
			}
		})
	}
}

func TestNativeIntegrationOwnerConsumptionBindsInputAndOperationKey(t *testing.T) {
	f, owner, input, _ := nativeIntegrationProducerFixture(t)
	result, err := owner.Apply(context.Background(), input)
	if err != nil || result.Record.State != "integrated" || result.Record.ReceiptDigest == "" || result.Duplicate {
		t.Fatalf("positive control did not perform actual integration: %+v %v", result, err)
	}
	initial := result.Record
	target := integrationTarget(owner.policy, input.Candidate)
	before, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
	if err != nil || before.Commit != input.Candidate.Commit || before.Tree != input.Candidate.Tree || !before.Clean {
		t.Fatalf("positive control target not at reviewed outcome: %+v %v", before, err)
	}
	targetPath := filepath.Join(owner.policy.TargetCheckout, "src")
	targetBytesBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateBefore := snapshotNativeIntegrationCheckout(t, f.w.Root)
	changed := input
	changed.Candidate.Tree = "0000000000000000000000000000000000000000"
	if _, err = owner.Apply(context.Background(), changed); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("same-key altered input was not refused as a mismatch: %v", err)
	}
	otherKey := input
	otherKey.OperationKey = "integration-competing-key"
	if _, err = owner.Apply(context.Background(), otherKey); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("second key must be refused as a policy denial: %v", err)
	}
	if err = assertNoNativeIntegrationOperation(t, owner, f.access.owner.config.Authority, otherKey.OperationKey); err != nil {
		t.Fatalf("second key manufactured a durable operation: %v", err)
	}
	record, err := owner.Inspect(input.OperationKey)
	if err != nil || !reflect.DeepEqual(record, initial) {
		t.Fatalf("refusal changed original durable receipt: before=%+v after=%+v err=%v", initial, record, err)
	}
	after, err := store.ObserveNativeIntegrationTarget(context.Background(), target)
	if err != nil || !sameIntegrationGitState(before, after) {
		t.Fatalf("refusal changed real target after finalized operation: before=%+v after=%+v err=%v", before, after, err)
	}
	targetBytesAfter, err := os.ReadFile(targetPath)
	if err != nil || !reflect.DeepEqual(targetBytesBefore, targetBytesAfter) {
		t.Fatalf("refusal changed target worktree bytes: before=%q after=%q err=%v", targetBytesBefore, targetBytesAfter, err)
	}
	candidateAfter := snapshotNativeIntegrationCheckout(t, f.w.Root)
	if !sameNativeIntegrationCheckout(candidateBefore, candidateAfter) {
		t.Fatalf("refusal changed reviewed candidate checkout: before=%+v after=%+v", candidateBefore, candidateAfter)
	}
	if !reflect.DeepEqual(targetBytesAfter, []byte("finished\nafter-policy\n")) {
		t.Fatalf("target worktree bytes differ from reviewed result: %q", targetBytesAfter)
	}
}

func TestNativeIntegrationOwnerRejectsRevokedIndependentRunner(t *testing.T) {
	f, owner, input, expected := nativeIntegrationProducerFixture(t)
	if err := f.a.RevokeVerification(f.owner, f.verificationGrantID); err != nil {
		t.Fatal(err)
	}
	before, err := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(owner.policy, input.Candidate))
	if err != nil || before.Commit != expected || !before.Clean {
		t.Fatalf("positive-control target differs before runner revocation refusal: %+v %v", before, err)
	}
	targetPath := filepath.Join(owner.policy.TargetCheckout, "src")
	targetBytesBefore, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	candidateBefore := snapshotNativeIntegrationCheckout(t, f.w.Root)
	if _, err = owner.Apply(context.Background(), input); !errors.Is(err, sessions.ErrDenied) {
		t.Fatalf("revoked independent verification grant must be refused as a policy denial: %v", err)
	}
	after, err := store.ObserveNativeIntegrationTarget(context.Background(), integrationTarget(owner.policy, input.Candidate))
	if err != nil || !sameIntegrationGitState(before, after) {
		t.Fatalf("revoked runner refusal changed target: before=%+v after=%+v err=%v", before, after, err)
	}
	targetBytesAfter, err := os.ReadFile(targetPath)
	if err != nil || !reflect.DeepEqual(targetBytesBefore, targetBytesAfter) {
		t.Fatalf("revoked runner refusal changed target bytes: before=%q after=%q err=%v", targetBytesBefore, targetBytesAfter, err)
	}
	candidateAfter := snapshotNativeIntegrationCheckout(t, f.w.Root)
	if !sameNativeIntegrationCheckout(candidateBefore, candidateAfter) {
		t.Fatalf("revoked runner refusal changed reviewed candidate checkout: before=%+v after=%+v", candidateBefore, candidateAfter)
	}
	if err = assertNoNativeIntegrationOperation(t, owner, f.access.owner.config.Authority, input.OperationKey); err != nil {
		t.Fatalf("revoked runner refusal manufactured a durable operation: %v", err)
	}
}

type nativeIntegrationCheckoutSnapshot struct {
	Commit, Tree, Status string
	Source               []byte
}

func snapshotNativeIntegrationCheckout(t *testing.T, root string) nativeIntegrationCheckoutSnapshot {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	return nativeIntegrationCheckoutSnapshot{
		Commit: nativeAcceptanceGitOutput(t, root, "rev-parse", "HEAD^{commit}"),
		Tree:   nativeAcceptanceGitOutput(t, root, "rev-parse", "HEAD^{tree}"),
		Status: nativeAcceptanceGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all"),
		Source: source,
	}
}

func sameNativeIntegrationCheckout(a, b nativeIntegrationCheckoutSnapshot) bool {
	return a.Commit == b.Commit && a.Tree == b.Tree && a.Status == b.Status && reflect.DeepEqual(a.Source, b.Source)
}

func assertNoNativeIntegrationOperation(t *testing.T, owner *NativeIntegrationOwner, authorityPath, key string) error {
	t.Helper()
	if _, err := owner.Inspect(key); !errors.Is(err, sessions.ErrDenied) {
		return errors.New("public inspect did not refuse absent operation")
	}
	raw, err := os.ReadFile(authorityPath)
	if err != nil {
		return err
	}
	var persisted struct {
		NativeIntegrationOperations map[string]sessions.NativeIntegrationRecord
	}
	if err = json.Unmarshal(raw, &persisted); err != nil {
		return err
	}
	if _, exists := persisted.NativeIntegrationOperations[key]; exists {
		return errors.New("authority persisted an operation record for refused input")
	}
	return nil
}

func sameIntegrationGitState(a, b store.NativeIntegrationGitObservation) bool {
	return a.Commit == b.Commit && a.Tree == b.Tree && a.Clean == b.Clean
}

package nativegovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/application"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

type taskClaimsCapability struct{}

func (taskClaimsCapability) AdmitSwarmCapability(string, string, string, string) error { return nil }

func TestOwnerTaskClaimsApplyAppearsInNextNativePlanWithoutGrantMutation(t *testing.T) {
	f := setupVerification(t, "printf verified", false)
	if _, err := f.authority.ReleaseLease(f.workerToken, f.worker.SessionID, sessions.ReleaseLeaseRequest{OperationKey: "release-before-claims", AttemptID: f.request.AttemptID, LeaseID: f.request.LeaseID, ExpectedFence: f.request.ExpectedFence}); err != nil {
		t.Fatal(err)
	}
	f.task.Doc.Front.SetList("claims", []string{"src"})
	if err := store.SaveTask(f.task); err != nil {
		t.Fatal(err)
	}
	verificationGit(t, f.w.Root, "add", ".dacli")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "declare task claims")
	if _, err := verificationRun(t, f.w, swarmUpgrade, "--config", f.config); err != nil {
		t.Fatal(err)
	}

	private := t.TempDir()
	capabilityPath := filepath.Join(private, "capability.json")
	capability := application.ManualSwarmCapability{SchemaVersion: 1, WorkspaceID: f.w.ID, ProjectID: "p", Harness: "codex", Source: "owner-manual-native-handoff", ObservedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
	verificationJSON(t, capabilityPath, capability)
	capabilityData, err := os.ReadFile(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(capabilityData)
	policy := sessions.SwarmPolicy{ProjectID: "p", Revision: "v1", Harness: "codex", CapabilityDigest: "sha256:" + hex.EncodeToString(digest[:]), Width: 1, WIP: 1, TTL: time.Hour, Roles: []sessions.SwarmRole{{ID: "implement", Kind: "implementation", Harness: "codex", Capacity: 1}, {ID: "review", Kind: "review", Harness: "codex", Capacity: 1}}, Placements: map[string]sessions.SwarmPlacement{f.task.ID: {Checkout: f.w.Root, ImplementKind: "implementation", ReviewKind: "review"}}}
	policyPath := filepath.Join(private, "policy.json")
	verificationJSON(t, policyPath, policy)
	if out, err := verificationRun(t, f.w, swarmConfigure, "--config", f.config, "--policy-file", policyPath); err != nil {
		t.Fatalf("configure: %v %s", err, out)
	}

	admission, err := application.NewSwarmAdmission(f.w, taskClaimsCapability{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := sessions.OpenSwarm(filepath.Join(filepath.Dir(f.config), "authority.json"), sessions.Binding{Workspace: f.w.ID, Installation: "fixture"}, admission)
	if err != nil {
		t.Fatal(err)
	}
	controller, token, err := authority.Enroll(f.owner, sessions.Scope{Actions: []string{"swarm.plan", "swarm.reserve", "swarm.dispatch"}, Resources: []string{"workspace:" + f.w.ID, "project:p", "task:" + f.task.ID, "path:src", "path:tests"}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	before, err := authority.PlanSwarm(token, controller.ID, "p")
	if err != nil || len(before.Verdicts) != 1 || !before.Verdicts[0].Selected {
		t.Fatalf("initial plan=%+v err=%v", before, err)
	}

	claimsFile := filepath.Join(private, "claims.json")
	verificationJSON(t, claimsFile, struct {
		Claims []string `json:"claims"`
	}{[]string{"src", "tests"}})
	planJSON, err := verificationRun(t, f.w, taskClaimsPreview, "--config", f.config, "--task", f.task.ID, "--claims-file", claimsFile)
	if err != nil {
		t.Fatalf("preview: %v %s", err, planJSON)
	}
	planFile := filepath.Join(private, "task-claims-plan.json")
	if err := os.WriteFile(planFile, []byte(planJSON), 0600); err != nil {
		t.Fatal(err)
	}
	authorityBeforeApply, err := os.ReadFile(filepath.Join(filepath.Dir(f.config), "authority.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verificationRun(t, f.w, taskClaimsApply, "--config", f.config, "--plan", planFile); err != nil {
		t.Fatal(err)
	}
	authorityAfterApply, err := os.ReadFile(filepath.Join(filepath.Dir(f.config), "authority.json"))
	if err != nil || !bytes.Equal(authorityBeforeApply, authorityAfterApply) {
		t.Fatal("task claims apply changed native grants or authority", err)
	}
	verificationGit(t, f.w.Root, "add", ".dacli")
	verificationGit(t, f.w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "apply task claims")

	after, err := authority.PlanSwarm(token, controller.ID, "p")
	if err != nil || len(after.Verdicts) != 1 || !after.Verdicts[0].Selected {
		t.Fatalf("next plan=%+v err=%v", after, err)
	}
	want := []sessions.Resource{{Kind: "path", Path: "src"}, {Kind: "path", Path: "tests"}, {Kind: "task", Path: f.task.ID}}
	if !jsonEqual(after.Verdicts[0].Resources, want) {
		t.Fatalf("next plan resources=%+v, want %+v", after.Verdicts[0].Resources, want)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

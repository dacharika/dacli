package application

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/checkout"
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/team"
)

func TestNativePreflightCanonicalRoleEstimateAndPrivateObservation(t *testing.T) {
	w, _ := readFixture(t)
	var e error
	w, e = nativeconfig.ResolveWorkspace(w.Root, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.CreateProject(w, "owner", "p", "p", "goal", ""); e != nil {
		t.Fatal(e)
	}
	task, e := store.CreateTask(w, "owner", "p", "Implement bounded task", store.TaskOpts{Estimate: "5,8,11", Claims: []string{"src"}, Accept: []string{"verified output"}})
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []team.Role{{Name: "implement", Kind: "implementer", Runtime: "codex", Model: "model", Grant: "rw", MaxPoints: 3, Scope: []string{"src", "src/**"}}, {Name: "review", Kind: "reviewer", Runtime: "codex", Model: "model", Grant: "ro"}} {
		if e = store.CreateRole(w, "owner", r); e != nil {
			t.Fatal(e)
		}
	}
	private := t.TempDir()
	token := strings.Repeat("e", 64)
	credential := filepath.Join(private, "worker.token")
	authority := filepath.Join(private, "authority.json")
	if e = os.WriteFile(credential, []byte(token), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(authority, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	write := func(path string, v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	worker := filepath.Join(private, "worker.json")
	write(worker, nativeconfig.Connection{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authority, InstallationID: "installation", SessionID: "worker", CredentialFile: credential})
	contract := filepath.Join(private, "contract.json")
	c := NativePhaseContract{SchemaVersion: 1, WorkspaceID: w.ID, ProjectID: "p", TaskID: task.ID, Harness: "codex", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute), Models: []string{"model"}, ReviewOutputContract: "native-review-v1", TokenControl: "advisory", BudgetControl: "unavailable"}
	write(contract, c)
	spec := sessions.PreflightTask{ImplementationRole: "implement", ReviewRole: "review", RecoveryRole: "implement", WorkerConnection: worker, ContractProfile: contract, FallbackRoles: []string{}}
	admission, e := NewLeaseAdmission(w)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	capacity := team.TaskCapacity(obs.Implementation, obs.Required, obs.Sized)
	if obs.Required != 8 || !obs.Sized || capacity.Fits || capacity.Limit != 3 || capacity.Delta != -5 || obs.Worker.CredentialHash == "" || len(obs.TaskDigest) != 64 || len(obs.RosterDigest) != 64 || len(obs.ProfileDigest) != 64 {
		t.Fatalf("canonical shared role/estimate facts wrong: %+v", capacity)
	}
	serialized, e := json.Marshal(obs)
	if e != nil || strings.Contains(string(serialized), token) || strings.Contains(string(serialized), obs.Worker.CredentialHash) || strings.Contains(string(serialized), "CredentialHash") {
		t.Fatal("private credential material escaped safe observations", e)
	}
	for _, p := range obs.Phases {
		if p.Phase == "fallback" && p.Verdict == "policy_refused" {
			t.Fatalf("empty owner list differed from empty canonical fallback: %+v", p)
		}
	}
	before := obs.TaskDigest
	raw, e := os.ReadFile(task.Path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(task.Path, append(raw, []byte("\nchanged canonical task\n")...), 0600); e != nil {
		t.Fatal(e)
	}
	changed, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{})
	if e != nil || changed.TaskDigest == before {
		t.Fatal("canonical task change not freshly observed", e)
	}
	spec.RequireReview = true
	spec.ReviewerProfile = filepath.Join(private, "missing-reviewer.json")
	missing, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range missing.Phases {
		if p.Phase == "review" && p.Required && p.Verdict == "unsupported" {
			found = true
		}
	}
	if !found {
		t.Fatal("required unavailable reviewer omitted before implementation")
	}
	spec.RequireIntegration = true
	spec.OwnerAuthorizedIntegration = true
	c.IntegrationRoute = "owner-manual-local"
	c.ExternalStatus = "ready"
	c.IntegrationBase = strings.Repeat("z", 40)
	c.IntegrationPolicyDigest = strings.Repeat("z", 64)
	c.IntegrationCheckout = w.Root
	write(contract, c)
	malformed, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{})
	if e != nil {
		t.Fatal(e)
	}
	found = false
	for _, p := range malformed.Phases {
		if p.Phase == "integration" && p.Verdict == "unsupported" {
			found = true
		}
	}
	if !found {
		t.Fatal("malformed unobserved manual integration base authorized")
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = store.VerificationEnvironment()
		if raw, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, raw)
		}
	}
	git(w.Root, "init", "-b", "main")
	if e = os.MkdirAll(filepath.Join(w.Root, "src"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(w.Root, "src", "tracked"), []byte("base"), 0600); e != nil {
		t.Fatal(e)
	}
	git(w.Root, "add", ".")
	git(w.Root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
	observed, e := checkout.Observe(w.Root)
	if e != nil {
		t.Fatal(e)
	}
	runner := filepath.Join(private, "runner.json")
	write(runner, NativeRunnerProfile{SchemaVersion: 1, Workspace: w.Root, WorkspaceID: w.ID, Authority: authority, InstallationID: "installation", TaskID: task.ID, Repository: observed.Repository, RunnerSessionID: "runner", CredentialFile: credential, Checks: []ConfiguredCheck{{ID: "compound", Command: "FOO=x go test ./...", Deadline: "1m", GrantID: "compound-grant"}, {ID: "builtin", Command: ":", Deadline: "1m", GrantID: "builtin-grant"}, {ID: "quoted", Command: "\"/usr/bin/printf\" verified", Deadline: "1m", GrantID: "quoted-grant"}}})
	spec.RequireReview = false
	spec.RequireVerification = true
	spec.RunnerProfile = runner
	c.IntegrationBase = observed.Commit
	c.IntegrationPolicyDigest = strings.Repeat("a", 64)
	write(contract, c)
	valid, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	validIntegration, validShell := false, false
	for _, p := range valid.Phases {
		if p.Phase == "integration" && p.Verdict == "ready" {
			validIntegration = true
		}
		if p.Phase == "verification-execution" && p.Verdict == "ready" && p.Assurance == "owner-manual" {
			validShell = true
		}
		if p.Phase == "verification" && p.Verdict == "unsupported" {
			t.Fatalf("legitimate sh-c compound/builtin/quoted path rejected: %+v", p)
		}
	}
	if !validIntegration || !validShell || len(valid.Checks) != 3 {
		t.Fatalf("valid clean same-repository manual route or shell contract missing: integration%v shell%v checks%d", validIntegration, validShell, len(valid.Checks))
	}
	wrong := filepath.Join(t.TempDir(), "clone")
	git(w.Root, "clone", "--quiet", w.Root, wrong)
	c.IntegrationCheckout = wrong
	write(contract, c)
	other, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range other.Phases {
		if p.Phase == "integration" && p.Verdict == "ready" {
			t.Fatal("same-commit wrong-repository landing falsely ready")
		}
	}
	c.IntegrationCheckout = w.Root
	write(contract, c)
	if e = os.WriteFile(filepath.Join(w.Root, "src", "tracked"), []byte("dirty"), 0600); e != nil {
		t.Fatal(e)
	}
	dirty, e := admission.ObservePreflight(w.ID, "p", task.ID, spec, sessions.SwarmPlacement{Checkout: w.Root})
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range dirty.Phases {
		if p.Phase == "integration" && p.Verdict == "ready" {
			t.Fatal("dirty manual landing falsely ready")
		}
	}

}

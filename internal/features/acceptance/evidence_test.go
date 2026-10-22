package acceptance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/agentid"
	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func evidenceEnv(t *testing.T) (*clikit.Ctx, *workspace.Workspace, *agentid.Identity) {
	t.Helper()
	w, err := workspace.Init(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		if out, err := gitx.Run(w.Root, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(w.Root+"/.gitignore", []byte(".dacli/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "seed"}} {
		if out, err := gitx.Run(w.Root, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, err := store.CreateProject(w, agentid.RootID, "Core", "core", "", ""); err != nil {
		t.Fatal(err)
	}
	// Older fixtures isolate external-check policy, not remote artifact selection.
	// Their canonical artifact is the fixture repository's main, never ctx.Cwd.
	oldArtifact := observeAcceptanceArtifact
	observeAcceptanceArtifact = func(w *workspace.Workspace, _ *store.Task, _ string, _ bool) (acceptanceArtifact, error) {
		return localAcceptanceArtifact(w, "refs/heads/main")
	}
	t.Cleanup(func() { observeAcceptanceArtifact = oldArtifact })
	ctx := &clikit.Ctx{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Cwd: w.Root}
	return ctx, w, &agentid.Identity{ID: agentid.RootID, Grant: model.GrantRW, Role: "root"}
}

// taskLog returns the task's "## Log" section text.
func taskLog(t *testing.T, tk *store.Task) string {
	t.Helper()
	s, ok := tk.Doc.Section("Log")
	if !ok {
		return ""
	}
	return s.Content
}

func mkTask(t *testing.T, w *workspace.Workspace, title string) *store.Task {
	t.Helper()
	tk, err := store.CreateTask(w, agentid.RootID, "core", title, store.TaskOpts{Accept: []string{"it works"}})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// A close must record WHAT certified it. Before this, an unverified close was
// indistinguishable from a verified one in the record — which is what made
// every `done` label an unverified assertion (dacli 184).
func TestCloseRecordsVerificationEvidence(t *testing.T) {
	ctx, w, root := evidenceEnv(t)

	unverified := mkTask(t, w, "closed with no check")
	if err := acceptOne(ctx, w, root, unverified, "", false, false, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindTask(w, unverified.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if body := taskLog(t, got); !strings.Contains(body, "WITHOUT verification") {
		t.Errorf("an unverified close must say so in the task log; got:\n%s", body)
	}

	verified := mkTask(t, w, "closed with a real check")
	if err := acceptOne(ctx, w, root, verified, "true", false, false, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	got2, err := store.FindTask(w, verified.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if body := taskLog(t, got2); !strings.Contains(body, "verified by") {
		t.Errorf("a verified close must record the command; got:\n%s", body)
	}
	records := store.VerificationEvidenceRecords(got2)
	if len(records) != 1 {
		t.Fatalf("structured verification records = %#v, want one", records)
	}
	ev := records[0]
	if ev.Command != "true" || ev.ExitCode != 0 || ev.DurationMS < 0 || ev.ArtifactHash == "" || ev.Verifier != root.ID {
		t.Fatalf("incomplete structured verification evidence: %#v", ev)
	}
}

func TestAcceptanceConsumesConfiguredExactHeadExternalEvidence(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	project.Doc.Front.SetList("required_checks", []string{"ci"})
	project.Doc.Front.SetList("required_artifacts", []string{"binary"})
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	old := store.ObserveGitHubExternalVerification
	oldPolicy := store.ObserveGitHubRequiredCheckPolicy
	t.Cleanup(func() {
		store.ObserveGitHubExternalVerification = old
		store.ObserveGitHubRequiredCheckPolicy = oldPolicy
	})
	store.ObserveGitHubRequiredCheckPolicy = func(_, repo, branch string, _ []string, at time.Time) (store.GitHubRequiredCheckPolicy, error) {
		return store.GitHubRequiredCheckPolicy{Schema: store.GitHubRequiredCheckPolicySchema, Repository: repo, Branch: branch, ObservedAt: at, State: "observed", Requirements: []store.RequiredCheckRequirement{{Name: "ci", Sources: []store.RequiredCheckSource{{Kind: "configured"}, {Kind: "ruleset", RulesetID: 8}}}}}, nil
	}
	stale := true
	store.ObserveGitHubExternalVerification = func(_, _, commit string, at time.Time) ([]store.ExternalVerificationEvidence, error) {
		head := commit
		if stale {
			head = "stale-head"
		}
		return []store.ExternalVerificationEvidence{{Provider: "github-actions", WorkflowRunID: "22", HeadSHA: head, Name: "ci", ObservedAt: at, State: "observed", Conclusion: "success", Artifacts: []store.ExternalArtifactEvidence{{ID: "31", Name: "binary", Digest: "sha256:abc"}}}}, nil
	}
	task := mkTask(t, w, "external evidence")
	if err := acceptOne(ctx, w, root, task, "true", false, false, false, false, false, ""); clikit.ExitCode(err) != 3 {
		t.Fatalf("stale external head = exit %d err=%v, want refusal", clikit.ExitCode(err), err)
	}
	got, _ := store.FindTask(w, task.ID)
	if got.Status == model.StatusDone {
		t.Fatal("stale external evidence closed task")
	}
	stale = false
	ctx.Stderr.(*bytes.Buffer).Reset()
	if err := acceptOne(ctx, w, root, got, "true", false, false, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"required check ci", "configured", "ruleset 8"} {
		if output := ctx.Stderr.(*bytes.Buffer).String(); !strings.Contains(output, want) {
			t.Fatalf("human policy output missing %q:\n%s", want, output)
		}
	}
	got, _ = store.FindTask(w, task.ID)
	records := store.VerificationEvidenceRecords(got)
	if len(records) != 1 || len(records[0].External) != 1 || records[0].External[0].Artifacts[0].Digest != "sha256:abc" {
		t.Fatalf("external evidence was not persisted with local evidence: %+v", records)
	}
}

func TestAcceptanceRulesetPolicyFailsClosedAndExposesProvenance(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	ctx.JSON = true
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	oldPolicy, oldEvidence := store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification
	t.Cleanup(func() {
		store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification = oldPolicy, oldEvidence
	})
	store.ObserveGitHubRequiredCheckPolicy = func(_, repo, branch string, _ []string, at time.Time) (store.GitHubRequiredCheckPolicy, error) {
		return store.GitHubRequiredCheckPolicy{Schema: store.GitHubRequiredCheckPolicySchema, Repository: repo, Branch: branch, ObservedAt: at, State: "observed", Requirements: []store.RequiredCheckRequirement{{Name: "ruleset-ci", Sources: []store.RequiredCheckSource{{Kind: "ruleset", RulesetID: 42, RulesetSourceType: "Organization", RulesetSource: "owner"}}}}}, nil
	}
	green := false
	store.ObserveGitHubExternalVerification = func(_, _, commit string, at time.Time) ([]store.ExternalVerificationEvidence, error) {
		conclusion := "failure"
		if green {
			conclusion = "success"
		}
		return []store.ExternalVerificationEvidence{{Provider: "github-check", CheckRunID: "1", HeadSHA: commit, Name: "ruleset-ci", ObservedAt: at, State: "observed", Conclusion: conclusion}}, nil
	}
	task := mkTask(t, w, "ruleset evidence")
	if err := acceptOneForTreePolicy(ctx, w, root, task, "true", false, false, false, false, false, false, false, "", "", ""); clikit.ExitCode(err) != 3 {
		t.Fatalf("red ruleset check exit=%d err=%v", clikit.ExitCode(err), err)
	}
	green = true
	ctx.Stdout.(*bytes.Buffer).Reset()
	if err := acceptOneForTreePolicy(ctx, w, root, task, "true", false, false, false, false, false, false, false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	output := ctx.Stdout.(*bytes.Buffer).String()
	for _, want := range []string{`"external_policy"`, `"name": "ruleset-ci"`, `"kind": "ruleset"`, `"ruleset_id": 42`} {
		if !strings.Contains(output, want) {
			t.Fatalf("JSON result missing %s:\n%s", want, output)
		}
	}
	got, _ := store.FindTask(w, task.ID)
	records := store.VerificationEvidenceRecords(got)
	if len(records) != 1 || records[0].ExternalPolicy == nil || records[0].ExternalPolicy.Requirements[0].Name != "ruleset-ci" {
		t.Fatalf("persisted policy = %+v", records)
	}
}

func TestAcceptanceUnobservablePolicyRequiresAuditedOverride(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	project, _ := store.LoadProject(w, "core")
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	oldPolicy, oldEvidence := store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification
	t.Cleanup(func() {
		store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification = oldPolicy, oldEvidence
	})
	store.ObserveGitHubRequiredCheckPolicy = func(_, repo, branch string, _ []string, at time.Time) (store.GitHubRequiredCheckPolicy, error) {
		return store.GitHubRequiredCheckPolicy{Schema: store.GitHubRequiredCheckPolicySchema, Repository: repo, Branch: branch, ObservedAt: at, State: "unobservable", Error: "HTTP 403"}, errors.New("HTTP 403")
	}
	store.ObserveGitHubExternalVerification = func(_, _, _ string, _ time.Time) ([]store.ExternalVerificationEvidence, error) { return nil, nil }
	task := mkTask(t, w, "unobservable policy")
	if err := acceptOneForTreePolicy(ctx, w, root, task, "true", false, false, false, false, false, false, false, "", "", ""); clikit.ExitCode(err) != 3 {
		t.Fatalf("unobservable policy exit=%d err=%v", clikit.ExitCode(err), err)
	}
	if err := acceptOneForTreePolicy(ctx, w, root, task, "true", false, false, false, false, true, false, false, "", "", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := store.FindTask(w, task.ID)
	if body := taskLog(t, got); !strings.Contains(body, "required-check policy override by a-root") || !strings.Contains(body, "HTTP 403") {
		t.Fatalf("override was not audited:\n%s", body)
	}
}

func TestAcceptAllEnforcesRulesetChecksPerTask(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	project, _ := store.LoadProject(w, "core")
	project.Doc.Front.Set("github_repo", "owner/repo")
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	oldPolicy, oldEvidence := store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification
	t.Cleanup(func() {
		store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification = oldPolicy, oldEvidence
	})
	store.ObserveGitHubRequiredCheckPolicy = func(_, repo, branch string, _ []string, at time.Time) (store.GitHubRequiredCheckPolicy, error) {
		return store.GitHubRequiredCheckPolicy{Schema: store.GitHubRequiredCheckPolicySchema, Repository: repo, Branch: branch, ObservedAt: at, State: "observed", Requirements: []store.RequiredCheckRequirement{{Name: "batch-ci", Sources: []store.RequiredCheckSource{{Kind: "ruleset", RulesetID: 77}}}}}, nil
	}
	store.ObserveGitHubExternalVerification = func(_, _, commit string, at time.Time) ([]store.ExternalVerificationEvidence, error) {
		return []store.ExternalVerificationEvidence{{Provider: "github-check", CheckRunID: "2", HeadSHA: commit, Name: "batch-ci", ObservedAt: at, State: "observed", Conclusion: "failure"}}, nil
	}
	task := mkTask(t, w, "batch ruleset evidence")
	if err := propose(ctx, w, root, task); err != nil {
		t.Fatal(err)
	}
	err := acceptAllForTreePolicy(ctx, w, root, "true", false, false, false, false, true, false, false, false, "", "", "")
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("batch ruleset check exit=%d err=%v", clikit.ExitCode(err), err)
	}
	got, _ := store.FindTask(w, task.ID)
	if got.Status == model.StatusDone {
		t.Fatal("batch acceptance closed task over red ruleset check")
	}
}

func TestAcceptRefusesCommandCriterionWithoutProvenance(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk, err := store.CreateTask(w, agentid.RootID, "core", "command checked close", store.TaskOpts{Accept: []string{"`go test ./...` exits zero"}})
	if err != nil {
		t.Fatal(err)
	}

	err = acceptOne(ctx, w, root, tk, "", false, false, false, false, false, "")
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("accept exit = %d, want refusal for missing command evidence: %v", clikit.ExitCode(err), err)
	}
	got, findErr := store.FindTask(w, tk.Slug)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if got.Status == model.StatusDone || got.Acceptance()[0].Done {
		t.Fatal("command criterion was accepted without provenance")
	}
}

// --require-verify makes an unverified close impossible rather than merely
// visible: the strict mode for generating repos whose record is the product.
func TestRequireVerifyRefusesUnverifiedClose(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk := mkTask(t, w, "must not close unverified")

	err := acceptOne(ctx, w, root, tk, "", true, false, false, false, false, "")
	if err == nil {
		t.Fatal("acceptOne with requireVerify and no command must refuse")
	}
	if clikit.ExitCode(err) != 3 {
		t.Errorf("exit code = %d, want 3 (refused by policy)", clikit.ExitCode(err))
	}
	got, err := store.FindTask(w, tk.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == model.StatusDone {
		t.Error("a refused close must leave the task open")
	}
}

// The implementer marking its own work complete is the failure the role split
// exists to prevent, and nothing enforced it: the claimant owned the task and
// accepted it (dacli 188).
func TestRequireIndependentBlocksSelfCertification(t *testing.T) {
	ctx, w, _ := evidenceEnv(t)
	tk := mkTask(t, w, "self certified")

	// The worker claims the task, then tries to certify its own work.
	worker := &agentid.Identity{ID: "a-worker", Grant: model.GrantRW, Role: "fixer"}
	store.AppendLog(tk, "claimed by "+worker.ID)
	tk.Doc.Front.Set("owner", worker.ID)
	if err := store.SaveTask(tk); err != nil {
		t.Fatal(err)
	}

	err := acceptOne(ctx, w, worker, tk, "true", false, true, false, false, false, "")
	if err == nil {
		t.Fatal("the claimant must not be able to certify its own task under --require-independent")
	}
	if clikit.ExitCode(err) != 3 {
		t.Errorf("exit code = %d, want 3 (refused by policy)", clikit.ExitCode(err))
	}

	// A DIFFERENT agent certifying the same task is fine.
	reviewer := &agentid.Identity{ID: "a-reviewer", Grant: model.GrantRW, Role: "reviewer"}
	if err := acceptOne(ctx, w, reviewer, tk, "true", false, true, false, false, false, ""); err != nil {
		t.Fatalf("an independent certifier must be allowed: %v", err)
	}
	got, err := store.FindTask(w, tk.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDone {
		t.Errorf("status = %s, want done after independent certification", got.Status)
	}
}

// A failing verification must never close the task.
func TestFailedVerificationLeavesTaskOpen(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk := mkTask(t, w, "verification fails")

	if err := acceptOne(ctx, w, root, tk, "exit 1", false, false, false, false, false, ""); err == nil {
		t.Fatal("a non-zero verify command must refuse the close")
	}
	got, err := store.FindTask(w, tk.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == model.StatusDone {
		t.Error("a task whose verification failed must stay open")
	}
}

func TestAcceptanceGradeVerificationRefusesDirtyTreeWithoutClosing(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk := mkTask(t, w, "dirty tree cannot certify")
	if err := os.WriteFile(w.Root+"/.gitignore", []byte(".dacli/\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := acceptOne(ctx, w, root, tk, "true", true, false, false, false, false, "")
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("dirty acceptance = exit %d, %v; want policy refusal naming dirty tree", clikit.ExitCode(err), err)
	}
	got, findErr := store.FindTask(w, tk.Slug)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if got.Status == model.StatusDone || len(store.VerificationEvidenceRecords(got)) != 0 {
		t.Fatalf("dirty verification closed or recorded success: status=%s evidence=%#v", got.Status, store.VerificationEvidenceRecords(got))
	}
}

func TestAcceptanceGradeVerificationRejectsMutationWithoutRecordingSuccess(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk := mkTask(t, w, "mutation cannot certify")

	err := acceptOne(ctx, w, root, tk, "printf mutation >> .gitignore", true, false, false, false, false, "")
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "changed during execution") {
		t.Fatalf("mutating acceptance = exit %d, %v; want policy refusal naming before/after change", clikit.ExitCode(err), err)
	}
	got, findErr := store.FindTask(w, tk.Slug)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if got.Status == model.StatusDone || len(store.VerificationEvidenceRecords(got)) != 0 {
		t.Fatalf("mutating verification closed or recorded success: status=%s evidence=%#v", got.Status, store.VerificationEvidenceRecords(got))
	}
}

func TestAcceptanceRefusesEvidenceForDifferentReviewedLandingTree(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	tk := mkTask(t, w, "stale tree cannot certify")
	commit, err := gitx.Run(w.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	err = acceptOneForTree(ctx, w, root, tk, "true", true, false, false, false, false, "", strings.TrimSpace(commit), "different-reviewed-tree")
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "rerun verification on the reviewed head") {
		t.Fatalf("stale final-tree acceptance = exit %d, %v; want actionable policy refusal", clikit.ExitCode(err), err)
	}
	got, findErr := store.FindTask(w, tk.Slug)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if got.Status == model.StatusDone || len(store.VerificationEvidenceRecords(got)) != 0 {
		t.Fatalf("stale final-tree evidence closed or persisted: status=%s evidence=%#v", got.Status, store.VerificationEvidenceRecords(got))
	}
}

// canonicalFixture uses the production selector and real immutable Git objects.
// Only the read-only GitHub transport/check observations are fixture responses.
type canonicalFixture struct {
	ctx           *clikit.Ctx
	w             *workspace.Workspace
	root          *agentid.Identity
	task          *store.Task
	old, reviewed acceptanceArtifact
	marker        string
	checks        int
	prHead        string
	onChecks      func()
}

func newCanonicalFixture(t *testing.T) *canonicalFixture {
	t.Helper()
	ctx, w, root := evidenceEnv(t)
	observeAcceptanceArtifact = canonicalAcceptanceArtifact
	f := &canonicalFixture{ctx: ctx, w: w, root: root, task: mkTask(t, w, "canonical reviewed artifact"), marker: w.Root + "/.dacli/verification-ran"}
	var err error
	f.old, err = localAcceptanceArtifact(w, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	branch := store.TaskBranch(f.task)
	git(t, w.Root, "checkout", "-qb", branch)
	if err := os.WriteFile(w.Root+"/deliverable", []byte("reviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, w.Root, "add", "deliverable")
	git(t, w.Root, "commit", "-qm", "reviewed")
	f.reviewed, err = localAcceptanceArtifact(w, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f.prHead = f.reviewed.Commit
	git(t, w.Root, "checkout", "-q", "main")
	git(t, w.Root, "merge", "--ff-only", branch)
	git(t, w.Root, "update-ref", "refs/remotes/origin/main", f.reviewed.Commit)
	project, err := store.LoadProject(w, "core")
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "fixture/repo")
	project.Doc.Front.SetList("required_checks", []string{"ci"})
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	oldGH, oldPolicy, oldExternal := runLandingGH, store.ObserveGitHubRequiredCheckPolicy, store.ObserveGitHubExternalVerification
	t.Cleanup(func() {
		runLandingGH = oldGH
		store.ObserveGitHubRequiredCheckPolicy = oldPolicy
		store.ObserveGitHubExternalVerification = oldExternal
	})
	runLandingGH = func(_ string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "api" {
			a, err := localAcceptanceArtifact(w, "refs/heads/main")
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(`{"sha":%q,"commit":{"tree":{"sha":%q}}}`, a.Commit, a.Tree), nil
		}
		headBranch := branch
		for i, a := range args {
			if a == "--head" && i+1 < len(args) {
				headBranch = args[i+1]
			}
		}
		pr := fmt.Sprintf(`{"number":7,"url":"https://github.com/fixture/repo/pull/7","state":"MERGED","baseRefName":"main","headRefName":%q,"headRefOid":%q}`, headBranch, f.prHead)
		if len(args) > 1 && args[1] == "view" {
			return pr, nil
		}
		return "[" + pr + "]", nil
	}
	store.ObserveGitHubRequiredCheckPolicy = func(_, repo, base string, _ []string, at time.Time) (store.GitHubRequiredCheckPolicy, error) {
		return store.GitHubRequiredCheckPolicy{Schema: store.GitHubRequiredCheckPolicySchema, Repository: repo, Branch: base, ObservedAt: at, State: "observed", Requirements: []store.RequiredCheckRequirement{{Name: "ci", Sources: []store.RequiredCheckSource{{Kind: "configured"}}}}}, nil
	}
	store.ObserveGitHubExternalVerification = func(_, _, head string, at time.Time) ([]store.ExternalVerificationEvidence, error) {
		f.checks++
		if f.onChecks != nil {
			f.onChecks()
		}
		return []store.ExternalVerificationEvidence{{Provider: "github-actions", WorkflowRunID: "22", HeadSHA: head, Name: "ci", ObservedAt: at, State: "observed", Conclusion: "success"}}, nil
	}
	return f
}

func (f *canonicalFixture) checkout(t *testing.T, a acceptanceArtifact) {
	t.Helper()
	path := f.w.WorktreesDir() + "/verification"
	if err := os.MkdirAll(f.w.WorktreesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, f.w.Root, "worktree", "add", "-q", "--detach", path, a.Commit)
	f.ctx.Cwd = path
}
func (f *canonicalFixture) command() string { return "touch '" + f.marker + "'" }
func (f *canonicalFixture) refused(t *testing.T, err error, ran bool) {
	t.Helper()
	if clikit.ExitCode(err) != 3 {
		t.Fatalf("canonical artifact refusal missing: exit=%d err=%v", clikit.ExitCode(err), err)
	}
	got, e := store.FindTask(f.w, f.task.ID)
	if e != nil {
		t.Fatal(e)
	}
	if got.Status == model.StatusDone || got.Acceptance()[0].Done || len(store.VerificationEvidenceRecords(got)) != 0 {
		t.Fatalf("stale canonical artifact closed or recorded success: status=%s evidence=%+v", got.Status, store.VerificationEvidenceRecords(got))
	}
	_, e = os.Stat(f.marker)
	if !ran && !os.IsNotExist(e) {
		t.Fatalf("verification command ran before canonical artifact refusal: %v", e)
	}
}
func (f *canonicalFixture) args() []string {
	return []string{f.task.ID, "--verify", f.command(), "--require-verify", "--into", "main"}
}

func TestCore541DefaultOwnerAcceptRefusesStaleCanonicalHead(t *testing.T) {
	f := newCanonicalFixture(t)
	f.checkout(t, f.old)
	f.refused(t, cmdAccept(f.ctx, f.args()), false)
	if f.checks != 0 {
		t.Fatal("external checks observed for stale checkout")
	}
}
func TestCore541ExplicitStalePairCannotSelectVerifierTree(t *testing.T) {
	f := newCanonicalFixture(t)
	// Even a current checkout cannot make an explicitly stale pair authoritative.
	args := append(f.args(), "--final-commit", f.old.Commit, "--final-tree", f.old.Tree)
	f.refused(t, cmdAccept(f.ctx, args), false)
	if f.checks != 0 {
		t.Fatal("external checks observed for supplied stale pair")
	}
}
func TestCore541OwnerAcceptMatchedReviewedAndLandingArtifacts(t *testing.T) {
	for _, landing := range []bool{false, true} {
		t.Run(fmt.Sprintf("landing-%t", landing), func(t *testing.T) {
			f := newCanonicalFixture(t)
			args := f.args()
			if landing {
				args = append(args, "--final-commit", f.reviewed.Commit, "--final-tree", f.reviewed.Tree)
			}
			if err := cmdAccept(f.ctx, args); err != nil {
				t.Fatal(err)
			}
			got, e := store.FindTask(f.w, f.task.ID)
			if e != nil {
				t.Fatal(e)
			}
			records := store.VerificationEvidenceRecords(got)
			if got.Status != model.StatusDone || len(records) != 1 || records[0].CommitSHA != f.reviewed.Commit || records[0].TreeSHA != f.reviewed.Tree || f.checks != 1 {
				t.Fatalf("matched artifact not certified: task=%s records=%+v checks=%d", got.Status, records, f.checks)
			}
		})
	}
}
func TestCore541BatchOwnerAcceptBindsEveryTask(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale-%t", stale), func(t *testing.T) {
			f := newCanonicalFixture(t)
			second := mkTask(t, f.w, "second canonical reviewed artifact")
			git(t, f.w.Root, "branch", store.TaskBranch(second), f.reviewed.Commit)
			for _, task := range []*store.Task{f.task, second} {
				if err := propose(f.ctx, f.w, f.root, task); err != nil {
					t.Fatal(err)
				}
			}
			if stale {
				f.checkout(t, f.old)
			}
			err := cmdAccept(f.ctx, []string{"--all", "--force", "--verify", f.command(), "--require-verify", "--into", "main"})
			if stale {
				f.refused(t, err, false)
			} else if err != nil {
				t.Fatal(err)
			}
			for _, task := range []*store.Task{f.task, second} {
				got, e := store.FindTask(f.w, task.ID)
				if e != nil {
					t.Fatal(e)
				}
				if stale && (got.Status == model.StatusDone || len(store.VerificationEvidenceRecords(got)) != 0) {
					t.Fatal("stale batch certified task")
				}
				if !stale && got.Status != model.StatusDone {
					t.Fatal("matched batch did not certify every task")
				}
			}
		})
	}
}

func (f *canonicalFixture) advance(t *testing.T) acceptanceArtifact {
	t.Helper()
	git(t, f.w.Root, "checkout", "-qb", "future")
	if err := os.WriteFile(f.w.Root+"/next", []byte("next\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, f.w.Root, "add", "next")
	git(t, f.w.Root, "commit", "-qm", "future task head")
	a, err := localAcceptanceArtifact(f.w, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	git(t, f.w.Root, "checkout", "-q", "main")
	return a
}
func TestCore541HeadAdvanceDuringCommandRefusesBeforeExternalChecks(t *testing.T) {
	f := newCanonicalFixture(t)
	next := f.advance(t)
	f.checkout(t, f.reviewed)
	// PR selection still reports the previous head, but its task branch advanced.
	command := fmt.Sprintf("git update-ref refs/heads/%s %s; %s", store.TaskBranch(f.task), next.Commit, f.command())
	args := f.args()
	args[2] = command
	f.refused(t, cmdAccept(f.ctx, args), true)
	if f.checks != 0 {
		t.Fatal("external checks observed after canonical head advanced during command")
	}
}
func TestCore541HeadAdvanceDuringPolicyRefusesInsideTaskLock(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch-%t", batch), func(t *testing.T) {
			f := newCanonicalFixture(t)
			next := f.advance(t)
			f.onChecks = func() {
				git(t, f.w.Root, "update-ref", "refs/heads/"+store.TaskBranch(f.task), next.Commit)
				f.prHead = next.Commit
			}
			args := f.args()
			if batch {
				if err := propose(f.ctx, f.w, f.root, f.task); err != nil {
					t.Fatal(err)
				}
				args = []string{"--all", "--force", "--verify", f.command(), "--require-verify", "--into", "main"}
			}
			f.refused(t, cmdAccept(f.ctx, args), true)
			if f.checks != 1 {
				t.Fatalf("policy advance fixture was not exercised: %d", f.checks)
			}
		})
	}
}
func TestCore541DefaultReviewedHeadDiffersFromExplicitLandingTree(t *testing.T) {
	f := newCanonicalFixture(t)
	next := f.advance(t)
	git(t, f.w.Root, "merge", "--ff-only", "future")
	git(t, f.w.Root, "update-ref", "refs/remotes/origin/main", next.Commit)
	f.refused(t, cmdAccept(f.ctx, f.args()), false)
	args := append(f.args(), "--final-commit", next.Commit, "--final-tree", next.Tree)
	if err := cmdAccept(f.ctx, args); err != nil {
		t.Fatalf("explicit current landing artifact refused: %v", err)
	}
}
func TestCore541CanonicalSelectionRefusesHistoricalAndMalformedPR(t *testing.T) {
	for _, kind := range []string{"historical", "missing-head", "wrong-repo", "wrong-base", "unknown-state"} {
		t.Run(kind, func(t *testing.T) {
			f := newCanonicalFixture(t)
			head, repo, base, state := f.reviewed.Commit, "fixture/repo", "main", "MERGED"
			switch kind {
			case "historical":
				head = f.old.Commit
			case "missing-head":
				head = ""
			case "wrong-repo":
				repo = "other/repo"
			case "wrong-base":
				base = "other"
			case "unknown-state":
				state = "UNKNOWN"
			}
			runLandingGH = func(string, ...string) (string, error) {
				return fmt.Sprintf(`[{"number":7,"url":"https://github.com/%s/pull/7","state":%q,"baseRefName":%q,"headRefName":%q,"headRefOid":%q}]`, repo, state, base, store.TaskBranch(f.task), head), nil
			}
			f.refused(t, cmdAccept(f.ctx, f.args()), false)
		})
	}
}
func TestCore541CanonicalObjectsIgnoreReplacementTrees(t *testing.T) {
	f := newCanonicalFixture(t)
	next := f.advance(t)
	git(t, f.w.Root, "replace", f.reviewed.Commit, next.Commit)
	a, err := localAcceptanceArtifact(f.w, f.reviewed.Commit)
	if err != nil || a != f.reviewed {
		t.Fatalf("canonical immutable object used replacement tree: artifact=%+v want=%+v err=%v", a, f.reviewed, err)
	}
}
func TestCore541DeferredPreintegrationDoesNotSelectLandingArtifact(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	task := mkTask(t, w, "preintegration verification")
	observeAcceptanceArtifact = func(*workspace.Workspace, *store.Task, string, bool) (acceptanceArtifact, error) {
		t.Fatal("preintegration selected canonical landing artifact")
		return acceptanceArtifact{}, nil
	}
	if err := acceptOne(ctx, w, root, task, "true", true, false, false, false, true, ""); err != nil {
		t.Fatal(err)
	}
}
func TestCore541BranchlessLocalCompatibility(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	observeAcceptanceArtifact = canonicalAcceptanceArtifact
	task := mkTask(t, w, "local task without PR contract")
	if err := acceptOne(ctx, w, root, task, "true", true, false, false, false, false, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCore541RecordedPRSurvivesDeletedTaskBranch(t *testing.T) {
	f := newCanonicalFixture(t)
	store.AppendLog(f.task, "PR https://github.com/fixture/repo/pull/7")
	if err := store.SaveTask(f.task); err != nil {
		t.Fatal(err)
	}
	git(t, f.w.Root, "branch", "-D", store.TaskBranch(f.task))
	if err := cmdAccept(f.ctx, f.args()); err != nil {
		t.Fatalf("exact recorded reviewed PR unavailable after branch deletion: %v", err)
	}
}
func TestCore541UnknownTaskGenerationCannotSelectHistoricalPR(t *testing.T) {
	f := newCanonicalFixture(t)
	git(t, f.w.Root, "branch", "-D", store.TaskBranch(f.task))
	f.refused(t, cmdAccept(f.ctx, f.args()), false)
}
func TestCore541RemoteCommitTreeObservationIsExactAndTyped(t *testing.T) {
	f := newCanonicalFixture(t)
	for _, landing := range []bool{false, true} {
		t.Run(fmt.Sprintf("landing-%t", landing), func(t *testing.T) {
			for _, valid := range []bool{false, true} {
				runLandingGH = func(_ string, args ...string) (string, error) {
					expected := "repos/fixture/repo/git/commits/" + f.reviewed.Commit
					if landing {
						expected = "repos/fixture/repo/commits/main"
					}
					if len(args) != 2 || args[0] != "api" || args[1] != expected {
						t.Fatalf("wrong immutable object endpoint: %v", args)
					}
					tree := f.reviewed.Tree
					if !valid {
						tree = "missing"
					}
					if landing {
						return fmt.Sprintf(`{"sha":%q,"commit":{"tree":{"sha":%q}}}`, f.reviewed.Commit, tree), nil
					}
					return fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, f.reviewed.Commit, tree), nil
				}
				ref := f.reviewed.Commit
				if landing {
					ref = "main"
				}
				got, err := acceptanceAPIArtifact(f.w, "fixture/repo", ref, landing)
				if valid && (err != nil || got != f.reviewed) {
					t.Fatalf("valid exact GitHub commit/tree refused: %+v %v", got, err)
				}
				if !valid && err == nil {
					t.Fatal("malformed remote tree became canonical artifact")
				}
			}
		})
	}
}

func TestCore541AllowUnlandedCannotBypassReviewedPR(t *testing.T) {
	f := newCanonicalFixture(t)
	f.checkout(t, f.old)
	f.refused(t, cmdAccept(f.ctx, append(f.args(), "--allow-unlanded")), false)
}

package releasetrain

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

func trainEnv(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	prior, hadPrior := os.LookupEnv("DACLI_AGENT")
	if err := os.Unsetenv("DACLI_AGENT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPrior {
			_ = os.Setenv("DACLI_AGENT", prior)
		} else {
			_ = os.Unsetenv("DACLI_AGENT")
		}
	})
	w, err := workspace.Init(t.TempDir(), "train")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, argv := range [][]string{{"config", "user.email", "t@example.test"}, {"config", "user.name", "test"}, {"commit", "--allow-empty", "-qm", "base"}} {
		cmd = exec.Command("git", argv...)
		cmd.Dir = w.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", argv, err, out)
		}
	}
	baseOut, _ := exec.Command("git", "-C", w.Root, "rev-parse", "HEAD").Output()
	base := strings.TrimSpace(string(baseOut))
	p, err := store.CreateProject(w, "a-root", "Core", "core", "goal", "")
	if err != nil {
		t.Fatal(err)
	}
	p.Doc.Front.Set("github_repo", "acme/widgets")
	p.Doc.Front.Set("release_merge_authority", "true")
	if err := store.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	done, err := store.CreateTask(w, "a-root", "core", "Accepted", store.TaskOpts{Accept: []string{"verified"}})
	if err != nil {
		t.Fatal(err)
	}
	store.CheckAllAcceptance(done)
	done.Doc.Front.SetBlock("github", "  issue: 55\n  repo: acme/widgets")
	if err := store.SaveTask(done); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveTask(w, done, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(w, "a-root", "core", "Still open", store.TaskOpts{Accept: []string{"pending"}}); err != nil {
		t.Fatal(err)
	}
	return w, base
}

func withTrainSeams(t *testing.T) {
	t.Helper()
	ors, opr, ogh, ofetch, odel, opresent, ocrash, oexternal := remoteSHA, observePromotionPRs, runGitHub, fetchBranch, deleteSource, remoteBranchPresent, crashAfter, observeExternalVerification
	t.Cleanup(func() {
		remoteSHA, observePromotionPRs, runGitHub, fetchBranch, deleteSource, remoteBranchPresent, crashAfter, observeExternalVerification = ors, opr, ogh, ofetch, odel, opresent, ocrash, oexternal
	})
	remoteBranchPresent = func(string, string) (bool, error) { return true, nil }
}

func ctx(w *workspace.Workspace, json bool) (*clikit.Ctx, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &clikit.Ctx{Cwd: w.Root, Stdout: out, Stderr: out, JSON: json}, out
}

func TestDryRunExactPlanDoesNotPersistTransaction(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "release/v2" {
			return strings.Repeat("a", 40), nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, repo, source, target, _, _ string) (promotionEvidence, error) {
		if repo != "acme/widgets" || source != "release/v2" || target != "master" {
			t.Fatalf("silent branch/repo fallback: %s %s %s", repo, source, target)
		}
		return promotionEvidence{Numbers: []int{41, 44}, Heads: map[string]bool{"dacli/001-accepted": true}}, nil
	}
	c, out := ctx(w, true)
	if err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "release/v2", "--target", "master", "--required-check", "test", "--required-reviews", "2", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"source": "release/v2"`, `"target": "master"`, `"source_sha": "aaaaaaaa`, `"included_pull_requests"`, `41`, `"required_reviews": 2`, `Accepted`, `Still open`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan omitted %q:\n%s", want, out)
		}
	}
	if _, err := store.ReadReleaseTrain(w, "core", "release/v2", "master"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run persisted transaction: %v", err)
	}
}

func TestApplyPersistsCreatedPRBeforeCrashAndResumesWithoutDuplicate(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "dev" {
			return strings.Repeat("b", 40), nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
		return promotionEvidence{Numbers: []int{70}, Heads: map[string]bool{"dacli/001-accepted": true}}, nil
	}
	creates := 0
	runGitHub = func(_ string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(s, "pr list"):
			return `[]`, nil
		case strings.HasPrefix(s, "pr create"):
			creates++
			return "https://github.com/acme/widgets/pull/91", nil
		case strings.HasPrefix(s, "pr view"):
			return fmt.Sprintf(`{"number":91,"url":"u","state":"OPEN","headRefOid":"%s","reviews":[]}`, strings.Repeat("b", 40)), nil
		default:
			return "", fmt.Errorf("unexpected gh %s", s)
		}
	}
	crashAfter = func(phase string) error {
		if phase == "pr-create" {
			return errors.New("fixture crash after pr create")
		}
		return nil
	}
	c, _ := ctx(w, false)
	err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--required-check", "test", "--apply"})
	if err == nil || !strings.Contains(err.Error(), "fixture crash") {
		t.Fatalf("crash fixture = %v", err)
	}
	tx, err := store.ReadReleaseTrain(w, "core", "dev", "main")
	if err != nil || tx.PullRequest != 91 || tx.Phase != "pr-persisted" {
		t.Fatalf("identity not durably persisted: %+v %v", tx, err)
	}
	crashAfter = func(string) error { return nil }
	views := 0
	runGitHub = func(_ string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		if strings.HasPrefix(s, "pr create") {
			creates++
			return "", errors.New("duplicate")
		}
		if strings.HasPrefix(s, "pr view") {
			views++
			if len(args) < 3 || args[2] != "91" {
				t.Fatalf("resume observed a different PR: %v", args)
			}
			return fmt.Sprintf(`{"number":91,"url":"u","state":"OPEN","headRefOid":"%s","reviews":[]}`, strings.Repeat("b", 40)), nil
		}
		if strings.HasPrefix(s, "pr checks") {
			return `[{"name":"test","bucket":"pending","state":"QUEUED"}]`, nil
		}
		return "", fmt.Errorf("unexpected gh %s", s)
	}
	// Omit the check on restart: the durable transaction must retain it rather
	// than treating fewer flags as permission to weaken the gate.
	crashAfter = func(phase string) error {
		if phase == "pending-ci" {
			return errors.New("fixture crash after pending-ci")
		}
		return nil
	}
	err = cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--apply"})
	if err == nil || !strings.Contains(err.Error(), "fixture crash after pending-ci") {
		t.Fatalf("pending-CI crash fixture = %v", err)
	}
	if creates != 1 {
		t.Fatalf("pending-CI restart created %d PRs, want one", creates)
	}
	tx, _ = store.ReadReleaseTrain(w, "core", "dev", "main")
	if tx.Phase != "pending-gates" || tx.PullRequest != 91 || !reflect.DeepEqual(tx.RequiredChecks, []string{"test"}) {
		t.Fatalf("pending transaction lost PR/gates: %+v", tx)
	}
	crashAfter = func(string) error { return nil }
	if err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--apply"}); err != nil {
		t.Fatalf("restart after pending-CI crash: %v", err)
	}
	if creates != 1 || views != 2 {
		t.Fatalf("pending-CI restarts created %d PRs and viewed canonical PR %d times, want 1 and 2", creates, views)
	}
	tx, _ = store.ReadReleaseTrain(w, "core", "dev", "main")
	if tx.Phase != "pending-gates" || tx.PullRequest != 91 || !reflect.DeepEqual(tx.RequiredChecks, []string{"test"}) {
		t.Fatalf("pending-CI recovery changed canonical PR or gates: %+v", tx)
	}
}

func TestGitHubUnknownFailsClosedAndCannotMeanPRAbsent(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "dev" {
			return strings.Repeat("c", 40), nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
		return promotionEvidence{Heads: map[string]bool{}}, nil
	}
	created := false
	runGitHub = func(_ string, args ...string) (string, error) {
		if len(args) > 1 && args[1] == "create" {
			created = true
		}
		return "", errors.New("authentication unknown")
	}
	c, _ := ctx(w, false)
	err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--apply"})
	if err == nil || !strings.Contains(err.Error(), "refusing to infer absence") || created {
		t.Fatalf("unknown fail-closed = %v created=%t", err, created)
	}
}

func TestMergeRequiresRecordedAuthorityAndCleanupOnlyAfterFreshContainment(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	cmd := exec.Command("git", "commit", "--allow-empty", "-qm", "source")
	cmd.Dir = w.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("source commit: %v %s", err, out)
	}
	sourceOut, _ := exec.Command("git", "-C", w.Root, "rev-parse", "HEAD").Output()
	source := strings.TrimSpace(string(sourceOut))
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "dev" {
			return source, nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
		return promotionEvidence{Heads: map[string]bool{}}, nil
	}
	runGitHub = func(_ string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		if strings.HasPrefix(s, "pr list") {
			return `[{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `"}]`, nil
		}
		if strings.HasPrefix(s, "pr view") {
			return `{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, nil
		}
		if strings.HasPrefix(s, "pr merge") {
			return "", nil
		}
		return "", fmt.Errorf("unexpected %s", s)
	}
	// Exact ancestry must be checked against a fresh remote-tracking target.
	fetchBranch = func(root, branch string) error {
		if branch != "main" {
			t.Fatalf("fetched fallback branch %s", branch)
		}
		_, err := exec.Command("git", "-C", root, "update-ref", "refs/remotes/origin/main", base).CombinedOutput()
		return err
	}
	deleted := false
	deleteSource = func(string, string) error { deleted = true; return nil }
	c, _ := ctx(w, false)
	err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--required-reviews", "1", "--merge", "--apply"})
	if err == nil || !strings.Contains(err.Error(), "does not contain reviewed source") || deleted {
		t.Fatalf("divergent cleanup = %v deleted=%t", err, deleted)
	}
	tx, _ := store.ReadReleaseTrain(w, "core", "dev", "main")
	if len(tx.ReconciledTasks) != 0 {
		t.Fatalf("unlanded tasks reconciled: %v", tx.ReconciledTasks)
	}
	// Mutation proof: if the containment guard is changed to accept false, this
	// assertion observes deleteSource and fails.
}

func TestCanonicalPRRequiresObservableOpenExactHeadBeforeMerge(t *testing.T) {
	for _, tc := range []struct {
		name, view, wantReason string
	}{
		{"missing-head", `{"number":12,"url":"u","state":"OPEN","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, "head SHA is unobservable"},
		{"missing-state", `{"number":12,"url":"u","headRefOid":"SOURCE","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, `state "" cannot authorize merge`},
		{"unknown-state", `{"number":12,"url":"u","state":"UNKNOWN","headRefOid":"SOURCE","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, `state "UNKNOWN" cannot authorize merge`},
		{"closed-state", `{"number":12,"url":"u","state":"CLOSED","headRefOid":"SOURCE","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, `state "CLOSED" cannot authorize merge`},
		{"changed-head", `{"number":12,"url":"u","state":"OPEN","headRefOid":"OTHER","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, "head changed from reviewed source SHA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTrainSeams(t)
			w, base := trainEnv(t)
			source := strings.Repeat("f", 40)
			remoteSHA = func(_ string, branch string) (string, error) {
				if branch == "dev" {
					return source, nil
				}
				return base, nil
			}
			observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
				return promotionEvidence{Heads: map[string]bool{}}, nil
			}
			viewed, mergeCalls := false, 0
			view := strings.NewReplacer("SOURCE", source, "OTHER", strings.Repeat("e", 40)).Replace(tc.view)
			runGitHub = func(_ string, args ...string) (string, error) {
				s := strings.Join(args, " ")
				switch {
				case strings.HasPrefix(s, "pr list"):
					return `[{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `"}]`, nil
				case strings.HasPrefix(s, "pr view"):
					viewed = true
					return view, nil
				case strings.HasPrefix(s, "pr merge"):
					mergeCalls++
					return "", nil
				default:
					return "", fmt.Errorf("unexpected gh %s", s)
				}
			}
			c, _ := ctx(w, false)
			err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--required-reviews", "1", "--merge", "--apply"})
			if !viewed {
				t.Fatal("fixture did not reach observeGates through apply")
			}
			if err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), tc.wantReason) || mergeCalls != 0 {
				t.Fatalf("PR observation must refuse before merge: err=%v code=%d mergeCalls=%d want_reason=%q", err, clikit.ExitCode(err), mergeCalls, tc.wantReason)
			}
		})
	}
}

func TestMergeUsesReviewedHeadCASAgainstHeadChangeAfterView(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	source := strings.Repeat("a", 40)
	changedHead := strings.Repeat("b", 40)
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "dev" {
			return source, nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
		return promotionEvidence{Heads: map[string]bool{}}, nil
	}
	mergeCalls, mergeSucceeded := 0, false
	matchHead := ""
	runGitHub = func(_ string, args ...string) (string, error) {
		switch {
		case len(args) > 1 && args[0] == "pr" && args[1] == "list":
			return `[{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `"}]`, nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "view":
			return `{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `","reviews":[{"state":"APPROVED","author":{"login":"reviewer"}}]}`, nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "merge":
			mergeCalls++
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--match-head-commit" {
					matchHead = args[i+1]
				}
			}
			if matchHead != source {
				return "", fmt.Errorf("merge omitted exact reviewed head compare-and-swap: got %q", matchHead)
			}
			// Model a head update after PR view but before GitHub evaluates its CAS.
			if source != changedHead {
				return "", fmt.Errorf("head changed after view: expected %s, current %s", matchHead, changedHead)
			}
			mergeSucceeded = true
			return "", nil
		default:
			return "", fmt.Errorf("unexpected gh %v", args)
		}
	}
	c, _ := ctx(w, false)
	err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--required-reviews", "1", "--merge", "--apply"})
	if err == nil || !strings.Contains(err.Error(), "head changed after view") || mergeCalls != 1 || matchHead != source || mergeSucceeded {
		t.Fatalf("stale viewed head was not rejected by exact merge CAS: err=%v calls=%d match=%q succeeded=%t", err, mergeCalls, matchHead, mergeSucceeded)
	}
}

func TestMergeNeedsDurableProjectAuthority(t *testing.T) {
	withTrainSeams(t)
	w, base := trainEnv(t)
	p, _ := store.LoadProject(w, "core")
	p.Doc.Front.Set("release_merge_authority", "false")
	if err := store.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	source := strings.Repeat("e", 40)
	remoteSHA = func(_ string, branch string) (string, error) {
		if branch == "dev" {
			return source, nil
		}
		return base, nil
	}
	observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
		return promotionEvidence{Heads: map[string]bool{}}, nil
	}
	merged := false
	runGitHub = func(_ string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		if strings.HasPrefix(s, "pr list") {
			return `[{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `"}]`, nil
		}
		if strings.HasPrefix(s, "pr view") {
			return `{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `","reviews":[]}`, nil
		}
		if strings.HasPrefix(s, "pr merge") {
			merged = true
			return "", nil
		}
		return "", fmt.Errorf("unexpected %s", s)
	}
	c, _ := ctx(w, false)
	err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--merge", "--apply"})
	if err == nil || clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "release_merge_authority") || merged {
		t.Fatalf("authority guard = %v merged=%t", err, merged)
	}
}

func TestAuthorityCommandPersistsAndRevokesProjectDecision(t *testing.T) {
	w, _ := trainEnv(t)
	c, _ := ctx(w, true)
	if err := cmdAuthority(c, []string{"--project", "core", "--revoke-merge"}); err != nil {
		t.Fatal(err)
	}
	p, _ := store.LoadProject(w, "core")
	if got, _ := p.Doc.Front.Get("release_merge_authority"); got != "false" {
		t.Fatalf("revoked authority = %q", got)
	}
	if err := cmdAuthority(c, []string{"--project", "core", "--allow-merge"}); err != nil {
		t.Fatal(err)
	}
	p, _ = store.LoadProject(w, "core")
	if got, _ := p.Doc.Front.Get("release_merge_authority"); got != "true" {
		t.Fatalf("allowed authority = %q", got)
	}
}

func TestCrashRestartFixturesAfterMergeFetchAndCleanup(t *testing.T) {
	for _, crashPhase := range []string{"merge", "fetch", "cleanup"} {
		t.Run(crashPhase, func(t *testing.T) {
			withTrainSeams(t)
			w, base := trainEnv(t)
			cmd := exec.Command("git", "commit", "--allow-empty", "-qm", "source")
			cmd.Dir = w.Root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("source commit: %v %s", err, out)
			}
			sourceOut, _ := exec.Command("git", "-C", w.Root, "rev-parse", "HEAD").Output()
			source := strings.TrimSpace(string(sourceOut))
			merged := false
			deletes := 0
			remoteSHA = func(_ string, branch string) (string, error) {
				if branch == "dev" {
					if deletes > 0 {
						return "", errors.New("remote branch absent")
					}
					return source, nil
				}
				if merged {
					return source, nil
				}
				return base, nil
			}
			observePromotionPRs = func(_, _, _, _, _, _ string) (promotionEvidence, error) {
				return promotionEvidence{Numbers: []int{8}, Heads: map[string]bool{"dacli/001-accepted": true}}, nil
			}
			runGitHub = func(_ string, args ...string) (string, error) {
				s := strings.Join(args, " ")
				if strings.HasPrefix(s, "pr list") {
					return `[{"number":12,"url":"u","state":"OPEN","headRefOid":"` + source + `"}]`, nil
				}
				if strings.HasPrefix(s, "pr view") {
					state := "OPEN"
					if merged {
						state = "MERGED"
					}
					return fmt.Sprintf(`{"number":12,"url":"u","state":"%s","headRefOid":"%s","reviews":[]}`, state, source), nil
				}
				if strings.HasPrefix(s, "pr merge") {
					merged = true
					return "", nil
				}
				return "", fmt.Errorf("unexpected %s", s)
			}
			fetchBranch = func(root, branch string) error {
				if branch != "main" {
					t.Fatalf("fetch branch = %s", branch)
				}
				_, err := exec.Command("git", "-C", root, "update-ref", "refs/remotes/origin/main", source).CombinedOutput()
				return err
			}
			remoteBranchPresent = func(string, string) (bool, error) { return deletes == 0, nil }
			deleteSource = func(_ string, branch string) error {
				if branch != "dev" {
					t.Fatalf("delete branch = %s", branch)
				}
				deletes++
				return nil
			}
			crashed := false
			crashAfter = func(phase string) error {
				if !crashed && phase == crashPhase {
					crashed = true
					return errors.New("fixture crash after " + phase)
				}
				return nil
			}
			c, _ := ctx(w, false)
			err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--merge", "--apply"})
			if err == nil || !strings.Contains(err.Error(), "fixture crash") {
				t.Fatalf("%s crash = %v", crashPhase, err)
			}
			if err := cmdReleaseTrain(c, []string{"--project", "core", "--source", "dev", "--target", "main", "--merge", "--apply"}); err != nil {
				t.Fatalf("restart after %s: %v", crashPhase, err)
			}
			tx, err := store.ReadReleaseTrain(w, "core", "dev", "main")
			if err != nil || !tx.CleanupComplete || tx.Phase != "complete" {
				t.Fatalf("restart final tx = %+v err=%v", tx, err)
			}
			if deletes != 1 {
				t.Fatalf("cleanup executions = %d, want exactly one", deletes)
			}
			if len(tx.ReconciledTasks) != 1 {
				t.Fatalf("accepted-task reconciliation = %v", tx.ReconciledTasks)
			}
		})
	}
}

func TestNoTagOrPublishSurface(t *testing.T) {
	usage := Commands[0].Usage
	for _, forbidden := range []string{"--tag", "--release", "publish"} {
		if strings.Contains(usage, forbidden) {
			t.Fatalf("release train exposes forbidden %q path: %s", forbidden, usage)
		}
	}
}

func TestCanonicalPRSelectionIgnoresHistoricalBranchGeneration(t *testing.T) {
	withTrainSeams(t)
	runGitHub = func(string, ...string) (string, error) {
		return `[{"number":4,"url":"old","state":"MERGED","headRefOid":"old"},{"number":9,"url":"new","state":"OPEN","headRefOid":"exact"}]`, nil
	}
	got, err := findCanonicalPR(t.TempDir(), "acme/widgets", "dev", "main", "exact")
	if err != nil || got.Number != 9 {
		t.Fatalf("canonical generation = %+v err=%v", got, err)
	}
}

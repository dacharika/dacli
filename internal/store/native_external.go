package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/prci"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// NativeExternalTarget is an explicit owner selection. Credential is private
// child environment data, never argv, status, receipts or command output.
type NativeExternalTarget struct {
	Workspace                                                                         *workspace.Workspace
	Project, Task, Repository, Checkout, GitHubRepository, SourceBranch, TargetBranch string
	Commit, Tree, ExpectedTrunk, ExpectedRemoteHead                                   string
	Generation, SliceGeneration                                                       int
	RequiredChecks                                                                    []string
	Credential                                                                        string
}
type NativeExternalFacts struct {
	Generation, SliceGeneration                   int
	Branch, Commit, Tree, Trunk, RemoteHead       string
	Clean                                         bool
	PullRequestNumber                             int
	PullRequestURL, PullRequestState, MergeCommit string
	ChecksSatisfied, APIComplete                  bool
	Diagnosis                                     prci.Result
	ObservedAt                                    time.Time
}

var nativeExternalRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func validateExternalTarget(t NativeExternalTarget) error {
	if t.Workspace == nil || t.Task == "" || t.Project == "" || !nativeExternalRepo.MatchString(t.GitHubRepository) || strings.Contains(t.GitHubRepository, "..") || t.Credential == "" || t.SourceBranch == t.TargetBranch || len(t.RequiredChecks) == 0 {
		return Refusedf("explicit native external owner target required")
	}
	for _, p := range []string{t.Repository, t.Checkout} {
		real, e := filepath.EvalSymlinks(p)
		if e != nil || !filepath.IsAbs(p) || real != p || filepath.Clean(p) != p {
			return Refusedf("external repository alias refused")
		}
	}
	for _, ref := range []string{t.SourceBranch, t.TargetBranch} {
		if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\\\x00\r\n") {
			return Refusedf("external ref invalid")
		}
	}
	if !nativeIntegrationObject(t.Commit) || !nativeIntegrationObject(t.Tree) || !nativeIntegrationObject(t.ExpectedTrunk) || (t.ExpectedRemoteHead != "" && !nativeIntegrationObject(t.ExpectedRemoteHead)) {
		return Refusedf("exact external object required")
	}
	return nil
}

// Descriptor-local finite buffers bound successful API output as well as failed
// diagnostics. Unknown/oversize responses never become successful observations.
type externalBuffer struct {
	data     []byte
	overflow bool
}

func (b *externalBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remain := (1 << 20) - len(b.data)
	if len(p) > remain {
		b.overflow = true
		p = p[:remain]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func externalCommand(parent context.Context, t NativeExternalTarget, exe, operation string, args ...string) ([]byte, error) {
	if parent == nil {
		return nil, Refusedf("external context required")
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = t.Checkout
	env := []string{}
	for _, v := range VerificationEnvironment() {
		key := strings.SplitN(v, "=", 2)[0]
		if strings.HasPrefix(key, "GH_") || strings.HasPrefix(key, "GITHUB_") || strings.HasPrefix(key, "GIT_") {
			continue
		}
		env = append(env, v)
	}
	cmd.Env = append(env, "GH_TOKEN="+t.Credential, "GH_HOST=github.com", "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	cmd.WaitDelay = 250 * time.Millisecond
	var out, stderr externalBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	e := cmd.Run()
	if out.overflow || stderr.overflow {
		return nil, Refusedf("bounded external response exceeded")
	}
	if e != nil {
		return nil, commandresult.NewExternalError(cmd, commandresult.RunOptions{Operation: operation, WorkspaceRoot: t.Checkout}, out.data, stderr.data, e, ctx.Err() != nil)
	}
	return out.data, nil
}
func externalSafe(t NativeExternalTarget, value string) string {
	cmd := &exec.Cmd{Env: []string{"GH_TOKEN=" + t.Credential}, Dir: t.Checkout}
	return commandresult.SanitizeCommandTail(cmd, value, t.Checkout)
}
func externalGet(ctx context.Context, t NativeExternalTarget, endpoint string, into any) error {
	raw, e := externalCommand(ctx, t, "gh", "github observation", "api", "--method", "GET", endpoint)
	if e != nil {
		return e
	}
	if json.Unmarshal(raw, into) != nil {
		return Refusedf("malformed external observation")
	}
	return nil
}
func externalRef(ctx context.Context, t NativeExternalTarget, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	e := externalGet(ctx, t, "repos/"+t.GitHubRepository+"/git/ref/heads/"+url.PathEscape(branch), &ref)
	if e != nil {
		// Only an explicit HTTP404 establishes absence. Authentication, timeout,
		// unknown payloads and other failures must remain unknown.
		if d, ok := commandresult.AsDiagnostic(e); ok && strings.Contains(d.StderrTail, "HTTP 404") {
			return "", nil
		}
		return "", e
	}
	if !nativeIntegrationObject(ref.Object.SHA) {
		return "", Refusedf("external ref object unknown")
	}
	return ref.Object.SHA, nil
}
func externalGeneration(t *Task, key string) (int, error) {
	v, ok := t.Doc.Front.Get(key)
	if !ok {
		return 0, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 0 {
		return 0, Refusedf("canonical generation malformed")
	}
	return n, nil
}
func ObserveNativeExternal(parent context.Context, t NativeExternalTarget) (NativeExternalFacts, error) {
	var out NativeExternalFacts
	if parent == nil || validateExternalTarget(t) != nil {
		return out, Refusedf("external selection invalid")
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	task, e := NativeAcceptanceTask(t.Workspace, t.Task, t.Project)
	if e != nil {
		return out, e
	}
	g, e := externalGeneration(task, "generation")
	if e != nil {
		return out, e
	}
	sg, e := externalGeneration(task, "delivery_generation")
	if e != nil {
		return out, e
	}
	if g != t.Generation || sg != t.SliceGeneration || TaskBranch(task) != t.SourceBranch {
		return out, Refusedf("external canonical generation/head mismatch")
	}
	run := func(args ...string) (string, error) { return nativeIntegrationGit(ctx, t.Checkout, args...) }
	top, e := run("rev-parse", "--show-toplevel")
	if e != nil || strings.TrimSpace(top) != t.Checkout {
		return out, Refusedf("external whole checkout required")
	}
	common, e := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || strings.TrimSpace(common) != t.Repository {
		return out, Refusedf("external repository mismatch")
	}
	for _, ref := range []string{t.SourceBranch, t.TargetBranch} {
		if _, e = run("check-ref-format", "refs/heads/"+ref); e != nil {
			return out, Refusedf("external ref refused")
		}
	}
	commit, e := run("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil || strings.TrimSpace(commit) != t.Commit {
		return out, Refusedf("external source head changed")
	}
	tree, e := run("rev-parse", "--verify", t.Commit+"^{tree}")
	if e != nil || strings.TrimSpace(tree) != t.Tree {
		return out, Refusedf("external genuine tree changed")
	}
	keys, e := run("config", "--null", "--name-only", "--list")
	if e != nil {
		return out, Refusedf("external Git config unavailable")
	}
	for _, key := range strings.Split(keys, "\x00") {
		key = strings.ToLower(key)
		if (strings.HasPrefix(key, "filter.") && (strings.HasSuffix(key, ".clean") || strings.HasSuffix(key, ".process"))) || (strings.HasPrefix(key, "url.") && (strings.HasSuffix(key, ".insteadof") || strings.HasSuffix(key, ".pushinsteadof"))) || strings.HasPrefix(key, "http.") || strings.HasPrefix(key, "credential.") || strings.HasSuffix(key, ".receivepack") || strings.HasSuffix(key, ".uploadpack") {
			return out, Refusedf("external executable or rerouting Git config refused")
		}
	}
	if t.ExpectedRemoteHead != "" {
		if _, e = run("merge-base", "--is-ancestor", t.ExpectedRemoteHead, t.Commit); e != nil {
			return out, Refusedf("external push requires locally observed genuine fast-forward ancestry")
		}
	}
	status, e := run("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil || strings.TrimSpace(status) != "" {
		return out, Refusedf("external dirty source refused")
	}
	out = NativeExternalFacts{Generation: g, SliceGeneration: sg, Branch: t.SourceBranch, Commit: t.Commit, Tree: t.Tree, Clean: true}
	trunk, e := externalRef(ctx, t, t.TargetBranch)
	if e != nil {
		return out, e
	}
	if trunk == "" {
		return out, Refusedf("external target ref missing")
	}
	out.Trunk = trunk
	head, e := externalRef(ctx, t, t.SourceBranch)
	if e != nil {
		return out, e
	}
	out.RemoteHead = head
	var prs []struct {
		Number                                                                             int
		URL, State, HeadRefName, HeadRefOid, BaseRefName, MergeStateStatus, ReviewDecision string
		MergeCommit                                                                        *struct{ OID string }
	}
	raw, e := externalCommand(ctx, t, "gh", "github pull requests", "pr", "list", "--repo", t.GitHubRepository, "--head", t.SourceBranch, "--base", t.TargetBranch, "--state", "all", "--limit", "100", "--json", "number,url,state,headRefName,headRefOid,baseRefName,mergeStateStatus,reviewDecision,mergeCommit")
	in := prci.Input{CanonicalHead: t.SourceBranch, CanonicalHeadOID: t.Commit, Now: time.Now().UTC()}
	if e != nil {
		in.AccessFailure = &prci.AccessFailure{Operation: "pull request observation", Message: e.Error()}
		out.Diagnosis = prci.Diagnose(in)
		out.ObservedAt = time.Now().UTC()
		return out, nil
	}
	if json.Unmarshal(raw, &prs) != nil || len(prs) >= 100 {
		return out, Refusedf("incomplete or malformed external PR set")
	}
	selected := 0
	for _, p := range prs {
		if p.HeadRefName != t.SourceBranch || p.BaseRefName != t.TargetBranch {
			continue
		}
		in.PullRequests = append(in.PullRequests, prci.PullRequest{Number: p.Number, URL: p.URL, State: p.State, Head: p.HeadRefName, HeadOID: p.HeadRefOid, MergeState: p.MergeStateStatus, ReviewDecision: p.ReviewDecision})
		if p.HeadRefOid == t.Commit && p.Number > selected {
			selected = p.Number
			out.PullRequestNumber = p.Number
			out.PullRequestURL = p.URL
			out.PullRequestState = p.State
			out.MergeCommit = ""
			if p.MergeCommit != nil {
				out.MergeCommit = p.MergeCommit.OID
			}
		}
	}
	var checks struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Name, Status, Conclusion string
			HTMLURL                  string     `json:"html_url"`
			StartedAt                *time.Time `json:"started_at"`
			Output                   struct{ Title, Summary, Text string }
		} `json:"check_runs"`
	}
	if e = externalGet(ctx, t, "repos/"+t.GitHubRepository+"/commits/"+t.Commit+"/check-runs?per_page=100", &checks); e != nil {
		in.AccessFailure = &prci.AccessFailure{Operation: "check observation", Message: e.Error()}
	} else {
		if checks.Total != len(checks.Runs) || checks.Total > 100 {
			return out, Refusedf("incomplete check observation")
		}
		for _, c := range checks.Runs {
			in.Checks = append(in.Checks, prci.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, StartedAt: c.StartedAt, URL: c.HTMLURL, Annotations: []prci.Evidence{{Source: "check_run", Message: externalSafe(t, c.Output.Title+" "+c.Output.Summary+" "+c.Output.Text), URL: c.HTMLURL}}})
		}
	}
	var runs struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Name, Status, Conclusion string
			HeadSHA                  string     `json:"head_sha"`
			HTMLURL                  string     `json:"html_url"`
			CreatedAt                *time.Time `json:"created_at"`
		} `json:"workflow_runs"`
	}
	if in.AccessFailure == nil {
		if e = externalGet(ctx, t, "repos/"+t.GitHubRepository+"/actions/runs?head_sha="+t.Commit+"&per_page=100", &runs); e != nil {
			in.AccessFailure = &prci.AccessFailure{Operation: "workflow observation", Message: e.Error()}
		} else {
			if runs.Total != len(runs.Runs) || runs.Total > 100 {
				return out, Refusedf("incomplete workflow observation")
			}
			for _, r := range runs.Runs {
				if r.HeadSHA == t.Commit {
					in.WorkflowRuns = append(in.WorkflowRuns, prci.WorkflowRun{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion, CreatedAt: r.CreatedAt, URL: r.HTMLURL})
				}
			}
		}
	}
	// Required checks must be present exactly once with recognized success. The
	// classifier's neutral/skipped compatibility does not waive owner requirements.
	out.ChecksSatisfied = in.AccessFailure == nil
	for _, name := range t.RequiredChecks {
		count := 0
		for _, c := range in.Checks {
			if c.Name == name {
				count++
				if c.Status != "completed" || c.Conclusion != "success" {
					out.ChecksSatisfied = false
				}
			}
		}
		if count != 1 {
			out.ChecksSatisfied = false
		}
	}
	for _, c := range in.Checks {
		if c.Status != "completed" || (c.Conclusion != "success" && c.Conclusion != "neutral" && c.Conclusion != "skipped") {
			out.ChecksSatisfied = false
		}
	}
	for _, w := range in.WorkflowRuns {
		if w.Status != "completed" || (w.Conclusion != "success" && w.Conclusion != "neutral" && w.Conclusion != "skipped") {
			out.ChecksSatisfied = false
		}
	}
	out.APIComplete = in.AccessFailure == nil
	out.Diagnosis = prci.Diagnose(in)
	if out.Diagnosis.Code == "ready" && !out.ChecksSatisfied {
		out.Diagnosis = prci.Result{Code: "required_checks_missing", Summary: "Required exact-head checks have not established success", Next: "observe the configured named required checks on the canonical head; do not infer success", Retryable: true}
	}
	if trunk != t.ExpectedTrunk && !(out.PullRequestState == "MERGED" && out.MergeCommit == t.Commit && trunk == t.Commit) {
		out.Diagnosis = prci.Result{Code: "stale_pending_landing", Summary: "External trunk advanced beyond the pinned landing premise", Next: "repin resulting tree and obtain fresh verification/review; preserve controller budgets and progress counters"}
	}
	if out.PullRequestState == "MERGED" && out.MergeCommit != t.Commit {
		out.Diagnosis = prci.Result{Code: "late_auto_merge", Summary: "External merge result differs from the reviewed committed tree", Next: "record the actual merge result and verify/review that exact resulting tree before integration or acceptance"}
	}
	// All slow reads precede the final canonical/local freshness check.
	fresh, e := NativeAcceptanceTask(t.Workspace, t.Task, t.Project)
	if e != nil {
		return NativeExternalFacts{}, e
	}
	fg, e := externalGeneration(fresh, "generation")
	if e != nil {
		return NativeExternalFacts{}, e
	}
	fsg, e := externalGeneration(fresh, "delivery_generation")
	if e != nil || fg != g || fsg != sg || TaskBranch(fresh) != t.SourceBranch {
		return NativeExternalFacts{}, Refusedf("external generation changed during observation")
	}
	again, e := run("rev-parse", "--verify", "HEAD^{commit}")
	if e != nil || again != commit {
		return NativeExternalFacts{}, Refusedf("external source changed during observation")
	}
	status, e = run("status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil || strings.TrimSpace(status) != "" {
		return NativeExternalFacts{}, Refusedf("external source changed during observation")
	}
	out.ObservedAt = time.Now().UTC()
	return out, nil
}
func WithNativeExternalTarget(ctx context.Context, t NativeExternalTarget, fn func() error) error {
	if ctx == nil || validateExternalTarget(t) != nil || fn == nil {
		return Refusedf("external selection required")
	}
	release, e := acquireFileLock(t.Repository + "/dacli-native-external.lock")
	if e != nil {
		return e
	}
	defer release()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fn()
}

// Only this explicit owner effect path writes externally. Replay/reconcile never
// calls it; each effect is preceded by exact remote and pure authority checks.
func ApplyNativeExternalEffect(ctx context.Context, t NativeExternalTarget, action string, final func() error) (NativeExternalFacts, error) {
	before, e := ObserveNativeExternal(ctx, t)
	if e != nil {
		return NativeExternalFacts{}, e
	}
	if !before.APIComplete || before.Trunk != t.ExpectedTrunk || final == nil {
		return NativeExternalFacts{}, Refusedf("external target changed")
	}
	if action == "push" && before.RemoteHead != t.ExpectedRemoteHead {
		return NativeExternalFacts{}, Refusedf("external source CAS changed")
	}
	if action == "create_pr" && (before.RemoteHead != t.Commit || before.PullRequestNumber != 0) {
		return NativeExternalFacts{}, Refusedf("external PR premise changed")
	}
	if action != "push" && action != "create_pr" {
		return NativeExternalFacts{}, Refusedf("external action unsupported")
	}
	if action == "push" && t.ExpectedRemoteHead != "" {
		if _, e = nativeIntegrationGit(ctx, t.Checkout, "merge-base", "--is-ancestor", t.ExpectedRemoteHead, t.Commit); e != nil {
			return NativeExternalFacts{}, Refusedf("external push must fast-forward pinned remote head")
		}
	}
	if e = final(); e != nil {
		return NativeExternalFacts{}, e
	}
	switch action {
	case "push":
		// Explicit HTTPS repository, disabled inherited helper, private GH_TOKEN
		// used by gh's credential adapter; no environment or token in argv.
		_, e = externalCommand(ctx, t, "git", "native external push", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "-c", "push.followTags=false", "push", "--porcelain", "--force-with-lease=refs/heads/"+t.SourceBranch+":"+t.ExpectedRemoteHead, "https://github.com/"+t.GitHubRepository+".git", t.Commit+":refs/heads/"+t.SourceBranch)
	case "create_pr":
		_, e = externalCommand(ctx, t, "gh", "native external create PR", "pr", "create", "--repo", t.GitHubRepository, "--head", t.SourceBranch, "--base", t.TargetBranch, "--title", "Governed delivery "+t.Task, "--body", fmt.Sprintf("Exact governed revision %s; generation %d, slice %d. Integration and acceptance require separate owner authority.", t.Commit, t.Generation, t.SliceGeneration))
	}
	if e != nil {
		return NativeExternalFacts{}, e
	}
	return ObserveNativeExternal(ctx, t)
}

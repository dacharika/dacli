// Package releasetrain promotes one configured integration branch through one
// resumable, checks-gated GitHub pull request. It never tags or publishes.
package releasetrain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/commandresult"
	"github.com/mlnomadpy/dacli/internal/gitx"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

var Commands = []clikit.Command{
	{Path: "release train", Brief: "Plan or resume one checks-gated source-to-target promotion PR; never tags or publishes", JSON: true, Mutates: true, Usage: "dacli release train --project SLUG --source BRANCH --target BRANCH (--dry-run | --apply) [--required-check NAME] [--required-artifact NAME] [--required-reviews N] [--merge]", Run: cmdReleaseTrain},
	{Path: "release train authority", Brief: "Persist or revoke the project's explicit authority for release-train merges", JSON: true, Mutates: true, Usage: "dacli release train authority --project SLUG (--allow-merge | --revoke-merge)", Run: cmdAuthority},
}

type TaskItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Issue  int    `json:"issue,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type Plan struct {
	Schema            string     `json:"schema"`
	ID                string     `json:"id"`
	Project           string     `json:"project"`
	Source            string     `json:"source"`
	Target            string     `json:"target"`
	SourceSHA         string     `json:"source_sha"`
	TargetSHA         string     `json:"target_sha"`
	Included          []TaskItem `json:"included_accepted_tasks"`
	Excluded          []TaskItem `json:"excluded_or_unverified_work"`
	PullRequests      []int      `json:"included_pull_requests"`
	RequiredChecks    []string   `json:"required_checks"`
	RequiredArtifacts []string   `json:"required_artifacts"`
	RequiredReviews   int        `json:"required_reviews"`
	Notes             string     `json:"release_pr_notes"`
}

type prObservation struct {
	Number     int    `json:"number"`
	URL        string `json:"url"`
	State      string `json:"state"`
	HeadRefOID string `json:"headRefOid"`
	MergeOID   string `json:"mergeCommit,omitempty"`
}
type promotionEvidence struct {
	Numbers []int
	Heads   map[string]bool
}

var (
	remoteSHA = func(root, branch string) (string, error) {
		out, err := gitx.RunNetwork(root, "ls-remote", "--exit-code", "origin", "refs/heads/"+branch)
		if err != nil {
			return "", err
		}
		fields := strings.Fields(out)
		if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
			return "", fmt.Errorf("remote returned no exact ref for %s", branch)
		}
		return fields[0], nil
	}
	observePromotionPRs = func(root, repo, source, target, sourceSHA, targetSHA string) (promotionEvidence, error) {
		compare, err := runGitHub(root, "api", "repos/"+repo+"/compare/"+targetSHA+"..."+sourceSHA+"?per_page=100", "--paginate", "--jq", ".commits[].sha")
		if err != nil {
			return promotionEvidence{}, err
		}
		delta := map[string]bool{}
		for _, sha := range strings.Fields(compare) {
			delta[sha] = true
		}
		out, err := runGitHub(root, "pr", "list", "--repo", repo, "--base", source, "--state", "merged", "--limit", "1000", "--json", "number,mergeCommit,headRefName")
		if err != nil {
			return promotionEvidence{}, err
		}
		var rows []struct {
			Number      int `json:"number"`
			MergeCommit struct {
				OID string `json:"oid"`
			} `json:"mergeCommit"`
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			return promotionEvidence{}, err
		}
		evidence := promotionEvidence{Heads: map[string]bool{}}
		for _, row := range rows {
			if delta[row.MergeCommit.OID] {
				evidence.Numbers = append(evidence.Numbers, row.Number)
				evidence.Heads[row.HeadRefName] = true
			}
		}
		sort.Ints(evidence.Numbers)
		return evidence, nil
	}
	// runGitHub uses gh directly; separate from git so tests prove outages fail
	// closed without depending on credentials.
	runGitHub = func(root string, args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "gh", args...)
		cmd.Dir = root
		out, err := commandresult.Run(cmd, commandresult.RunOptions{
			Operation: "observe release-train GitHub state", WorkspaceRoot: root,
			TimedOut: func() bool { return cctx.Err() == context.DeadlineExceeded },
		})
		return strings.TrimSpace(string(out)), err
	}
	fetchBranch = func(root, branch string) error {
		_, err := gitx.RunNetwork(root, "fetch", "--no-tags", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)
		return err
	}
	deleteSource = func(root, branch string) error {
		_, err := gitx.RunNetwork(root, "push", "origin", "--delete", "--", branch)
		return err
	}
	remoteBranchPresent = func(root, branch string) (bool, error) {
		out, err := gitx.RunNetwork(root, "ls-remote", "origin", "refs/heads/"+branch)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(out) == "" {
			return false, nil
		}
		fields := strings.Fields(out)
		return len(fields) == 2 && fields[1] == "refs/heads/"+branch, nil
	}
	crashAfter = func(string) error { return nil }
)

func cmdReleaseTrain(ctx *clikit.Ctx, args []string) error {
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("project", "source", "target", "dry-run", "apply", "required-check", "required-artifact", "required-reviews", "merge"); err != nil {
		return err
	}
	project, source, target := f.Get("project"), f.Get("source"), f.Get("target")
	if project == "" || source == "" || target == "" {
		return clikit.Usagef("--project, --source, and --target are required")
	}
	if source == target {
		return clikit.Usagef("--source and --target must differ")
	}
	if f.Bool("dry-run") == f.Bool("apply") {
		return clikit.Usagef("choose exactly one of --dry-run or --apply")
	}
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	p, err := store.LoadProject(w, project)
	if err != nil {
		return err
	}
	var known *store.ReleaseTrain
	if f.Bool("apply") {
		if tx, readErr := store.ReadReleaseTrain(w, project, source, target); readErr == nil {
			known = &tx
			if tx.CleanupComplete {
				currentSource, observeErr := remoteSHA(w.Root, source)
				if observeErr != nil || currentSource == tx.SourceSHA {
					return printTransaction(ctx, tx)
				}
			}
		}
	}
	if err := validateBranch(w.Root, source); err != nil {
		return clikit.Usagef("invalid --source: %v", err)
	}
	if err := validateBranch(w.Root, target); err != nil {
		return clikit.Usagef("invalid --target: %v", err)
	}
	checks := unique(f.All("required-check"))
	artifacts := unique(f.All("required-artifact"))
	reviews, err := optionalNonnegative(f.Get("required-reviews"))
	if err != nil {
		return err
	}
	plan, err := buildPlan(w, p, source, target, checks, artifacts, reviews, known)
	if err != nil {
		return fmt.Errorf("release-train observation failed closed: %w", err)
	}
	if f.Bool("dry-run") {
		return printPlan(ctx, plan)
	}
	if id.Grant != model.GrantRW {
		return clikit.Refusedf("release train apply needs an rw grant (yours is %s)", id.Grant)
	}
	return apply(ctx, w, p, plan, f.Bool("merge"))
}

func cmdAuthority(ctx *clikit.Ctx, args []string) error {
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("project", "allow-merge", "revoke-merge"); err != nil {
		return err
	}
	if f.Get("project") == "" || f.Bool("allow-merge") == f.Bool("revoke-merge") {
		return clikit.Usagef("--project and exactly one of --allow-merge or --revoke-merge are required")
	}
	w, id, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	if err := clikit.RequireRW(id, "configuring project release merge authority"); err != nil {
		return err
	}
	p, err := store.LoadProject(w, f.Get("project"))
	if err != nil {
		return err
	}
	p.Doc.Front.Set("release_merge_authority", strconv.FormatBool(f.Bool("allow-merge")))
	if err := store.SaveProject(p); err != nil {
		return err
	}
	if ctx.JSON {
		return clikit.EmitJSON(ctx, map[string]any{"project": p.Slug, "release_merge_authority": f.Bool("allow-merge")})
	}
	fmt.Fprintf(ctx.Stdout, "%s release merge authority: %t\n", p.Slug, f.Bool("allow-merge"))
	return nil
}

func validateBranch(root, branch string) error {
	out, err := gitx.Run(root, "check-ref-format", "--branch", branch)
	if err != nil {
		return fmt.Errorf("%q is not a valid exact branch name", branch)
	}
	if strings.TrimSpace(out) != branch {
		return fmt.Errorf("%q did not round-trip exactly", branch)
	}
	return nil
}

func optionalNonnegative(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, clikit.Usagef("--required-reviews must be a non-negative integer")
	}
	return n, nil
}

func buildPlan(w *workspace.Workspace, p *store.Project, source, target string, checks, artifacts []string, reviews int, known *store.ReleaseTrain) (Plan, error) {
	sourceSHA, err := remoteSHA(w.Root, source)
	if err != nil {
		if known == nil || (known.Phase != "merge-requested" && known.Phase != "target-fetched" && known.Phase != "landing-observed") {
			return Plan{}, fmt.Errorf("observe exact source branch %s: %w", source, err)
		}
		sourceSHA = known.SourceSHA
	}
	targetSHA, err := remoteSHA(w.Root, target)
	if err != nil {
		return Plan{}, fmt.Errorf("observe exact target branch %s: %w", target, err)
	}
	tasks, err := store.ListTasks(w, p.Slug, "")
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Schema: "release-train-plan/v1", Project: p.Slug, Source: source, Target: target, SourceSHA: sourceSHA, TargetSHA: targetSHA, RequiredChecks: checks, RequiredArtifacts: artifacts, RequiredReviews: reviews}
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return Plan{}, fmt.Errorf("project %s has no exact github_repo configured", p.Slug)
	}
	evidence, err := observePromotionPRs(w.Root, repo, source, target, sourceSHA, targetSHA)
	if err != nil {
		return Plan{}, fmt.Errorf("observe included pull requests for exact comparison %s...%s: %w", targetSHA, sourceSHA, err)
	}
	plan.PullRequests = evidence.Numbers
	for _, t := range tasks {
		item := TaskItem{ID: t.ID, Title: t.Title, Issue: githubIssue(t)}
		inDelta := evidence.Heads[store.TaskBranch(t)]
		if t.Status == model.StatusDone && allChecked(t) && inDelta {
			plan.Included = append(plan.Included, item)
		} else {
			item.Reason = "not accepted: task is " + string(t.Status)
			if t.Status == model.StatusDone && !allChecked(t) {
				item.Reason = "unverified: acceptance checklist is incomplete"
			} else if t.Status == model.StatusDone && !inDelta {
				item.Reason = "accepted but no task PR is present in the exact promotion delta"
			}
			plan.Excluded = append(plan.Excluded, item)
		}
	}
	plan.Notes = renderNotes(plan)
	h := sha256.Sum256([]byte(plan.Project + "\x00" + source + "\x00" + target + "\x00" + sourceSHA + "\x00" + targetSHA + "\x00" + plan.Notes))
	plan.ID = hex.EncodeToString(h[:12])
	return plan, nil
}

func allChecked(t *store.Task) bool {
	boxes := t.Acceptance()
	if len(boxes) == 0 {
		return false
	}
	for _, box := range boxes {
		if !box.Done {
			return false
		}
	}
	return true
}

var issueLine = regexp.MustCompile(`(?m)^\s*issue:\s*([0-9]+)\s*$`)

func githubIssue(t *store.Task) int {
	b, ok := t.Doc.Front.GetBlock("github")
	if !ok {
		return 0
	}
	m := issueLine.FindStringSubmatch(b)
	if len(m) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func renderNotes(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Promote `%s` (`%s`) to `%s` (`%s`).\n\n## Accepted work\n", p.Source, p.SourceSHA, p.Target, p.TargetSHA)
	for _, t := range p.Included {
		fmt.Fprintf(&b, "- %s — %s", t.ID, t.Title)
		if t.Issue > 0 {
			fmt.Fprintf(&b, " (#%d)", t.Issue)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n## Excluded work\n")
	for _, t := range p.Excluded {
		fmt.Fprintf(&b, "- %s — %s (%s)\n", t.ID, t.Title, t.Reason)
	}
	fmt.Fprintf(&b, "\nIncluded pull requests: %v\n", p.PullRequests)
	fmt.Fprintf(&b, "\nRequired checks: %s\nRequired artifacts: %s\nRequired approving reviews: %d\n", strings.Join(p.RequiredChecks, ", "), strings.Join(p.RequiredArtifacts, ", "), p.RequiredReviews)
	return b.String()
}

func printPlan(ctx *clikit.Ctx, plan Plan) error {
	if ctx.JSON {
		e := json.NewEncoder(ctx.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(plan)
	}
	fmt.Fprintf(ctx.Stdout, "release train %s (dry-run; no mutation)\n%s\n", plan.ID, plan.Notes)
	return nil
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func apply(ctx *clikit.Ctx, w *workspace.Workspace, p *store.Project, plan Plan, merge bool) error {
	tx, err := store.ReadReleaseTrain(w, plan.Project, plan.Source, plan.Target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return clikit.Refusedf("cannot resume release train: %v", err)
	}
	newGeneration := errors.Is(err, os.ErrNotExist) || (err == nil && tx.CleanupComplete && tx.SourceSHA != plan.SourceSHA)
	if newGeneration {
		var taskIDs []string
		for _, item := range plan.Included {
			taskIDs = append(taskIDs, item.ID)
		}
		tx = store.ReleaseTrain{Schema: store.ReleaseTrainSchema, Project: plan.Project, Source: plan.Source, Target: plan.Target, SourceSHA: plan.SourceSHA, TargetSHA: plan.TargetSHA, RequiredChecks: append([]string(nil), plan.RequiredChecks...), RequiredArtifacts: append([]string(nil), plan.RequiredArtifacts...), RequiredReviews: plan.RequiredReviews, IncludedTasks: taskIDs, Phase: "planned"}
		if err := store.WriteReleaseTrain(w, tx); err != nil {
			return err
		}
	} else if tx.CleanupComplete {
		return printTransaction(ctx, tx)
	} else if tx.SourceSHA != plan.SourceSHA || (tx.TargetSHA != plan.TargetSHA && tx.Phase != "merge-requested" && tx.Phase != "target-fetched" && tx.Phase != "landing-observed") {
		return clikit.Refusedf("release train changed since its durable plan (%s/%s -> %s/%s); finish or explicitly inspect the existing transaction before starting a new promotion", tx.SourceSHA, tx.TargetSHA, plan.SourceSHA, plan.TargetSHA)
	}
	// Gate configuration is part of the durable transaction. A restart with
	// fewer flags cannot weaken the checks/reviews the original plan recorded.
	plan.RequiredChecks = append([]string(nil), tx.RequiredChecks...)
	plan.RequiredArtifacts = append([]string(nil), tx.RequiredArtifacts...)
	plan.RequiredReviews = tx.RequiredReviews
	repo, _ := p.Doc.Front.Get("github_repo")
	if repo == "" {
		return clikit.Refusedf("project %s has no exact github_repo configured", p.Slug)
	}
	if tx.PullRequest == 0 {
		obs, err := findCanonicalPR(w.Root, repo, plan.Source, plan.Target, plan.SourceSHA)
		if err != nil {
			return fmt.Errorf("GitHub PR observation unknown; refusing to infer absence: %w", err)
		}
		if obs.Number == 0 {
			obs, err = createPR(w.Root, repo, plan)
			if err != nil {
				return fmt.Errorf("create promotion PR: %w", err)
			}
		}
		tx.PullRequest, tx.PullRequestURL, tx.Phase = obs.Number, obs.URL, "pr-persisted"
		if err := store.WriteReleaseTrain(w, tx); err != nil {
			return fmt.Errorf("persist canonical PR identity immediately: %w", err)
		}
		if err := crashAfter("pr-create"); err != nil {
			return err
		}
	}
	obs, checksReady, reviewsReady, external, err := observeGates(w.Root, repo, tx.PullRequest, plan)
	if err != nil {
		return fmt.Errorf("GitHub gate state unknown; refusing to merge: %w", err)
	}
	tx.ExternalVerification = append([]store.ExternalVerificationEvidence(nil), external...)
	if err := store.WriteReleaseTrain(w, tx); err != nil {
		return fmt.Errorf("persist exact-head external verification: %w", err)
	}
	if strings.EqualFold(obs.State, "MERGED") {
		return reconcileLanding(ctx, w, &tx)
	}
	if !checksReady || !reviewsReady {
		tx.Phase = "pending-gates"
		if err := store.WriteReleaseTrain(w, tx); err != nil {
			return err
		}
		if err := crashAfter("pending-ci"); err != nil {
			return err
		}
		return printTransaction(ctx, tx)
	}
	if !merge {
		tx.Phase = "ready-awaiting-authority"
		if err := store.WriteReleaseTrain(w, tx); err != nil {
			return err
		}
		return printTransaction(ctx, tx)
	}
	authorized, _ := p.Doc.Front.Get("release_merge_authority")
	if !strings.EqualFold(authorized, "true") {
		return clikit.Refusedf("--merge also requires project release_merge_authority: true; no transient flag can invent release authority")
	}
	if _, err := runGitHub(w.Root, "pr", "merge", strconv.Itoa(tx.PullRequest), "--repo", repo, "--merge", "--match-head-commit", plan.SourceSHA); err != nil {
		return fmt.Errorf("merge promotion PR: %w", err)
	}
	tx.Phase = "merge-requested"
	if err := store.WriteReleaseTrain(w, tx); err != nil {
		return err
	}
	if err := crashAfter("merge"); err != nil {
		return err
	}
	return reconcileLanding(ctx, w, &tx)
}

func findCanonicalPR(root, repo, source, target, sourceSHA string) (prObservation, error) {
	out, err := runGitHub(root, "pr", "list", "--repo", repo, "--head", source, "--base", target, "--state", "all", "--json", "number,url,state,headRefOid")
	if err != nil {
		return prObservation{}, err
	}
	var prs []prObservation
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return prObservation{}, err
	}
	var exact []prObservation
	for _, pr := range prs {
		if pr.HeadRefOID == sourceSHA {
			exact = append(exact, pr)
		}
	}
	if len(exact) > 1 {
		return prObservation{}, fmt.Errorf("multiple PRs match exact branch pair %s -> %s", source, target)
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	return prObservation{}, nil
}

func createPR(root, repo string, plan Plan) (prObservation, error) {
	title := fmt.Sprintf("Promote %s to %s (%s)", plan.Source, plan.Target, plan.SourceSHA[:min(12, len(plan.SourceSHA))])
	out, err := runGitHub(root, "pr", "create", "--repo", repo, "--head", plan.Source, "--base", plan.Target, "--title", title, "--body", plan.Notes)
	if err != nil {
		return prObservation{}, err
	}
	url := strings.TrimSpace(out)
	m := regexp.MustCompile(`/pull/([0-9]+)`).FindStringSubmatch(url)
	if len(m) != 2 {
		return prObservation{}, fmt.Errorf("GitHub created PR but returned no durable identity")
	}
	n, _ := strconv.Atoi(m[1])
	return prObservation{Number: n, URL: url, State: "OPEN", HeadRefOID: plan.SourceSHA}, nil
}

var observeExternalVerification = store.ObserveGitHubExternalVerification

func observeGates(root, repo string, number int, plan Plan) (prObservation, bool, bool, []store.ExternalVerificationEvidence, error) {
	out, err := runGitHub(root, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", "number,url,state,headRefOid,reviews")
	if err != nil {
		return prObservation{}, false, false, nil, err
	}
	var raw struct {
		Number                 int `json:"number"`
		URL, State, HeadRefOID string
		Reviews                []struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return prObservation{}, false, false, nil, err
	}
	if raw.Number != number {
		return prObservation{}, false, false, nil, clikit.Refusedf("canonical PR identity changed: requested #%d, observed #%d", number, raw.Number)
	}
	if raw.HeadRefOID == "" {
		return prObservation{}, false, false, nil, clikit.Refusedf("canonical PR head SHA is unobservable; refusing to merge")
	}
	if raw.HeadRefOID != plan.SourceSHA {
		return prObservation{}, false, false, nil, clikit.Refusedf("canonical PR head changed from reviewed source SHA %s to %s", plan.SourceSHA, raw.HeadRefOID)
	}
	state := strings.ToUpper(strings.TrimSpace(raw.State))
	if state != "OPEN" && state != "MERGED" {
		return prObservation{}, false, false, nil, clikit.Refusedf("canonical PR state %q cannot authorize merge; expected OPEN or MERGED", raw.State)
	}
	latest := map[string]string{}
	for _, r := range raw.Reviews {
		if r.Author.Login != "" {
			latest[r.Author.Login] = r.State
		}
	}
	approved := 0
	for _, state := range latest {
		if strings.EqualFold(state, "APPROVED") {
			approved++
		}
	}
	checksReady := len(plan.RequiredChecks) == 0
	if len(plan.RequiredChecks) > 0 {
		co, ce := runGitHub(root, "pr", "checks", strconv.Itoa(number), "--repo", repo, "--required", "--json", "name,state,bucket")
		if ce != nil {
			return prObservation{}, false, false, nil, ce
		}
		var rows []struct{ Name, State, Bucket string }
		if json.Unmarshal([]byte(co), &rows) != nil {
			return prObservation{}, false, false, nil, fmt.Errorf("decode required checks")
		}
		states := map[string]string{}
		for _, row := range rows {
			states[row.Name] = strings.ToUpper(row.Bucket + " " + row.State)
		}
		checksReady = true
		for _, check := range plan.RequiredChecks {
			if !strings.Contains(states[check], "PASS") && !strings.Contains(states[check], "SUCCESS") {
				checksReady = false
			}
		}
	}
	var external []store.ExternalVerificationEvidence
	if checksReady && (len(plan.RequiredChecks) > 0 || len(plan.RequiredArtifacts) > 0) {
		external, err = observeExternalVerification(root, repo, plan.SourceSHA, time.Now())
		if err != nil {
			return prObservation{}, false, false, nil, err
		}
		evidence := store.VerificationEvidence{External: external}
		if err := store.ValidateExternalVerification(evidence, store.ExternalVerificationPolicy{HeadSHA: plan.SourceSHA, RequiredChecks: plan.RequiredChecks, RequiredArtifacts: plan.RequiredArtifacts}); err != nil {
			checksReady = false
		}
	}
	return prObservation{Number: raw.Number, URL: raw.URL, State: state, HeadRefOID: raw.HeadRefOID}, checksReady, approved >= plan.RequiredReviews, external, nil
}

func reconcileLanding(ctx *clikit.Ctx, w *workspace.Workspace, tx *store.ReleaseTrain) error {
	if err := fetchBranch(w.Root, tx.Target); err != nil {
		return fmt.Errorf("fresh fetch of exact target %s failed: %w", tx.Target, err)
	}
	tx.Phase = "target-fetched"
	if err := store.WriteReleaseTrain(w, *tx); err != nil {
		return err
	}
	if err := crashAfter("fetch"); err != nil {
		return err
	}
	landed, err := gitx.IsAncestor(w.Root, tx.SourceSHA, "refs/remotes/origin/"+tx.Target)
	if err != nil {
		return fmt.Errorf("compare reviewed source %s to fetched target %s: %w", tx.SourceSHA, tx.Target, err)
	}
	if !landed {
		return clikit.Refusedf("fetched target %s does not contain reviewed source %s; cleanup refused (inspect git log %s..origin/%s)", tx.Target, tx.SourceSHA, tx.SourceSHA, tx.Target)
	}
	targetSHA, err := gitx.Run(w.Root, "rev-parse", "refs/remotes/origin/"+tx.Target)
	if err != nil {
		return err
	}
	tx.LandedTargetSHA, tx.Phase = strings.TrimSpace(targetSHA), "landing-observed"
	if err := store.WriteReleaseTrain(w, *tx); err != nil {
		return err
	}
	present, err := remoteBranchPresent(w.Root, tx.Source)
	if err != nil {
		return fmt.Errorf("landing observed but source cleanup state is unknown: %w", err)
	}
	if present {
		if err := deleteSource(w.Root, tx.Source); err != nil {
			return fmt.Errorf("landing observed but source cleanup failed safely: %w", err)
		}
	}
	if err := crashAfter("cleanup"); err != nil {
		return err
	}
	tx.ReconciledTasks = append([]string(nil), tx.IncludedTasks...)
	tx.CleanupComplete, tx.Phase = true, "complete"
	if err := store.WriteReleaseTrain(w, *tx); err != nil {
		return err
	}
	if err := crashAfter("complete"); err != nil {
		return err
	}
	return printTransaction(ctx, *tx)
}

func printTransaction(ctx *clikit.Ctx, tx store.ReleaseTrain) error {
	if ctx.JSON {
		e := json.NewEncoder(ctx.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(tx)
	}
	fmt.Fprintf(ctx.Stdout, "release train %s -> %s: %s (PR #%d)\n", tx.Source, tx.Target, tx.Phase, tx.PullRequest)
	return nil
}

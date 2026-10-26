package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/commandresult"
)

const GitHubRequiredCheckPolicySchema = "github-required-check-policy/v1"

type RequiredCheckSource struct {
	Kind              string `json:"kind"`
	RulesetID         int64  `json:"ruleset_id,omitempty"`
	RulesetSourceType string `json:"ruleset_source_type,omitempty"`
	RulesetSource     string `json:"ruleset_source,omitempty"`
}

type RequiredCheckRequirement struct {
	Name    string                `json:"name"`
	Sources []RequiredCheckSource `json:"sources"`
}

// GitHubRequiredCheckPolicy is the exact effective check policy observed for
// one target branch. Requirements retain every source so a configured check
// cannot conceal a stricter repository or organization rule (issue #1004).
type GitHubRequiredCheckPolicy struct {
	Schema       string                     `json:"schema"`
	Repository   string                     `json:"repository"`
	Branch       string                     `json:"branch"`
	ObservedAt   time.Time                  `json:"observed_at"`
	State        string                     `json:"state"` // observed or unobservable
	Requirements []RequiredCheckRequirement `json:"requirements"`
	Error        string                     `json:"error,omitempty"`
	Override     bool                       `json:"override,omitempty"`
}

func (p GitHubRequiredCheckPolicy) Names() []string {
	out := make([]string, 0, len(p.Requirements))
	for _, requirement := range p.Requirements {
		out = append(out, requirement.Name)
	}
	return out
}

var ObserveGitHubRequiredCheckPolicy = observeGitHubRequiredCheckPolicy

func observeGitHubRequiredCheckPolicy(root, repo, branch string, configured []string, now time.Time) (GitHubRequiredCheckPolicy, error) {
	policy := newRequiredCheckPolicy(repo, branch, configured, now)
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(branch) == "" {
		policy.State, policy.Error = "unobservable", "GitHub check policy requires repository and target branch"
		return policy, errors.New(policy.Error)
	}
	run := func(operation string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		base := []string{"api", "-H", "X-GitHub-Api-Version: 2022-11-28"}
		cmd := exec.CommandContext(ctx, "gh", append(base, args...)...)
		cmd.Dir = root
		return commandresult.Output(cmd, commandresult.RunOptions{Operation: operation, WorkspaceRoot: root, TimedOut: func() bool { return ctx.Err() == context.DeadlineExceeded }})
	}
	branchPath := url.PathEscape(branch)
	legacyRaw, legacyErr := run("observe legacy GitHub required checks", "repos/"+repo+"/branches/"+branchPath+"/protection/required_status_checks")
	if legacyErr != nil {
		switch {
		case githubRequiredChecksDisabled(legacyErr):
			if err := confirmDisabledGitHubRequiredChecks(run, repo, branch, branchPath); err != nil {
				policy.State, policy.Error = "unobservable", err.Error()
				return policy, fmt.Errorf("confirm disabled legacy required checks for %s/%s: %w", repo, branch, err)
			}
			// The dedicated endpoint's 404 is empty only after the exact visible
			// repository, branch, and retained parent protection were confirmed.
			legacyRaw = nil
		case githubBranchNotProtected(legacyErr):
			if err := confirmGitHubRepositoryBranch(run, repo, branch, branchPath); err != nil {
				policy.State, policy.Error = "unobservable", err.Error()
				return policy, fmt.Errorf("confirm unprotected branch for %s/%s: %w", repo, branch, err)
			}
			legacyRaw = nil
		default:
			policy.State, policy.Error = "unobservable", legacyErr.Error()
			return policy, fmt.Errorf("observe legacy required checks for %s/%s: %w", repo, branch, legacyErr)
		}
	}
	rulesRaw, err := run("observe applicable GitHub ruleset checks", "--paginate", "--slurp", "repos/"+repo+"/rules/branches/"+branchPath+"?per_page=100")
	if err != nil {
		policy.State, policy.Error = "unobservable", err.Error()
		return policy, fmt.Errorf("observe applicable rulesets for %s/%s: %w", repo, branch, err)
	}
	if err := mergeGitHubRequiredChecks(&policy, legacyRaw, rulesRaw); err != nil {
		policy.State, policy.Error = "unobservable", err.Error()
		return policy, err
	}
	policy.State = "observed"
	return policy, nil
}

func githubRequiredChecksDisabled(err error) bool {
	diagnostic, ok := commandresult.AsDiagnostic(err)
	if !ok {
		return false
	}
	text := strings.ToLower(diagnostic.StdoutTail + "\n" + diagnostic.StderrTail)
	return strings.Contains(text, "http 404") && strings.Contains(text, "required status checks not enabled")
}

func confirmDisabledGitHubRequiredChecks(run func(string, ...string) ([]byte, error), repo, branch, branchPath string) error {
	if err := confirmGitHubRepositoryBranch(run, repo, branch, branchPath); err != nil {
		return err
	}
	protectionRaw, err := run("confirm GitHub branch protection", "repos/"+repo+"/branches/"+branchPath+"/protection")
	if err != nil {
		return fmt.Errorf("parent branch protection is unobservable: %w", err)
	}
	var protection map[string]json.RawMessage
	if err := json.Unmarshal(protectionRaw, &protection); err != nil || protection == nil {
		return fmt.Errorf("parent branch protection is malformed")
	}
	// These fields are part of the complete branch-protection response. A
	// partial response cannot establish that omitted status checks are empty.
	for _, key := range []string{"enforce_admins", "required_pull_request_reviews", "restrictions"} {
		if _, ok := protection[key]; !ok {
			return fmt.Errorf("parent branch protection is partial: missing %s", key)
		}
	}
	var admins struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(protection["enforce_admins"], &admins); err != nil || admins.Enabled == nil {
		return fmt.Errorf("parent branch protection has malformed enforce_admins")
	}
	var reviews json.RawMessage = protection["required_pull_request_reviews"]
	if !jsonNull(reviews) {
		var value struct {
			DismissStaleReviews          *bool `json:"dismiss_stale_reviews"`
			RequiredApprovingReviewCount *int  `json:"required_approving_review_count"`
		}
		if err := json.Unmarshal(reviews, &value); err != nil {
			return fmt.Errorf("parent branch protection has malformed required_pull_request_reviews")
		}
		if value.DismissStaleReviews == nil || value.RequiredApprovingReviewCount == nil || *value.RequiredApprovingReviewCount < 0 {
			return fmt.Errorf("parent branch protection has partial required_pull_request_reviews")
		}
	}
	var restrictions json.RawMessage = protection["restrictions"]
	if !jsonNull(restrictions) {
		var value struct {
			Users []json.RawMessage `json:"users"`
			Teams []json.RawMessage `json:"teams"`
			Apps  []json.RawMessage `json:"apps"`
		}
		if err := json.Unmarshal(restrictions, &value); err != nil {
			return fmt.Errorf("parent branch protection has malformed restrictions")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(restrictions, &fields); err != nil || fields["users"] == nil || fields["teams"] == nil || fields["apps"] == nil || value.Users == nil || value.Teams == nil || value.Apps == nil {
			return fmt.Errorf("parent branch protection has partial restrictions")
		}
	}
	if raw, ok := protection["required_status_checks"]; ok && !jsonNull(raw) {
		var checks struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(raw, &checks); err != nil {
			return fmt.Errorf("parent branch protection has malformed required_status_checks")
		}
		return fmt.Errorf("inconsistent GitHub observations: required checks endpoint reported disabled but parent protection contains required_status_checks")
	}
	// Require evidence that another protection remains active. This prevents a
	// truncated/empty object from being interpreted as a deliberate disabled
	// check policy.
	retained := *admins.Enabled || !jsonNull(reviews) || !jsonNull(restrictions)
	for _, key := range []string{"required_linear_history", "allow_force_pushes", "allow_deletions", "block_creations", "required_conversation_resolution", "lock_branch", "allow_fork_syncing"} {
		if raw, ok := protection[key]; ok {
			var value struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &value); err != nil || value.Enabled == nil {
				return fmt.Errorf("parent branch protection has malformed %s", key)
			}
			retained = retained || *value.Enabled
		}
	}
	if !retained {
		return fmt.Errorf("parent branch protection does not confirm retained protections")
	}
	return nil
}

func confirmGitHubRepositoryBranch(run func(string, ...string) ([]byte, error), repo, branch, branchPath string) error {
	repoRaw, err := run("confirm GitHub repository visibility", "repos/"+repo)
	if err != nil {
		return fmt.Errorf("repository visibility is unobservable: %w", err)
	}
	var repository struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(repoRaw, &repository); err != nil || repository.FullName != repo {
		return fmt.Errorf("repository observation is malformed or does not match %s", repo)
	}
	branchRaw, err := run("confirm GitHub branch visibility", "repos/"+repo+"/branches/"+branchPath)
	if err != nil {
		return fmt.Errorf("branch visibility is unobservable: %w", err)
	}
	var observed struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(branchRaw, &observed); err != nil || observed.Name != branch || !validGitHubCommitSHA(observed.Commit.SHA) {
		return fmt.Errorf("branch observation is malformed or does not match %s", branch)
	}
	return nil
}

func validGitHubCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func jsonNull(raw json.RawMessage) bool {
	return len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null"
}

func newRequiredCheckPolicy(repo, branch string, configured []string, now time.Time) GitHubRequiredCheckPolicy {
	p := GitHubRequiredCheckPolicy{Schema: GitHubRequiredCheckPolicySchema, Repository: repo, Branch: branch, ObservedAt: now.UTC()}
	for _, name := range configured {
		addRequiredCheck(&p, name, RequiredCheckSource{Kind: "configured"})
	}
	return p
}

func mergeGitHubRequiredChecks(policy *GitHubRequiredCheckPolicy, legacyRaw, rulesRaw []byte) error {
	if len(strings.TrimSpace(string(legacyRaw))) > 0 {
		var legacy struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(legacyRaw, &legacy); err != nil {
			return fmt.Errorf("decode legacy branch protection required checks: %w", err)
		}
		for _, name := range legacy.Contexts {
			addRequiredCheck(policy, name, RequiredCheckSource{Kind: "legacy_branch_protection"})
		}
		for _, check := range legacy.Checks {
			addRequiredCheck(policy, check.Context, RequiredCheckSource{Kind: "legacy_branch_protection"})
		}
	}
	type branchRule struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
		RulesetID         int64  `json:"ruleset_id"`
		RulesetSourceType string `json:"ruleset_source_type"`
		RulesetSource     string `json:"ruleset_source"`
	}
	var pages [][]branchRule
	if err := json.Unmarshal(rulesRaw, &pages); err != nil {
		var rules []branchRule
		if directErr := json.Unmarshal(rulesRaw, &rules); directErr != nil {
			return fmt.Errorf("decode applicable GitHub ruleset checks: %w", err)
		}
		pages = [][]branchRule{rules}
	}
	for _, rules := range pages {
		for _, rule := range rules {
			if rule.Type != "required_status_checks" {
				continue
			}
			source := RequiredCheckSource{Kind: "ruleset", RulesetID: rule.RulesetID, RulesetSourceType: rule.RulesetSourceType, RulesetSource: rule.RulesetSource}
			for _, check := range rule.Parameters.RequiredStatusChecks {
				addRequiredCheck(policy, check.Context, source)
			}
		}
	}
	sort.Slice(policy.Requirements, func(i, j int) bool { return policy.Requirements[i].Name < policy.Requirements[j].Name })
	for i := range policy.Requirements {
		sort.Slice(policy.Requirements[i].Sources, func(a, b int) bool {
			x, y := policy.Requirements[i].Sources[a], policy.Requirements[i].Sources[b]
			return fmt.Sprintf("%s/%020d/%s/%s", x.Kind, x.RulesetID, x.RulesetSourceType, x.RulesetSource) < fmt.Sprintf("%s/%020d/%s/%s", y.Kind, y.RulesetID, y.RulesetSourceType, y.RulesetSource)
		})
	}
	return nil
}

func addRequiredCheck(policy *GitHubRequiredCheckPolicy, name string, source RequiredCheckSource) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for i := range policy.Requirements {
		if policy.Requirements[i].Name != name {
			continue
		}
		for _, existing := range policy.Requirements[i].Sources {
			if existing == source {
				return
			}
		}
		policy.Requirements[i].Sources = append(policy.Requirements[i].Sources, source)
		return
	}
	policy.Requirements = append(policy.Requirements, RequiredCheckRequirement{Name: name, Sources: []RequiredCheckSource{source}})
}

func githubBranchNotProtected(err error) bool {
	diagnostic, ok := commandresult.AsDiagnostic(err)
	if !ok {
		return false
	}
	text := strings.ToLower(diagnostic.StdoutTail + "\n" + diagnostic.StderrTail)
	return strings.Contains(text, "http 404") && strings.Contains(text, "branch not protected")
}

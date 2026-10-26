package store

import (
	"bytes"
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
			_, observedBranch, err := confirmGitHubRepositoryBranch(run, repo, branch, branchPath)
			if err != nil {
				policy.State, policy.Error = "unobservable", err.Error()
				return policy, fmt.Errorf("confirm unprotected branch for %s/%s: %w", repo, branch, err)
			}
			if *observedBranch.Protected {
				err := fmt.Errorf("inconsistent GitHub observations: required checks endpoint reported branch not protected but branch is protected")
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
	repository, observedBranch, err := confirmGitHubRepositoryBranch(run, repo, branch, branchPath)
	if err != nil {
		return err
	}
	if observedBranch.Protected == nil || !*observedBranch.Protected {
		return fmt.Errorf("branch observation does not confirm protected=true")
	}
	expectedProtectionURL := strings.TrimSuffix(repository.URL, "/") + "/branches/" + branchPath + "/protection"
	if observedBranch.ProtectionURL != expectedProtectionURL {
		return fmt.Errorf("branch protection URL does not match the exact repository and branch")
	}
	protectionRaw, err := run("confirm GitHub branch protection", "repos/"+repo+"/branches/"+branchPath+"/protection")
	if err != nil {
		return fmt.Errorf("parent branch protection is unobservable: %w", err)
	}
	var protection map[string]json.RawMessage
	if err := json.Unmarshal(protectionRaw, &protection); err != nil || protection == nil {
		return fmt.Errorf("parent branch protection is malformed")
	}
	var parent struct {
		URL                            string          `json:"url"`
		RequiredSignatures             json.RawMessage `json:"required_signatures"`
		EnforceAdmins                  json.RawMessage `json:"enforce_admins"`
		RequiredLinearHistory          json.RawMessage `json:"required_linear_history"`
		AllowForcePushes               json.RawMessage `json:"allow_force_pushes"`
		AllowDeletions                 json.RawMessage `json:"allow_deletions"`
		BlockCreations                 json.RawMessage `json:"block_creations"`
		RequiredConversationResolution json.RawMessage `json:"required_conversation_resolution"`
		LockBranch                     json.RawMessage `json:"lock_branch"`
		AllowForkSyncing               json.RawMessage `json:"allow_fork_syncing"`
		RequiredPullRequestReviews     json.RawMessage `json:"required_pull_request_reviews"`
		Restrictions                   json.RawMessage `json:"restrictions"`
		RequiredStatusChecks           json.RawMessage `json:"required_status_checks"`
	}
	if err := json.Unmarshal(protectionRaw, &parent); err != nil || parent.URL != expectedProtectionURL {
		return fmt.Errorf("parent branch protection URL is malformed or does not match the exact repository and branch")
	}
	// GitHub's complete parent response carries these fields even when their
	// enabled values are false. Review and push restrictions are optional in the
	// API response, so validate them only when present.
	otherProtectionFields := []struct {
		name string
		raw  json.RawMessage
	}{
		{"required_signatures", parent.RequiredSignatures},
		{"enforce_admins", parent.EnforceAdmins},
		{"required_linear_history", parent.RequiredLinearHistory},
		{"allow_force_pushes", parent.AllowForcePushes},
		{"allow_deletions", parent.AllowDeletions},
		{"block_creations", parent.BlockCreations},
		{"required_conversation_resolution", parent.RequiredConversationResolution},
		{"lock_branch", parent.LockBranch},
		{"allow_fork_syncing", parent.AllowForkSyncing},
	}
	retained := false
	for _, field := range otherProtectionFields {
		if len(field.raw) == 0 || jsonNull(field.raw) {
			return fmt.Errorf("parent branch protection is partial: missing %s", field.name)
		}
		var value struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(field.raw, &value); err != nil || value.Enabled == nil {
			return fmt.Errorf("parent branch protection has malformed %s", field.name)
		}
		retained = retained || *value.Enabled
	}
	if !jsonNull(parent.RequiredPullRequestReviews) {
		var value struct {
			DismissStaleReviews          *bool `json:"dismiss_stale_reviews"`
			RequiredApprovingReviewCount *int  `json:"required_approving_review_count"`
		}
		if err := json.Unmarshal(parent.RequiredPullRequestReviews, &value); err != nil {
			return fmt.Errorf("parent branch protection has malformed required_pull_request_reviews")
		}
		if value.DismissStaleReviews == nil || value.RequiredApprovingReviewCount == nil || *value.RequiredApprovingReviewCount < 0 {
			return fmt.Errorf("parent branch protection has partial required_pull_request_reviews")
		}
		retained = true
	}
	if !jsonNull(parent.Restrictions) {
		var value struct {
			Users []json.RawMessage `json:"users"`
			Teams []json.RawMessage `json:"teams"`
			Apps  []json.RawMessage `json:"apps"`
		}
		if err := json.Unmarshal(parent.Restrictions, &value); err != nil {
			return fmt.Errorf("parent branch protection has malformed restrictions")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(parent.Restrictions, &fields); err != nil || fields["users"] == nil || fields["teams"] == nil || fields["apps"] == nil || value.Users == nil || value.Teams == nil || value.Apps == nil {
			return fmt.Errorf("parent branch protection has partial restrictions")
		}
		retained = true
	}
	if !jsonNull(parent.RequiredStatusChecks) {
		var checks struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(parent.RequiredStatusChecks, &checks); err != nil {
			return fmt.Errorf("parent branch protection has malformed required_status_checks")
		}
		return fmt.Errorf("inconsistent GitHub observations: required checks endpoint reported disabled but parent protection contains required_status_checks")
	}
	if !retained {
		return fmt.Errorf("parent branch protection does not confirm retained protections")
	}
	return nil
}

type githubRepositoryObservation struct {
	FullName string `json:"full_name"`
	URL      string `json:"url"`
}

type githubBranchObservation struct {
	Name          string `json:"name"`
	Protected     *bool  `json:"protected"`
	ProtectionURL string `json:"protection_url"`
	Commit        struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func confirmGitHubRepositoryBranch(run func(string, ...string) ([]byte, error), repo, branch, branchPath string) (githubRepositoryObservation, githubBranchObservation, error) {
	repoRaw, err := run("confirm GitHub repository visibility", "repos/"+repo)
	if err != nil {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("repository visibility is unobservable: %w", err)
	}
	var repository githubRepositoryObservation
	if err := json.Unmarshal(repoRaw, &repository); err != nil || repository.FullName != repo {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("repository observation is malformed or does not match %s", repo)
	}
	repoURL, err := url.Parse(repository.URL)
	if err != nil || repoURL.Scheme != "https" || repoURL.Host == "" || repoURL.RawQuery != "" || repoURL.Fragment != "" || !strings.HasSuffix(strings.TrimSuffix(repoURL.Path, "/"), "/repos/"+repo) {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("repository API URL is malformed or does not match %s", repo)
	}
	branchRaw, err := run("confirm GitHub branch visibility", "repos/"+repo+"/branches/"+branchPath)
	if err != nil {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("branch visibility is unobservable: %w", err)
	}
	var observed githubBranchObservation
	if err := json.Unmarshal(branchRaw, &observed); err != nil || observed.Name != branch || !validGitHubCommitSHA(observed.Commit.SHA) {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("branch observation is malformed or does not match %s", branch)
	}
	if observed.Protected == nil {
		return githubRepositoryObservation{}, githubBranchObservation{}, fmt.Errorf("branch observation is partial: missing protected")
	}
	return repository, observed, nil
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
		var legacy map[string]json.RawMessage
		if err := json.Unmarshal(legacyRaw, &legacy); err != nil {
			return fmt.Errorf("decode legacy branch protection required checks: %w", err)
		}
		if legacy == nil {
			return fmt.Errorf("decode legacy branch protection required checks: response must be an object")
		}
		var contexts []string
		if raw, ok := legacy["contexts"]; !ok || json.Unmarshal(raw, &contexts) != nil || contexts == nil {
			return fmt.Errorf("decode legacy branch protection required checks: contexts is missing or malformed")
		}
		var checkRecords []json.RawMessage
		if raw, ok := legacy["checks"]; !ok || json.Unmarshal(raw, &checkRecords) != nil || checkRecords == nil {
			return fmt.Errorf("decode legacy branch protection required checks: checks is missing or malformed")
		}
		for _, name := range contexts {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("decode legacy branch protection required checks: context is empty")
			}
			addRequiredCheck(policy, name, RequiredCheckSource{Kind: "legacy_branch_protection"})
		}
		for checkIndex, raw := range checkRecords {
			var check struct {
				Context string `json:"context"`
			}
			if err := json.Unmarshal(raw, &check); err != nil || strings.TrimSpace(check.Context) == "" {
				return fmt.Errorf("decode legacy branch protection required checks: check %d is malformed", checkIndex+1)
			}
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
	pages, err := decodeApplicableGitHubRulesets(rulesRaw)
	if err != nil {
		return err
	}
	for _, rules := range pages {
		for _, raw := range rules {
			var rule branchRule
			if err := json.Unmarshal(raw, &rule); err != nil {
				return fmt.Errorf("decode applicable GitHub ruleset checks: %w", err)
			}
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

func decodeApplicableGitHubRulesets(raw []byte) ([][]json.RawMessage, error) {
	var top []json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, fmt.Errorf("decode applicable GitHub ruleset checks: response must be a complete JSON page array")
	}
	pages := [][]json.RawMessage{}
	if len(top) > 0 && len(bytes.TrimSpace(top[0])) > 0 && bytes.TrimSpace(top[0])[0] == '[' {
		for pageIndex, pageRaw := range top {
			var page []json.RawMessage
			if err := json.Unmarshal(pageRaw, &page); err != nil || page == nil {
				return nil, fmt.Errorf("decode applicable GitHub ruleset checks: page %d is malformed or null", pageIndex+1)
			}
			if err := validateGitHubBranchRules(page, pageIndex+1); err != nil {
				return nil, err
			}
			pages = append(pages, page)
		}
		return pages, nil
	}
	if err := validateGitHubBranchRules(top, 1); err != nil {
		return nil, err
	}
	return [][]json.RawMessage{top}, nil
}

func validateGitHubBranchRules(rules []json.RawMessage, page int) error {
	for ruleIndex, raw := range rules {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return fmt.Errorf("decode applicable GitHub ruleset checks: page %d rule %d is malformed or null", page, ruleIndex+1)
		}
		var ruleType string
		if err := json.Unmarshal(object["type"], &ruleType); err != nil || strings.TrimSpace(ruleType) == "" {
			return fmt.Errorf("decode applicable GitHub ruleset checks: page %d rule %d has no valid type", page, ruleIndex+1)
		}
		var parameters map[string]json.RawMessage
		if err := json.Unmarshal(object["parameters"], &parameters); err != nil || parameters == nil {
			return fmt.Errorf("decode applicable GitHub ruleset checks: page %d rule %d has malformed parameters", page, ruleIndex+1)
		}
		if ruleType != "required_status_checks" {
			continue
		}
		var checks []json.RawMessage
		if err := json.Unmarshal(parameters["required_status_checks"], &checks); err != nil || checks == nil {
			return fmt.Errorf("decode applicable GitHub ruleset checks: page %d required_status_checks rule %d has malformed checks", page, ruleIndex+1)
		}
		for checkIndex, checkRaw := range checks {
			var check struct {
				Context string `json:"context"`
			}
			if err := json.Unmarshal(checkRaw, &check); err != nil || strings.TrimSpace(check.Context) == "" {
				return fmt.Errorf("decode applicable GitHub ruleset checks: page %d required_status_checks rule %d check %d is malformed", page, ruleIndex+1, checkIndex+1)
			}
		}
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

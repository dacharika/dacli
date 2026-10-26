package store

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func installRequiredCheckFakeGH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	// The disabled fixtures below mirror the sanitized live response: status
	// checks are absent from the parent object while signature/admin/history
	// protections remain explicitly represented.
	script := `#!/bin/sh
for arg do endpoint=$arg; done
printf '%s\n' "$endpoint" >> "$GH_CALL_LOG"
case "$endpoint" in
  repos/owner/repo)
    case "$GH_FIXTURE" in concealed-repo) echo 'gh: HTTP 404: Not Found' >&2; exit 1;; esac
    case "$GH_FIXTURE" in wrong-repository-url) printf '%s\n' '{"full_name":"owner/repo","url":"https://api.github.com/repos/other/repo"}'; exit 0;; esac
    printf '%s\n' '{"full_name":"owner/repo","url":"https://api.github.com/repos/owner/repo"}'
    ;;
  repos/owner/repo/branches/main)
    case "$GH_FIXTURE" in
      missing-branch) echo 'gh: HTTP 404: Not Found' >&2; exit 1;;
      wrong-branch) printf '%s\n' '{"name":"other","commit":{"sha":"0123456789012345678901234567890123456789"},"protected":true}'; exit 0;;
      partial-branch) printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0;;
      unprotected) printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"},"protected":false}'; exit 0;;
      wrong-protection-url) printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"},"protected":true,"protection_url":"https://api.github.com/repos/other/repo/branches/main/protection"}'; exit 0;;
    esac
    printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"},"protected":true,"protection_url":"https://api.github.com/repos/owner/repo/branches/main/protection"}'
    ;;
	  repos/owner/repo/branches/main/protection/required_status_checks)
	    case "$GH_FIXTURE" in
	  disabled|disabled-absent|inherited|partial-branch|wrong-protection-url|partial-protection|partial-reviews|malformed-protection|null-protection|no-retained|inconsistent|wrong-repository-url|wrong-parent-url)
        echo 'gh: HTTP 404: Required status checks not enabled for this branch.' >&2; exit 1;;
      generic-404|concealed-repo|missing-branch|wrong-branch)
        echo 'gh: HTTP 404: Not Found' >&2; exit 1;;
      unprotected)
        echo 'gh: HTTP 404: Branch not protected' >&2; exit 1;;
      permission)
        echo 'gh: HTTP 403: Resource not accessible by integration' >&2; exit 1;;
	  unknown-rule|parameterless-only) printf '%s\n' '{"contexts":[],"checks":[]}'; exit 0;;
      legacy-partial) printf '%s\n' '{"contexts":[]}'; exit 0;;
      legacy-null) printf '%s\n' 'null'; exit 0;;
      *) printf '%s\n' '{"contexts":["legacy-ci"],"checks":[{"context":"app-ci"}]}' ;;
    esac
    ;;
  repos/owner/repo/branches/main/protection)
    case "$GH_FIXTURE" in
      partial-protection) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"enabled":false},"enforce_admins":{"enabled":false},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false}}';;
	      partial-reviews) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"enabled":false},"enforce_admins":{"enabled":true},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false},"required_pull_request_reviews":{"required_approving_review_count":1}}';;
      malformed-protection) printf '%s\n' '{';;
      null-protection) printf '%s\n' 'null';;
	      wrong-parent-url) printf '%s\n' '{"url":"https://api.github.com/repos/other/repo/branches/main/protection","required_signatures":{"enabled":false},"enforce_admins":{"enabled":true},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false}}';;
      no-retained) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"enabled":false},"enforce_admins":{"enabled":false},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false}}';;
      inconsistent) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"enabled":false},"enforce_admins":{"enabled":true},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false},"required_status_checks":{"contexts":["ci"],"checks":[]} }';;
	      disabled-absent) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"url":"https://api.github.com/repos/owner/repo/branches/main/protection/required_signatures","enabled":false},"enforce_admins":{"url":"https://api.github.com/repos/owner/repo/branches/main/protection/enforce_admins","enabled":true},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false}}';;
	      *) printf '%s\n' '{"url":"https://api.github.com/repos/owner/repo/branches/main/protection","required_signatures":{"url":"https://api.github.com/repos/owner/repo/branches/main/protection/required_signatures","enabled":false},"enforce_admins":{"url":"https://api.github.com/repos/owner/repo/branches/main/protection/enforce_admins","enabled":true},"required_linear_history":{"enabled":false},"allow_force_pushes":{"enabled":false},"allow_deletions":{"enabled":false},"block_creations":{"enabled":false},"required_conversation_resolution":{"enabled":false},"lock_branch":{"enabled":false},"allow_fork_syncing":{"enabled":false}}';;
    esac
    ;;
	  repos/owner/repo/rules/branches/main?per_page=100)
	    case "$GH_FIXTURE" in
	      rules-failure) echo 'gh: HTTP 500: Internal Server Error' >&2; exit 1;;
	      rules-later-page-failure) printf '%s\n' '[[{"type":"future_rule"}]]'; echo 'gh: HTTP 500: later page failed' >&2; exit 1;;
	      rules-malformed) printf '%s\n' '{'; exit 0;;
	      rules-null) printf '%s\n' 'null'; exit 0;;
	      rules-direct-null-rule) printf '%s\n' '[null]'; exit 0;;
	      rules-null-page) printf '%s\n' '[[{"type":"future_rule"}],null]'; exit 0;;
	      rules-null-rule) printf '%s\n' '[[null]]'; exit 0;;
	      rules-null-noncheck-parameters) printf '%s\n' '[[{"type":"creation","parameters":null}]]'; exit 0;;
	      rules-null-parameters) printf '%s\n' '[[{"type":"required_status_checks","parameters":null}]]'; exit 0;;
	      rules-missing-parameters) printf '%s\n' '[[{"type":"required_status_checks"}]]'; exit 0;;
	      rules-missing-checks) printf '%s\n' '[[{"type":"required_status_checks","parameters":{}}]]'; exit 0;;
	      rules-null-checks) printf '%s\n' '[[{"type":"required_status_checks","parameters":{"required_status_checks":null}}]]'; exit 0;;
	      rules-malformed-check) printf '%s\n' '[[{"type":"required_status_checks","parameters":{"required_status_checks":[{}]}}]]'; exit 0;;
	      unknown-rule) printf '%s\n' '[[{"type":"future_branch_rule"}]]'; exit 0;;
	      parameterless-only) printf '%s\n' '[[{"type":"creation"},{"type":"deletion"},{"type":"required_linear_history"},{"type":"required_signatures"},{"type":"non_fast_forward"}]]'; exit 0;;
	    esac
    if [ "$GH_FIXTURE" = inherited ]; then
	      printf '%s\n' '[[{"type":"creation"},{"type":"deletion"},{"type":"required_linear_history"},{"type":"required_signatures"},{"type":"non_fast_forward"}],[{"type":"required_status_checks","ruleset_id":31,"ruleset_source_type":"Organization","ruleset_source":"owner","parameters":{"required_status_checks":[{"context":"org-ci"}]}}],[{"type":"required_status_checks","ruleset_id":32,"ruleset_source_type":"Repository","ruleset_source":"owner/repo","parameters":{"required_status_checks":[{"context":"repo-ci"}]}}]]'
	    elif [ "$GH_FIXTURE" = parameterless-only ]; then
	      printf '%s\n' '[[{"type":"creation"},{"type":"deletion"},{"type":"required_linear_history"},{"type":"required_signatures"},{"type":"non_fast_forward"}]]'
    else
      printf '%s\n' '[[]]'
    fi
    ;;
  *)
    echo "unexpected gh endpoint: $endpoint" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "gh-calls.log")
	t.Setenv("GH_CALL_LOG", log)
	t.Cleanup(func() { _ = os.Remove(log) })
	return log
}

func observeRequiredCheckFixture(t *testing.T, fixture string, configured []string) (GitHubRequiredCheckPolicy, error, string) {
	t.Helper()
	log := installRequiredCheckFakeGH(t)
	t.Setenv("GH_FIXTURE", fixture)
	policy, err := observeGitHubRequiredCheckPolicy(t.TempDir(), "owner/repo", "main", configured, time.Unix(10, 0))
	calls, _ := os.ReadFile(log)
	return policy, err, string(calls)
}

func TestObserveGitHubRequiredCheckPolicyConfirmsExplicitlyDisabledLegacyChecks(t *testing.T) {
	for _, fixture := range []string{"disabled", "disabled-absent"} {
		t.Run(fixture, func(t *testing.T) {
			policy, err, calls := observeRequiredCheckFixture(t, fixture, []string{"configured-ci"})
			if err != nil {
				t.Fatalf("explicitly disabled checks were unobservable: %v", err)
			}
			if policy.State != "observed" || !reflect.DeepEqual(policy.Names(), []string{"configured-ci"}) {
				t.Fatalf("policy = %+v, want observed configured check only", policy)
			}
			for _, endpoint := range []string{"repos/owner/repo\n", "repos/owner/repo/branches/main\n", "repos/owner/repo/branches/main/protection\n", "repos/owner/repo/rules/branches/main?per_page=100\n"} {
				if !strings.Contains(calls, endpoint) {
					t.Errorf("missing positive observation %q in calls %q", strings.TrimSpace(endpoint), calls)
				}
			}
		})
	}
}

func TestObserveGitHubRequiredCheckPolicyConfirmsUnprotectedBranch(t *testing.T) {
	policy, err, calls := observeRequiredCheckFixture(t, "unprotected", nil)
	if err != nil {
		t.Fatalf("visible unprotected branch was unobservable: %v", err)
	}
	if policy.State != "observed" || len(policy.Requirements) != 0 || !strings.Contains(calls, "repos/owner/repo/branches/main\n") {
		t.Fatalf("unprotected branch policy = %+v, calls=%q", policy, calls)
	}
}

func TestObserveGitHubRequiredCheckPolicyKeepsEnabledAndInheritedChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		fixture string
		want    []string
	}{
		"enabled legacy":                    {"enabled", []string{"app-ci", "legacy-ci"}},
		"inherited and repository rulesets": {"inherited", []string{"org-ci", "repo-ci"}},
	} {
		t.Run(name, func(t *testing.T) {
			policy, err, calls := observeRequiredCheckFixture(t, tc.fixture, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := policy.Names(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("required checks = %v, want %v", got, tc.want)
			}
			if tc.fixture == "inherited" && (!strings.Contains(calls, "protection/required_status_checks\n") || !strings.Contains(calls, "branches/main/protection\n")) {
				t.Fatalf("inherited checks were not observed alongside the disabled legacy endpoint: %q", calls)
			}
		})
	}
}

func TestObserveGitHubRequiredCheckPolicyAllowsParameterlessRules(t *testing.T) {
	policy, err, _ := observeRequiredCheckFixture(t, "parameterless-only", nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.State != "observed" || len(policy.Requirements) != 0 {
		t.Fatalf("parameterless rules created required status checks: %+v", policy)
	}
}

func TestObserveGitHubRequiredCheckPolicyIgnoresWellFormedFutureRules(t *testing.T) {
	policy, err, _ := observeRequiredCheckFixture(t, "unknown-rule", nil)
	if err != nil {
		t.Fatal(err)
	}
	if policy.State != "observed" || len(policy.Requirements) != 0 {
		t.Fatalf("unknown well-formed rule changed the required checks: %+v", policy)
	}
}

func TestObserveGitHubRequiredCheckPolicyFailsClosedOnUnconfirmedDisabledChecks(t *testing.T) {
	for _, fixture := range []string{"generic-404", "permission", "concealed-repo", "wrong-repository-url", "missing-branch", "wrong-branch", "partial-branch", "wrong-protection-url", "wrong-parent-url", "partial-protection", "partial-reviews", "malformed-protection", "null-protection", "no-retained", "inconsistent", "legacy-partial", "legacy-null", "rules-failure", "rules-later-page-failure", "rules-malformed", "rules-null", "rules-direct-null-rule", "rules-null-page", "rules-null-rule", "rules-null-noncheck-parameters", "rules-null-parameters", "rules-missing-parameters", "rules-missing-checks", "rules-null-checks", "rules-malformed-check"} {
		t.Run(fixture, func(t *testing.T) {
			policy, err, _ := observeRequiredCheckFixture(t, fixture, nil)
			if err == nil || policy.State != "unobservable" {
				t.Fatalf("fixture %s was accepted: policy=%+v err=%v", fixture, policy, err)
			}
		})
	}
}

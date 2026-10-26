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
	script := `#!/bin/sh
for arg do endpoint=$arg; done
printf '%s\n' "$endpoint" >> "$GH_CALL_LOG"
case "$endpoint" in
  repos/owner/repo)
    case "$GH_FIXTURE" in concealed-repo) echo 'gh: HTTP 404: Not Found' >&2; exit 1;; esac
    printf '%s\n' '{"full_name":"owner/repo"}'
    ;;
  repos/owner/repo/branches/main)
	    case "$GH_FIXTURE" in missing-branch) echo 'gh: HTTP 404: Not Found' >&2; exit 1;; wrong-branch) printf '%s\n' '{"name":"other","commit":{"sha":"0123456789012345678901234567890123456789"}}'; exit 0;; esac
	    printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"}}'
    ;;
  repos/owner/repo/branches/main/protection/required_status_checks)
    case "$GH_FIXTURE" in
	  inherited) printf '%s\n' '{"contexts":[],"checks":[]}';;
	      disabled|disabled-absent|partial-protection|partial-reviews|malformed-protection|null-protection|no-retained|inconsistent)
        echo 'gh: HTTP 404: Required status checks not enabled for this branch.' >&2; exit 1;;
      generic-404|concealed-repo|missing-branch|wrong-branch)
        echo 'gh: HTTP 404: Not Found' >&2; exit 1;;
      unprotected)
        echo 'gh: HTTP 404: Branch not protected' >&2; exit 1;;
      permission)
        echo 'gh: HTTP 403: Resource not accessible by integration' >&2; exit 1;;
      *) printf '%s\n' '{"contexts":["legacy-ci"],"checks":[{"context":"app-ci"}]}' ;;
    esac
    ;;
  repos/owner/repo/branches/main/protection)
    case "$GH_FIXTURE" in
      partial-protection) printf '%s\n' '{"enforce_admins":{"enabled":false}}';;
	      partial-reviews) printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":{"required_approving_review_count":1},"restrictions":null,"required_status_checks":null}';;
      malformed-protection) printf '%s\n' '{';;
      null-protection) printf '%s\n' 'null';;
      no-retained) printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":null,"restrictions":null,"required_status_checks":null}';;
	      inconsistent) printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":{"dismiss_stale_reviews":true,"required_approving_review_count":1},"restrictions":null,"required_status_checks":{"contexts":["ci"],"checks":[]} }';;
	      disabled-absent) printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":{"dismiss_stale_reviews":true,"required_approving_review_count":1},"restrictions":null,"required_conversation_resolution":{"enabled":true}}';;
	      *) printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":{"dismiss_stale_reviews":true,"required_approving_review_count":1},"restrictions":null,"required_status_checks":null,"required_conversation_resolution":{"enabled":true}}';;
    esac
    ;;
  repos/owner/repo/rules/branches/main?per_page=100)
	    case "$GH_FIXTURE" in rules-failure) echo 'gh: HTTP 500: Internal Server Error' >&2; exit 1;; rules-malformed) printf '%s\n' '{'; exit 0;; esac
    if [ "$GH_FIXTURE" = inherited ]; then
      printf '%s\n' '[[{"type":"required_status_checks","ruleset_id":31,"ruleset_source_type":"Organization","ruleset_source":"owner","parameters":{"required_status_checks":[{"context":"org-ci"}]}}],[{"type":"required_status_checks","ruleset_id":32,"ruleset_source_type":"Repository","ruleset_source":"owner/repo","parameters":{"required_status_checks":[{"context":"repo-ci"}]}}]]'
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
			policy, err, _ := observeRequiredCheckFixture(t, tc.fixture, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := policy.Names(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("required checks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestObserveGitHubRequiredCheckPolicyFailsClosedOnUnconfirmedDisabledChecks(t *testing.T) {
	for _, fixture := range []string{"generic-404", "permission", "concealed-repo", "missing-branch", "wrong-branch", "partial-protection", "partial-reviews", "malformed-protection", "null-protection", "no-retained", "inconsistent", "rules-failure", "rules-malformed"} {
		t.Run(fixture, func(t *testing.T) {
			policy, err, _ := observeRequiredCheckFixture(t, fixture, nil)
			if err == nil || policy.State != "unobservable" {
				t.Fatalf("fixture %s was accepted: policy=%+v err=%v", fixture, policy, err)
			}
		})
	}
}

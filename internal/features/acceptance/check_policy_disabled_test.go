package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlnomadpy/dacli/internal/store"
)

func installDisabledPolicyAcceptanceGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
for arg do endpoint=$arg; done
if [ "$1" = run ]; then printf '%s\n' '[]'; exit 0; fi
case "$endpoint" in
  repos/owner/repo)
    printf '%s\n' '{"full_name":"owner/repo"}' ;;
  repos/owner/repo/branches/main)
	    printf '%s\n' '{"name":"main","commit":{"sha":"0123456789012345678901234567890123456789"}}' ;;
  repos/owner/repo/branches/main/protection/required_status_checks)
    echo 'gh: HTTP 404: Required status checks not enabled for this branch.' >&2; exit 1 ;;
  repos/owner/repo/branches/main/protection)
	    printf '%s\n' '{"enforce_admins":{"enabled":false},"required_pull_request_reviews":{"dismiss_stale_reviews":true,"required_approving_review_count":1},"restrictions":null,"required_status_checks":null,"required_conversation_resolution":{"enabled":true}}' ;;
  repos/owner/repo/rules/branches/main?per_page=100)
    printf '%s\n' '[[]]' ;;
  repos/owner/repo/commits/*/check-runs)
    printf '%s\n' '{"check_runs":[]}' ;;
  repos/owner/repo/actions/artifacts?per_page=100)
    printf '%s\n' '{"artifacts":[]}' ;;
  *) echo "unexpected gh request: $endpoint" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAcceptWithExplicitlyDisabledLegacyChecksNeedsNoPolicyOverride(t *testing.T) {
	ctx, w, root := evidenceEnv(t)
	task := mkTask(t, w, "manual only checks")
	project, err := store.LoadProject(w, task.Project)
	if err != nil {
		t.Fatal(err)
	}
	project.Doc.Front.Set("github_repo", "owner/repo")
	project.Doc.Front.SetList("required_checks", nil)
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	installDisabledPolicyAcceptanceGH(t)
	oldLanding := runLandingGH
	runLandingGH = func(_ string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
			return `[{"state":"MERGED","baseRefName":"main"}]`, nil
		}
		return "", nil
	}
	t.Cleanup(func() { runLandingGH = oldLanding })

	marker := filepath.Join(t.TempDir(), "verified")
	err = acceptOneForTreePolicy(ctx, w, root, task, "touch '"+marker+"'", true, false, false, false, false, false, false, "main", "", "")
	if err != nil {
		t.Fatalf("manual-only acceptance failed without --allow-unobservable-check-policy: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("local verification did not run: %v", err)
	}
	accepted, err := store.FindTask(w, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.VerificationEvidenceRecords(accepted)) != 1 {
		t.Fatalf("local verification evidence missing: %+v", store.VerificationEvidenceRecords(accepted))
	}
	policy := store.VerificationEvidenceRecords(accepted)[0].ExternalPolicy
	if policy == nil || policy.State != "observed" || policy.Override || len(policy.Requirements) != 0 {
		t.Fatalf("disabled check policy = %+v, want observed empty policy without override", policy)
	}
	log := taskLog(t, accepted)
	if strings.Contains(log, "required-check policy override") || !strings.Contains(log, "merged into main") {
		t.Fatalf("acceptance log lost local gates or recorded an override: %s", log)
	}
}

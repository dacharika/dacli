package store

import (
	"reflect"
	"testing"
	"time"
)

func TestRequiredCheckPolicyMergesConfiguredLegacyAndApplicableRulesets(t *testing.T) {
	policy := newRequiredCheckPolicy("owner/repo", "main", []string{"configured", "shared"}, time.Unix(1, 0))
	legacy := []byte(`{"contexts":["legacy","shared"],"checks":[{"context":"app-check"}]}`)
	rules := []byte(`[
      {"type":"required_status_checks","ruleset_id":22,"ruleset_source_type":"Organization","ruleset_source":"owner","parameters":{"required_status_checks":[{"context":"ruleset"},{"context":"shared"}]}},
      {"type":"pull_request","ruleset_id":23,"parameters":{} }
    ]`)
	if err := mergeGitHubRequiredChecks(&policy, legacy, rules); err != nil {
		t.Fatal(err)
	}
	if got, want := policy.Names(), []string{"app-check", "configured", "legacy", "ruleset", "shared"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements = %v, want %v", got, want)
	}
	shared := policy.Requirements[4]
	if len(shared.Sources) != 3 || shared.Sources[0].Kind != "configured" || shared.Sources[1].Kind != "legacy_branch_protection" || shared.Sources[2].Kind != "ruleset" {
		t.Fatalf("shared provenance = %+v", shared.Sources)
	}
}

func TestRequiredCheckPolicyRulesetOnlyBranchProtectionOnlyAndNoProtection(t *testing.T) {
	for name, tc := range map[string]struct {
		legacy []byte
		rules  []byte
		want   []string
	}{
		"ruleset only":                      {rules: []byte(`[{"type":"required_status_checks","ruleset_id":7,"parameters":{"required_status_checks":[{"context":"ci"}]}}]`), want: []string{"ci"}},
		"paginated ruleset":                 {rules: []byte(`[[{"type":"required_status_checks","ruleset_id":8,"parameters":{"required_status_checks":[{"context":"pages"}]}}]]`), want: []string{"pages"}},
		"legacy only":                       {legacy: []byte(`{"contexts":["build"],"checks":[]}`), rules: []byte(`[]`), want: []string{"build"}},
		"no protection":                     {rules: []byte(`[]`), want: []string{}},
		"non-check evaluated rules omitted": {rules: []byte(`[{"type":"pull_request","ruleset_id":9,"parameters":{}}]`), want: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := newRequiredCheckPolicy("owner/repo", "main", nil, time.Time{})
			if err := mergeGitHubRequiredChecks(&policy, tc.legacy, tc.rules); err != nil {
				t.Fatal(err)
			}
			if got := policy.Names(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("requirements = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiredCheckPolicyMalformedObservationFailsClosed(t *testing.T) {
	for name, tc := range map[string][2][]byte{
		"legacy":  {[]byte(`{`), []byte(`[]`)},
		"ruleset": {nil, []byte(`{`)},
	} {
		t.Run(name, func(t *testing.T) {
			policy := newRequiredCheckPolicy("owner/repo", "main", nil, time.Time{})
			if err := mergeGitHubRequiredChecks(&policy, tc[0], tc[1]); err == nil {
				t.Fatal("malformed GitHub policy was accepted")
			}
		})
	}
}

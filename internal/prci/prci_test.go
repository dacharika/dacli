package prci

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRequiredClassificationsHaveStableAgentFields(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * time.Minute)
	base := Input{CanonicalHead: "dacli/858-pr-diagnose", CanonicalHeadOID: "new", Now: now,
		PullRequests: []PullRequest{{Number: 9, URL: "https://github.test/o/r/pull/9", State: "OPEN", Head: "dacli/858-pr-diagnose", HeadOID: "new", MergeState: "CLEAN"}}}
	with := func(fn func(*Input)) Input {
		in := base
		in.PullRequests = append([]PullRequest(nil), base.PullRequests...)
		fn(&in)
		return in
	}
	cases := []struct {
		name, code, source string
		in                 Input
		retry              bool
	}{
		{"test failure", "test_failure", "check_run", with(func(in *Input) { in.Checks = []Check{{Name: "unit", Conclusion: "FAILURE", URL: "check"}} }), false},
		{"annotation workflow syntax", "workflow_configuration_failure", "annotation", with(func(in *Input) {
			in.Checks = []Check{{Name: "build", Conclusion: "FAILURE", Annotations: []Evidence{{Source: "annotation", Message: "Invalid workflow: YAML syntax error", URL: "annotation"}}}}
		}), false},
		{"workflow conclusion syntax", "workflow_configuration_failure", "workflow_run", with(func(in *Input) {
			in.WorkflowRuns = []WorkflowRun{{Name: "invalid workflow", Conclusion: "startup_failure", URL: "run"}}
		}), false},
		{"runner unavailable", "runner_unavailable", "check_run", with(func(in *Input) { in.Checks = []Check{{Name: "macos", Status: "queued", StartedAt: &old, URL: "check"}} }), true},
		{"billing", "billing_restriction", "annotation", with(func(in *Input) {
			in.Checks = []Check{{Name: "build", Conclusion: "FAILURE", Annotations: []Evidence{{Source: "annotation", Message: "Job not started because spending limit was reached"}}}}
		}), false},
		{"authentication", "github_authentication", "github_api", Input{AccessFailure: &AccessFailure{Operation: "pr list", Message: "HTTP 401: Bad credentials"}}, false},
		{"authorization", "github_authorization", "github_api", Input{AccessFailure: &AccessFailure{Operation: "checks", Message: "HTTP 403: Resource not accessible by integration"}}, false},
		{"rate limit", "github_rate_limited", "github_api", Input{AccessFailure: &AccessFailure{Operation: "checks", Message: "API rate limit exceeded"}}, true},
		{"outage", "github_outage", "github_api", Input{AccessFailure: &AccessFailure{Operation: "checks", Message: "HTTP 503 connection timed out"}}, true},
		{"approval", "approval_pending", "pull_request", with(func(in *Input) { in.PullRequests[0].ReviewDecision = "REVIEW_REQUIRED" }), false},
		{"environment approval", "approval_pending", "annotation", with(func(in *Input) {
			in.Checks = []Check{{Name: "deploy", Status: "waiting", Annotations: []Evidence{{Source: "annotation", Message: "Waiting for approval from a deployment reviewer"}}}}
		}), false},
		{"conflict", "merge_conflict", "pull_request", with(func(in *Input) { in.PullRequests[0].MergeState = "DIRTY" }), false},
		{"behind", "stale_base", "pull_request", with(func(in *Input) { in.PullRequests[0].MergeState = "BEHIND" }), false},
		{"closed", "closed_unmerged", "pull_request", with(func(in *Input) { in.PullRequests[0].State = "CLOSED" }), false},
		{"missing", "missing_canonical_pr", "pull_request", Input{CanonicalHead: base.CanonicalHead, CanonicalHeadOID: "new", Now: now}, false},
		{"superseded", "superseded_pr_generation", "pull_request", with(func(in *Input) { in.PullRequests[0].HeadOID = "old" }), false},
		{"unknown", "unknown", "github", base, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnose(tc.in)
			if got.Code != tc.code || got.Retryable != tc.retry {
				t.Fatalf("result=%#v, want code=%s retry=%v", got, tc.code, tc.retry)
			}
			if got.Summary == "" || got.Next == "" || len(got.Evidence) == 0 {
				t.Fatalf("result lost stable agent fields: %#v", got)
			}
			if got.Evidence[0].Source != tc.source {
				t.Fatalf("evidence=%#v, want source %q", got.Evidence, tc.source)
			}
		})
	}
}

func TestAccountAndTransportFailuresNeverCollapseIntoRedCI(t *testing.T) {
	for _, message := range []string{"billing spending limit reached", "Bad credentials", "Resource not accessible by integration", "API rate limit exceeded", "connection timed out"} {
		got := Diagnose(Input{AccessFailure: &AccessFailure{Operation: "checks", Message: message}})
		if got.Code == "test_failure" || got.Code == "ready" || !strings.HasPrefix(got.Evidence[0].Source, "github_api") {
			t.Fatalf("%q collapsed into CI verdict: %#v", message, got)
		}
	}
}

func TestCanonicalGenerationUsesHeadOIDNotFirstHistoricalPR(t *testing.T) {
	in := Input{CanonicalHead: "dacli/858", CanonicalHeadOID: "current", PullRequests: []PullRequest{
		{Number: 11, State: "OPEN", Head: "dacli/858", HeadOID: "old", MergeState: "DIRTY"},
		{Number: 12, State: "OPEN", Head: "somewhere-else", HeadOID: "current", MergeState: "DIRTY"},
		{Number: 13, State: "OPEN", Head: "dacli/858", HeadOID: "current", MergeState: "CLEAN"},
	}, Checks: []Check{{Name: "unit", Status: "completed", Conclusion: "SUCCESS"}}}
	got := Diagnose(in)
	if got.Code != "ready" || got.PullRequest == nil || got.PullRequest.Number != 13 || len(got.SupersededPRs) != 1 || got.SupersededPRs[0].Number != 11 {
		t.Fatalf("canonical generation resolution=%#v", got)
	}
}

func TestExact540WorkflowOnlyPendingMustNotBeReady(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Minute)
	in := Input{CanonicalHead: "task", CanonicalHeadOID: "current", Now: now, PullRequests: []PullRequest{{Number: 1, State: "OPEN", Head: "task", HeadOID: "current", MergeState: "CLEAN"}}, WorkflowRuns: []WorkflowRun{{Name: "unit", Status: "in_progress", CreatedAt: &recent}}}
	got := Diagnose(in)
	if got.Code != "ci_pending" || !got.Retryable {
		t.Fatalf("workflow-only pending falsely classified: %+v", got)
	}
}

func TestExact540RunningPastQueueThresholdIsNotRunnerUnavailable(t *testing.T) {
	now := time.Now()
	old := now.Add(-31 * time.Minute)
	got := Diagnose(Input{CanonicalHead: "task", CanonicalHeadOID: "current", Now: now, PullRequests: []PullRequest{{Number: 1, State: "OPEN", Head: "task", HeadOID: "current", MergeState: "CLEAN"}}, Checks: []Check{{Name: "long running", Status: "in_progress", StartedAt: &old}}})
	if got.Code != "ci_pending" || !got.Retryable {
		t.Fatalf("running job mislabeled queued runner unavailable: %+v", got)
	}
}

func TestUnknownEvidenceNeverReady(t *testing.T) {
	base := Input{CanonicalHead: "task", CanonicalHeadOID: "current", PullRequests: []PullRequest{{Number: 1, State: "OPEN", Head: "task", HeadOID: "current", MergeState: "CLEAN"}}, Checks: []Check{{Name: "unit", Status: "completed", Conclusion: "success"}}}
	for _, field := range []string{"pr-state", "merge-state", "review-decision", "check-status", "check-conclusion", "workflow-status", "workflow-conclusion", "missing-canonical-head"} {
		t.Run(field, func(t *testing.T) {
			in := base
			in.PullRequests = append([]PullRequest(nil), base.PullRequests...)
			in.Checks = append([]Check(nil), base.Checks...)
			switch field {
			case "pr-state":
				in.PullRequests[0].State = "unexpected"
			case "merge-state":
				in.PullRequests[0].MergeState = ""
			case "review-decision":
				in.PullRequests[0].ReviewDecision = "unexpected"
			case "check-status":
				in.Checks[0].Status = ""
			case "check-conclusion":
				in.Checks[0].Conclusion = "unexpected"
			case "workflow-status":
				in.WorkflowRuns = []WorkflowRun{{Name: "workflow", Status: "unexpected", Conclusion: "success"}}
			case "workflow-conclusion":
				in.WorkflowRuns = []WorkflowRun{{Name: "workflow", Status: "completed"}}
			case "missing-canonical-head":
				in.CanonicalHeadOID = ""
			}
			got := Diagnose(in)
			if got.Code != "unknown" || !got.Retryable || got.Next == "" || len(got.Evidence) == 0 {
				t.Fatalf("unknown evidence became ready or lost actionable diagnosis (%s): %+v", field, got)
			}
		})
	}
}
func TestQueueThresholdDistinguishesWaitingRunningAndQueue(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * time.Minute)
	for _, status := range []string{"queued", "pending", "requested", "waiting", "in_progress"} {
		for _, workflow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/workflow=%t", status, workflow), func(t *testing.T) {
				in := Input{CanonicalHead: "task", CanonicalHeadOID: "current", Now: now, PullRequests: []PullRequest{{Number: 1, State: "OPEN", Head: "task", HeadOID: "current", MergeState: "CLEAN"}}}
				if workflow {
					in.WorkflowRuns = []WorkflowRun{{Name: "CI", Status: status, CreatedAt: &old}}
				} else {
					in.Checks = []Check{{Name: "CI", Status: status, StartedAt: &old}}
				}
				want := "runner_unavailable"
				if status == "waiting" || status == "in_progress" {
					want = "ci_pending"
				}
				got := Diagnose(in)
				if got.Code != want || !got.Retryable {
					t.Fatalf("queue-only threshold classification (%s): %+v", want, got)
				}
			})
		}
	}
}
func TestRecognizedTerminalChecksCanBeReady(t *testing.T) {
	for _, conclusion := range []string{"success", "neutral", "skipped"} {
		in := Input{CanonicalHead: "task", CanonicalHeadOID: "current", PullRequests: []PullRequest{{Number: 1, State: "OPEN", Head: "task", HeadOID: "current", MergeState: "CLEAN"}}, Checks: []Check{{Name: "unit", Status: "completed", Conclusion: conclusion}}, WorkflowRuns: []WorkflowRun{{Name: "workflow", Status: "completed", Conclusion: conclusion}}}
		if got := Diagnose(in); got.Code != "ready" {
			t.Fatalf("recognized terminal success refused: %+v", got)
		}
	}
}

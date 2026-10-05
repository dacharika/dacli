package vcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/prci"
)

func TestCollectPRDiagnosisInspectsAnnotationsAndWorkflowConclusions(t *testing.T) {
	dir, _, tk := prIntegrateEnv(t)
	branch := BranchFor(tk)
	branchOID := gitRef(t, dir, branch)
	stubGH(t, func(_ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "repo view --json nameWithOwner --jq .nameWithOwner":
			return "o/r", nil
		case strings.HasPrefix(joined, "pr list --head "):
			return fmt.Sprintf(`[{"number":8,"url":"old","state":"CLOSED","headRefName":%q,"headRefOid":"old"},{"number":9,"url":"current","state":"OPEN","headRefName":%q,"headRefOid":"%s","mergeStateStatus":"CLEAN"}]`, branch, branch, branchOID), nil
		case strings.Contains(joined, "/check-runs/71/annotations"):
			return `[{"path":".github/workflows/ci.yml","start_line":3,"message":"Invalid workflow: YAML syntax error","blob_href":"annotation-url"}]`, nil
		case strings.Contains(joined, "/commits/") && strings.HasSuffix(joined, "/check-runs"):
			return `{"check_runs":[{"id":71,"name":"CI","status":"completed","conclusion":"failure","details_url":"check-url","annotations_count":1}]}`, nil
		case strings.HasPrefix(joined, "run list --branch "):
			return fmt.Sprintf(`[{"name":"CI","status":"completed","conclusion":"failure","createdAt":"2026-08-28T10:00:00Z","url":"run-url","headSha":%q},{"name":"old CI","status":"completed","conclusion":"failure","headSha":"old"}]`, branchOID), nil
		default:
			return "unexpected: " + joined, errors.New("unexpected gh invocation")
		}
	})
	in := collectPRDiagnosisFromGitHub(dir, branch)
	got := prci.Diagnose(in)
	if got.Code != "workflow_configuration_failure" || got.PullRequest == nil || got.PullRequest.Number != 9 {
		t.Fatalf("diagnosis=%#v input=%#v", got, in)
	}
	if len(in.Checks) != 1 || len(in.Checks[0].Annotations) != 1 || len(in.WorkflowRuns) != 1 {
		t.Fatalf("collector skipped detailed evidence: %#v", in)
	}
}

func TestCollectPRDiagnosisPreservesGitHubFailureClass(t *testing.T) {
	dir, _, tk := prIntegrateEnv(t)
	stubGH(t, func(_ string, args ...string) (string, error) {
		if args[0] == "repo" {
			return "o/r", nil
		}
		return "HTTP 403: Resource not accessible by integration", errors.New("exit status 1")
	})
	got := prci.Diagnose(collectPRDiagnosisFromGitHub(dir, BranchFor(tk)))
	if got.Code != "github_authorization" {
		t.Fatalf("diagnosis=%#v", got)
	}
}

func TestCmdPRDiagnoseHumanAndJSONUseSameTypedResult(t *testing.T) {
	dir, _, tk := prIntegrateEnv(t)
	oldCollect := collectPRDiagnosis
	t.Cleanup(func() { collectPRDiagnosis = oldCollect })
	collectPRDiagnosis = func(_ string, head string) prci.Input {
		return prci.Input{CanonicalHead: head, CanonicalHeadOID: "x", PullRequests: []prci.PullRequest{{Number: 4, URL: "https://example/pr/4", State: "OPEN", Head: head, HeadOID: "x", MergeState: "CLEAN"}}, Checks: []prci.Check{{Name: "unit", Conclusion: "failure", URL: "check"}}}
	}
	var human bytes.Buffer
	if err := cmdPRDiagnose(&clikit.Ctx{Cwd: dir, Stdout: &human}, []string{"--task", fmt.Sprint(tk.Seq)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "test_failure") || !strings.Contains(human.String(), "retryable: false") || !strings.Contains(human.String(), "next:") {
		t.Fatalf("human=%s", human.String())
	}
	var machine bytes.Buffer
	if err := cmdPRDiagnose(&clikit.Ctx{Cwd: dir, Stdout: &machine, JSON: true}, []string{"--task", fmt.Sprint(tk.Seq)}); err != nil {
		t.Fatal(err)
	}
	var got prci.Result
	if err := json.Unmarshal(machine.Bytes(), &got); err != nil {
		t.Fatalf("json=%q: %v", machine.String(), err)
	}
	if got.Code != "test_failure" || got.PullRequest == nil || got.PullRequest.Number != 4 || got.Next == "" {
		t.Fatalf("json result=%#v", got)
	}
}

func TestPRWaitUsesDiagnosisAndStopsOnReadyOrPermanentBlocker(t *testing.T) {
	dir, _, task := prIntegrateEnv(t)
	oldCollect := collectPRDiagnosis
	t.Cleanup(func() { collectPRDiagnosis = oldCollect })
	ready := true
	collectPRDiagnosis = func(_ string, head string) prci.Input {
		input := prci.Input{CanonicalHead: head, CanonicalHeadOID: "x", PullRequests: []prci.PullRequest{{Number: 4, URL: "https://example/pr/4", State: "OPEN", Head: head, HeadOID: "x", MergeState: "CLEAN"}}}
		if ready {
			input.Checks = []prci.Check{{Name: "unit", Status: "completed", Conclusion: "success"}}
		} else {
			input.Checks = []prci.Check{{Name: "unit", Status: "completed", Conclusion: "failure"}}
		}
		return input
	}
	var out bytes.Buffer
	if err := cmdPRWait(&clikit.Ctx{Cwd: dir, Stdout: &out, JSON: true}, []string{"--task", task.ID, "--timeout", "1", "--interval", "1"}); err != nil {
		t.Fatal(err)
	}
	var got prci.Result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Code != "ready" {
		t.Fatalf("ready wait = %+v err=%v output=%s", got, err, out.String())
	}
	ready = false
	err := cmdPRWait(&clikit.Ctx{Cwd: dir, Stdout: &bytes.Buffer{}}, []string{"--task", task.ID, "--timeout", "1", "--interval", "1"})
	if clikit.ExitCode(err) != 3 || !strings.Contains(err.Error(), "test_failure") {
		t.Fatalf("permanent wait diagnosis = exit %d err=%v", clikit.ExitCode(err), err)
	}
}

func TestPRLandDelegatesExactArgumentsToCanonicalIntegration(t *testing.T) {
	old := integrateTaskPR
	t.Cleanup(func() { integrateTaskPR = old })
	var got []string
	integrateTaskPR = func(_ *clikit.Ctx, args []string) error {
		got = append([]string(nil), args...)
		return nil
	}
	if err := cmdPRLand(&clikit.Ctx{}, []string{"--task", "core/001", "--base", "release", "--auto", "--merge"}); err != nil {
		t.Fatal(err)
	}
	want := "--tasks core/001 --pr --into release --auto --merge"
	if strings.Join(got, " ") != want {
		t.Fatalf("pr land delegation = %q, want %q", strings.Join(got, " "), want)
	}
}

func gitRef(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := gitIn(dir, "rev-parse", ref)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func TestRunnerThresholdIsDocumentedAndStable(t *testing.T) {
	if prci.RunnerQueueThreshold != 30*time.Minute {
		t.Fatalf("threshold=%s", prci.RunnerQueueThreshold)
	}
}

func TestExact540UnobservableCanonicalHeadMustNotUseHistoricalPR(t *testing.T) {
	dir, _, _ := prIntegrateEnv(t)
	head := "dacli/missing-local-canonical-head"
	stubGH(t, func(_ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "repo view"):
			return "o/r", nil
		case strings.HasPrefix(joined, "pr list"):
			return fmt.Sprintf(`[{"number":1,"state":"OPEN","headRefName":%q,"headRefOid":"historical-first","mergeStateStatus":"CLEAN"},{"number":2,"state":"OPEN","headRefName":%q,"headRefOid":"different-generation","mergeStateStatus":"CLEAN"}]`, head, head), nil
		case strings.Contains(joined, "/check-runs"):
			return `{"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success"}]}`, nil
		case strings.HasPrefix(joined, "run list"):
			return `[]`, nil
		default:
			return "", errors.New("unexpected fixture read")
		}
	})
	in := collectPRDiagnosisFromGitHub(dir, head)
	got := prci.Diagnose(in)
	if got.Code != "unknown" || in.CanonicalHeadOID != "" {
		t.Fatalf("unobservable canonical head adopted historical generation: code=%s oid=%s pr=%+v", got.Code, in.CanonicalHeadOID, got.PullRequest)
	}
}

func TestWorkflowWithoutExactHeadIsNotCanonicalEvidence(t *testing.T) {
	dir, _, tk := prIntegrateEnv(t)
	head := BranchFor(tk)
	oid := gitRef(t, dir, head)
	stubGH(t, func(_ string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "repo view"):
			return "o/r", nil
		case strings.HasPrefix(joined, "pr list"):
			return fmt.Sprintf(`[{"number":1,"state":"OPEN","headRefName":%q,"headRefOid":%q,"mergeStateStatus":"CLEAN"}]`, head, oid), nil
		case strings.Contains(joined, "/check-runs"):
			return `{"check_runs":[]}`, nil
		case strings.HasPrefix(joined, "run list"):
			return `[{"name":"unbound workflow","status":"completed","conclusion":"success"}]`, nil
		default:
			return "", errors.New("unexpected fixture read")
		}
	})
	in := collectPRDiagnosisFromGitHub(dir, head)
	if got := prci.Diagnose(in); got.Code != "unknown" || len(in.WorkflowRuns) != 0 {
		t.Fatalf("unbound workflow became canonical evidence: %+v input=%+v", got, in)
	}
}

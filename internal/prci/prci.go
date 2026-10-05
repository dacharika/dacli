// Package prci classifies pull-request and CI evidence without performing I/O.
// Keeping this model below feature slices lets the CLI, reconciliation, and
// loop recovery reach the same conclusion from the same GitHub evidence.
package prci

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const RunnerQueueThreshold = 30 * time.Minute

type Evidence struct {
	Source  string `json:"source"`
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
}

type PullRequest struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	Head           string `json:"head"`
	HeadOID        string `json:"head_oid"`
	MergeState     string `json:"merge_state,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"`
}

type Check struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	URL         string     `json:"url,omitempty"`
	Annotations []Evidence `json:"annotations,omitempty"`
}

type WorkflowRun struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion,omitempty"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
	URL        string     `json:"url,omitempty"`
}

type AccessFailure struct {
	Operation string `json:"operation"`
	Message   string `json:"message"`
}

type Input struct {
	CanonicalHead    string         `json:"canonical_head"`
	CanonicalHeadOID string         `json:"canonical_head_oid,omitempty"`
	PullRequests     []PullRequest  `json:"pull_requests,omitempty"`
	Checks           []Check        `json:"checks,omitempty"`
	WorkflowRuns     []WorkflowRun  `json:"workflow_runs,omitempty"`
	AccessFailure    *AccessFailure `json:"access_failure,omitempty"`
	Now              time.Time      `json:"-"`
}

type Result struct {
	Code           string        `json:"code"`
	Summary        string        `json:"summary"`
	Retryable      bool          `json:"retryable"`
	Next           string        `json:"next"`
	Evidence       []Evidence    `json:"evidence"`
	PullRequest    *PullRequest  `json:"pull_request,omitempty"`
	SupersededPRs  []PullRequest `json:"superseded_prs,omitempty"`
	QueueThreshold string        `json:"queue_threshold,omitempty"`
}

func result(code, summary string, retryable bool, next string, evidence ...Evidence) Result {
	return Result{Code: code, Summary: summary, Retryable: retryable, Next: next, Evidence: evidence}
}

func ev(source, message, url string) Evidence {
	return Evidence{Source: source, Message: message, URL: url}
}

// Diagnose is deliberately deterministic: precedence is policy/account,
// canonical PR identity, PR topology, approvals, then individual CI evidence.
func Diagnose(in Input) Result {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	if in.AccessFailure != nil {
		return classifyAccess(*in.AccessFailure)
	}

	// A branch name or historical PR is not an observation of its current commit.
	if in.CanonicalHead == "" || in.CanonicalHeadOID == "" {
		return unknownEvidence("local_git", "canonical task head commit could not be observed", "")
	}

	matching := make([]PullRequest, 0)
	for _, pr := range in.PullRequests {
		if pr.Head == in.CanonicalHead {
			matching = append(matching, pr)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Number > matching[j].Number })
	current, superseded := selectGeneration(matching, in.CanonicalHeadOID)
	if current == nil {
		if len(superseded) > 0 {
			r := result("superseded_pr_generation", "Only superseded pull-request generations exist for the task head", false,
				"Push the current canonical task head and open or reuse a pull request for that exact commit.",
				ev("pull_request", fmt.Sprintf("newest historical PR #%d points at %s, canonical head is %s", superseded[0].Number, dash(superseded[0].HeadOID), dash(in.CanonicalHeadOID)), superseded[0].URL))
			r.SupersededPRs = superseded
			return r
		}
		return result("missing_canonical_pr", "No pull request exists for the canonical task head", false,
			"Push the canonical task branch and open a pull request for it.", ev("pull_request", "no PR matched head "+in.CanonicalHead, ""))
	}
	var r Result
	state, merge := strings.ToUpper(current.State), strings.ToUpper(current.MergeState)
	if state == "CLOSED" {
		r = result("closed_unmerged", fmt.Sprintf("Pull request #%d was closed without merging", current.Number), false,
			"Reopen it or open a replacement for the same canonical head after confirming the closure was intentional.", ev("pull_request", "state=CLOSED", current.URL))
		return withPR(r, current, superseded)
	}
	if merge == "DIRTY" || merge == "CONFLICTING" {
		r = result("merge_conflict", fmt.Sprintf("Pull request #%d conflicts with its base", current.Number), false,
			"Rebase or merge the base into the task branch, resolve conflicts, and push the canonical head.", ev("pull_request", "merge_state="+merge, current.URL))
		return withPR(r, current, superseded)
	}
	if merge == "BEHIND" {
		r = result("stale_base", fmt.Sprintf("Pull request #%d is behind its base", current.Number), false,
			"Update the task branch from the configured base, rerun verification, and push.", ev("pull_request", "merge_state=BEHIND", current.URL))
		return withPR(r, current, superseded)
	}
	if strings.EqualFold(current.ReviewDecision, "REVIEW_REQUIRED") || strings.EqualFold(current.ReviewDecision, "CHANGES_REQUESTED") {
		r = result("approval_pending", fmt.Sprintf("Pull request #%d is waiting for reviewer approval", current.Number), false,
			"Request or address the required review; do not retry CI as a substitute for approval.", ev("pull_request", "review_decision="+current.ReviewDecision, current.URL))
		return withPR(r, current, superseded)
	}

	all := append([]Check(nil), in.Checks...)
	for _, c := range all {
		if code, ok := specialCode(c.Name + " " + c.Conclusion); ok {
			r = specialResult(code, ev("check_run", c.Name+": "+c.Conclusion, c.URL), c.Name)
			return withPR(r, current, superseded)
		}
		for _, a := range c.Annotations {
			if code, ok := specialCode(a.Message); ok {
				r = specialResult(code, a, c.Name)
				return withPR(r, current, superseded)
			}
		}
	}
	for _, w := range in.WorkflowRuns {
		if code, ok := specialCode(w.Name + " " + w.Conclusion); ok {
			r = specialResult(code, ev("workflow_run", w.Name+": "+w.Conclusion, w.URL), w.Name)
			return withPR(r, current, superseded)
		}
	}
	for _, c := range all {
		if queuedTooLong(c.Status, c.StartedAt, in.Now) {
			r = result("runner_unavailable", "A CI job has remained queued beyond 30 minutes", true,
				"Inspect runner availability and labels; retry only after runner capacity or routing is restored.", ev("check_run", c.Name+" status="+c.Status, c.URL))
			r.QueueThreshold = RunnerQueueThreshold.String()
			return withPR(r, current, superseded)
		}
	}
	for _, w := range in.WorkflowRuns {
		if queuedTooLong(w.Status, w.CreatedAt, in.Now) {
			r = result("runner_unavailable", "A workflow has remained queued beyond 30 minutes", true,
				"Inspect runner availability and labels; retry only after runner capacity or routing is restored.", ev("workflow_run", w.Name+" status="+w.Status, w.URL))
			r.QueueThreshold = RunnerQueueThreshold.String()
			return withPR(r, current, superseded)
		}
	}
	for _, c := range all {
		if failed(c.Conclusion) {
			evidence := ev("check_run", c.Name+" conclusion="+c.Conclusion, c.URL)
			if len(c.Annotations) > 0 {
				evidence = c.Annotations[0]
			}
			r = result("test_failure", "A test or workflow job failed", false,
				"Open the failing check and its annotations, reproduce the failure, then push a verified fix.", evidence)
			return withPR(r, current, superseded)
		}
	}
	for _, w := range in.WorkflowRuns {
		if failed(w.Conclusion) {
			r = result("test_failure", "A workflow job failed", false,
				"Open the failing workflow run, reproduce the failure, then push a verified fix.", ev("workflow_run", w.Name+" conclusion="+w.Conclusion, w.URL))
			return withPR(r, current, superseded)
		}
	}
	for _, c := range all {
		if pending(c.Status) {
			r = result("ci_pending", "CI is still running or queued within the runner threshold", true,
				"Wait for the existing check run; do not start a duplicate run.", ev("check_run", c.Name+" status="+c.Status, c.URL))
			return withPR(r, current, superseded)
		}
	}
	for _, w := range in.WorkflowRuns {
		if pending(w.Status) {
			r = result("ci_pending", "A workflow is still running or waiting", true,
				"Wait for the existing workflow; inspect its waiting reason rather than starting a duplicate run.", ev("workflow_run", w.Name+" status="+w.Status, w.URL))
			return withPR(r, current, superseded)
		}
	}
	// Presence of evidence alone is not success. Preserve unknown upstream values
	// rather than treating a new or incomplete GitHub enum as a passed gate.
	if (state != "OPEN" && state != "MERGED") || (merge != "CLEAN" && merge != "HAS_HOOKS") || (current.ReviewDecision != "" && !strings.EqualFold(current.ReviewDecision, "APPROVED")) {
		return withPR(unknownEvidence("pull_request", "unresolved PR state, merge state, or review decision", current.URL), current, superseded)
	}
	for _, c := range all {
		if !terminalSuccess(c.Status, c.Conclusion) {
			return withPR(unknownEvidence("check_run", c.Name+" status="+c.Status+" conclusion="+c.Conclusion, c.URL), current, superseded)
		}
	}
	for _, w := range in.WorkflowRuns {
		if !terminalSuccess(w.Status, w.Conclusion) {
			return withPR(unknownEvidence("workflow_run", w.Name+" status="+w.Status+" conclusion="+w.Conclusion, w.URL), current, superseded)
		}
	}
	if len(all) > 0 || len(in.WorkflowRuns) > 0 {
		r = result("ready", "The canonical pull request has no diagnosed blocker", false,
			"Continue with the configured review and landing policy.", ev("pull_request", "canonical PR evidence inspected", current.URL))
		return withPR(r, current, superseded)
	}
	r = result("unknown", "GitHub returned too little evidence to diagnose the pull request", true,
		"Inspect the pull request, check runs, workflow runs, and GitHub status; preserve the raw response when escalating.", ev("github", "no check-suite or workflow-run evidence", current.URL))
	return withPR(r, current, superseded)
}

func selectGeneration(prs []PullRequest, oid string) (*PullRequest, []PullRequest) {
	var current *PullRequest
	var old []PullRequest
	for i := range prs {
		if oid == "" || prs[i].HeadOID == oid {
			if current == nil {
				p := prs[i]
				current = &p
			} else {
				old = append(old, prs[i])
			}
		} else {
			old = append(old, prs[i])
		}
	}
	return current, old
}

func withPR(r Result, pr *PullRequest, old []PullRequest) Result {
	r.PullRequest = pr
	r.SupersededPRs = old
	return r
}

func queuedTooLong(status string, started *time.Time, now time.Time) bool {
	// Waiting may require environment approval; in-progress work has a runner.
	s := strings.ToLower(status)
	return (s == "queued" || s == "pending" || s == "requested") && started != nil && now.Sub(*started) > RunnerQueueThreshold
}
func pending(s string) bool {
	s = strings.ToLower(s)
	return s == "queued" || s == "in_progress" || s == "pending" || s == "waiting" || s == "requested"
}
func failed(s string) bool {
	s = strings.ToLower(s)
	return s == "failure" || s == "failed" || s == "timed_out" || s == "cancelled" || s == "startup_failure"
}
func dash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func specialCode(s string) (string, bool) {
	s = strings.ToLower(s)
	patterns := []struct {
		code  string
		terms []string
	}{
		{"billing_restriction", []string{"billing", "spending limit", "payment", "account is locked"}},
		{"workflow_configuration_failure", []string{"workflow is not valid", "invalid workflow", "yaml syntax", "startup_failure", "no jobs were run"}},
		{"runner_unavailable", []string{"no runner matching", "runner unavailable", "no hosted runners", "waiting for a runner"}},
		{"approval_pending", []string{"waiting for approval", "environment approval", "deployment review", "action_required"}},
	}
	for _, p := range patterns {
		for _, term := range p.terms {
			if strings.Contains(s, term) {
				return p.code, true
			}
		}
	}
	return "", false
}

func specialResult(code string, evidence Evidence, name string) Result {
	switch code {
	case "billing_restriction":
		return result(code, "GitHub blocked CI because of an account billing or spending restriction", false, "Have an account owner repair billing or spending limits; rerun only after GitHub permits Actions.", evidence)
	case "workflow_configuration_failure":
		return result(code, "GitHub could not start the workflow because its configuration is invalid", false, "Open the workflow diagnostic, repair the referenced workflow configuration, validate it, and push the fix.", evidence)
	case "runner_unavailable":
		r := result(code, "GitHub could not assign a compatible runner", true, "Inspect runner availability and labels; retry only after runner capacity or routing is restored.", evidence)
		r.QueueThreshold = RunnerQueueThreshold.String()
		return r
	default:
		return result(code, "A deployment environment is waiting for approval", false, "Have an authorized reviewer approve or reject the pending environment deployment.", evidence)
	}
}

func classifyAccess(f AccessFailure) Result {
	s := strings.ToLower(f.Message)
	e := ev("github_api", f.Operation+": "+f.Message, "")
	switch {
	case strings.Contains(s, "billing"), strings.Contains(s, "spending limit"), strings.Contains(s, "payment required"):
		return result("billing_restriction", "GitHub blocked CI because of an account billing or spending restriction", false, "Have an account owner repair billing or spending limits; rerun only after GitHub permits Actions.", e)
	case strings.Contains(s, "bad credentials"), strings.Contains(s, "authentication"), strings.Contains(s, "not logged"), strings.Contains(s, "token has expired"), strings.Contains(s, "http 401"):
		return result("github_authentication", "GitHub authentication failed", false, "Authenticate gh with the intended account and token, then rerun diagnosis.", e)
	case strings.Contains(s, "rate limit"), strings.Contains(s, "secondary rate"):
		return result("github_rate_limited", "GitHub API rate limiting prevented diagnosis", true, "Wait for the reported reset window and retry once; avoid parallel duplicate polling.", e)
	case strings.Contains(s, "forbidden"), strings.Contains(s, "resource not accessible"), strings.Contains(s, "permission"), strings.Contains(s, "authorization"), strings.Contains(s, "http 403"):
		return result("github_authorization", "GitHub denied this identity permission", false, "Grant the identity read access to pull requests, checks, actions, and annotations, then rerun diagnosis.", e)
	case strings.Contains(s, "timeout"), strings.Contains(s, "timed out"), strings.Contains(s, "could not resolve"), strings.Contains(s, "no such host"), strings.Contains(s, "connection"), strings.Contains(s, "tls handshake"), strings.Contains(s, "http 500"), strings.Contains(s, "502"), strings.Contains(s, "503"), strings.Contains(s, "504"), strings.Contains(s, "github is down"):
		return result("github_outage", "GitHub network or API availability prevented diagnosis", true, "Check GitHub status and network reachability, then retry without changing the PR.", e)
	default:
		return result("unknown", "GitHub failed without enough evidence to classify the cause", true, "Preserve the raw GitHub response and inspect authentication, permissions, rate limits, and service status before retrying.", e)
	}
}

func terminalSuccess(status, conclusion string) bool {
	if !strings.EqualFold(status, "completed") {
		return false
	}
	switch strings.ToLower(conclusion) {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}
func unknownEvidence(source, message, url string) Result {
	return result("unknown", "Available evidence does not establish canonical PR readiness", true,
		"Inspect the canonical local branch and exact GitHub PR/check/workflow response; resolve missing or unknown fields before continuing.", ev(source, message, url))
}

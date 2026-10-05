package vcs

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/clikit"
	"github.com/mlnomadpy/dacli/internal/prci"
)

type ghPRDiagnosis struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	State          string `json:"state"`
	HeadRefName    string `json:"headRefName"`
	HeadRefOID     string `json:"headRefOid"`
	MergeState     string `json:"mergeStateStatus"`
	ReviewDecision string `json:"reviewDecision"`
}

type ghCheckRuns struct {
	CheckRuns []struct {
		ID               int64  `json:"id"`
		Name             string `json:"name"`
		Status           string `json:"status"`
		Conclusion       string `json:"conclusion"`
		StartedAt        string `json:"started_at"`
		DetailsURL       string `json:"details_url"`
		AnnotationsCount int    `json:"annotations_count"`
	} `json:"check_runs"`
}

type ghAnnotation struct {
	Message         string `json:"message"`
	AnnotationLevel string `json:"annotation_level"`
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	BlobHref        string `json:"blob_href"`
}

type ghWorkflowRun struct {
	Name       string `json:"name"`
	Workflow   string `json:"workflowName"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	CreatedAt  string `json:"createdAt"`
	URL        string `json:"url"`
	HeadSHA    string `json:"headSha"`
}

var collectPRDiagnosis = collectPRDiagnosisFromGitHub
var integrateTaskPR = cmdIntegrate

func cmdPRDiagnose(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("task"); err != nil {
		return err
	}
	task, err := resolveTaskFlag(w, f)
	if err != nil {
		return err
	}
	in := collectPRDiagnosis(w.Root, BranchFor(task))
	got := prci.Diagnose(in)
	if ctx.JSON {
		return json.NewEncoder(ctx.Stdout).Encode(got)
	}
	fmt.Fprintf(ctx.Stdout, "%s: %s\n", got.Code, got.Summary)
	if got.PullRequest != nil {
		fmt.Fprintf(ctx.Stdout, "pr: #%d %s\n", got.PullRequest.Number, got.PullRequest.URL)
	}
	for _, e := range got.Evidence {
		fmt.Fprintf(ctx.Stdout, "evidence[%s]: %s", e.Source, e.Message)
		if e.URL != "" {
			fmt.Fprintf(ctx.Stdout, " (%s)", e.URL)
		}
		fmt.Fprintln(ctx.Stdout)
	}
	fmt.Fprintf(ctx.Stdout, "retryable: %t\nnext: %s\n", got.Retryable, got.Next)
	return nil
}

func cmdPRWait(ctx *clikit.Ctx, args []string) error {
	w, _, err := clikit.OpenWorkspace(ctx)
	if err != nil {
		return err
	}
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("task", "timeout", "interval"); err != nil {
		return err
	}
	task, err := resolveTaskFlag(w, f)
	if err != nil {
		return err
	}
	timeout, err := f.Int("timeout", 900)
	if err != nil || timeout <= 0 {
		return clikit.Usagef("--timeout must be a positive number of seconds")
	}
	interval, err := f.Int("interval", 10)
	if err != nil || interval <= 0 {
		return clikit.Usagef("--interval must be a positive number of seconds")
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		got := prci.Diagnose(collectPRDiagnosis(w.Root, BranchFor(task)))
		if got.Code == "ready" {
			if ctx.JSON {
				return json.NewEncoder(ctx.Stdout).Encode(got)
			}
			fmt.Fprintf(ctx.Stdout, "ready: %s\nnext: %s\n", got.Summary, got.Next)
			return nil
		}
		if !got.Retryable {
			return clikit.Refusedf("PR wait stopped at %s: %s; next: %s", got.Code, got.Summary, got.Next)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("PR wait timed out at %s: %s; next: %s", got.Code, got.Summary, got.Next)
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func cmdPRLand(ctx *clikit.Ctx, args []string) error {
	f, err := clikit.ParseFlags(args)
	if err != nil {
		return err
	}
	if err := f.Reject("task", "base", "auto", "merge"); err != nil {
		return err
	}
	task := f.Get("task")
	if task == "" {
		return clikit.Usagef("--task is required")
	}
	integrate := []string{"--tasks", task, "--pr"}
	if base := f.Get("base"); base != "" {
		integrate = append(integrate, "--into", base)
	}
	if f.Bool("auto") {
		integrate = append(integrate, "--auto")
	}
	if f.Bool("merge") {
		integrate = append(integrate, "--merge")
	}
	return integrateTaskPR(ctx, integrate)
}

func collectPRDiagnosisFromGitHub(root, head string) prci.Input {
	in := prci.Input{CanonicalHead: head, Now: time.Now().UTC()}
	if oid, err := gitIn(root, "rev-parse", head); err == nil {
		in.CanonicalHeadOID = strings.TrimSpace(oid)
	}
	repoOut, err := runGH(root, "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return accessInput(in, "repository lookup", repoOut, err)
	}
	repo := strings.TrimSpace(repoOut)
	prOut, err := runGH(root, "pr", "list", "--head", head, "--state", "all", "--limit", "100", "--json", "number,url,state,headRefName,headRefOid,mergeStateStatus,reviewDecision")
	if err != nil {
		return accessInput(in, "pull-request lookup", prOut, err)
	}
	var prs []ghPRDiagnosis
	if err := json.Unmarshal([]byte(prOut), &prs); err != nil {
		in.AccessFailure = &prci.AccessFailure{Operation: "pull-request decode", Message: err.Error()}
		return in
	}
	for _, p := range prs {
		in.PullRequests = append(in.PullRequests, prci.PullRequest{Number: p.Number, URL: p.URL, State: p.State, Head: p.HeadRefName, HeadOID: p.HeadRefOID, MergeState: p.MergeState, ReviewDecision: p.ReviewDecision})
	}
	if in.CanonicalHeadOID == "" {
		return in
	}
	checksOut, err := runGH(root, "api", "repos/"+repo+"/commits/"+in.CanonicalHeadOID+"/check-runs")
	if err != nil {
		return accessInput(in, "check-suite lookup", checksOut, err)
	}
	var checks ghCheckRuns
	if err := json.Unmarshal([]byte(checksOut), &checks); err != nil {
		in.AccessFailure = &prci.AccessFailure{Operation: "check-suite decode", Message: err.Error()}
		return in
	}
	for _, c := range checks.CheckRuns {
		check := prci.Check{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, URL: c.DetailsURL, StartedAt: parseGitHubTime(c.StartedAt)}
		if c.AnnotationsCount > 0 {
			out, aerr := runGH(root, "api", fmt.Sprintf("repos/%s/check-runs/%d/annotations", repo, c.ID), "--paginate", "--slurp")
			if aerr != nil {
				return accessInput(in, "check annotation lookup", out, aerr)
			}
			annotations, err := decodeAnnotations([]byte(out))
			if err != nil {
				in.AccessFailure = &prci.AccessFailure{Operation: "check annotation decode", Message: err.Error()}
				return in
			}
			for _, a := range annotations {
				message := a.Message
				if a.Path != "" {
					message = fmt.Sprintf("%s:%d: %s", a.Path, a.StartLine, message)
				}
				check.Annotations = append(check.Annotations, prci.Evidence{Source: "annotation", Message: message, URL: a.BlobHref})
			}
		}
		in.Checks = append(in.Checks, check)
	}
	runsOut, err := runGH(root, "run", "list", "--branch", head, "--limit", "50", "--json", "name,workflowName,status,conclusion,createdAt,url,headSha")
	if err != nil {
		return accessInput(in, "workflow-run lookup", runsOut, err)
	}
	var runs []ghWorkflowRun
	if err := json.Unmarshal([]byte(runsOut), &runs); err != nil {
		in.AccessFailure = &prci.AccessFailure{Operation: "workflow-run decode", Message: err.Error()}
		return in
	}
	for _, r := range runs {
		if r.HeadSHA != in.CanonicalHeadOID {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.Workflow
		}
		in.WorkflowRuns = append(in.WorkflowRuns, prci.WorkflowRun{Name: name, Status: r.Status, Conclusion: r.Conclusion, CreatedAt: parseGitHubTime(r.CreatedAt), URL: r.URL})
	}
	return in
}

func decodeAnnotations(raw []byte) ([]ghAnnotation, error) {
	var pages [][]ghAnnotation
	if err := json.Unmarshal(raw, &pages); err == nil {
		var out []ghAnnotation
		for _, page := range pages {
			out = append(out, page...)
		}
		return out, nil
	}
	var flat []ghAnnotation
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}

func accessInput(in prci.Input, operation, out string, err error) prci.Input {
	message := strings.TrimSpace(out)
	if message == "" {
		message = err.Error()
	}
	in.AccessFailure = &prci.AccessFailure{Operation: operation, Message: message}
	return in
}

func parseGitHubTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
